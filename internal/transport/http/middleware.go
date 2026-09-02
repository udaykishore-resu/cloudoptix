package http

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/auth"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/resilience"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/telemetry"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Middleware chain order — this is the one place that decides it, and every
// entry below explains why it sits where it does relative to its neighbours.
// A router built anywhere else in this package (routes.go, router.go) must
// apply these through Chain, never assemble its own subset in a different
// order, or the reasoning below stops describing what actually runs.
//
//  1. RequestID       — every later step (logs, traces, problem+json,
//     the audit record) needs a stable id to correlate against; nothing
//     downstream can generate one that would already be visible to an
//     earlier step, so it goes first.
//  2. RealIP           — the access log and the audit "actor" record want
//     the caller's real address, not the last hop's; must run before
//     logging.
//  3. OTelSpan          — starts the request's root span before anything
//     that should appear as a child of it (including the auth check,
//     which is worth its own span when it fails).
//  4. AccessLog         — wraps the response so it can report the final
//     status and duration; must be outside (started before, finished
//     after) everything that can write the response, which is
//     everything below it.
//  5. Recoverer         — a panic anywhere below this must become
//     problem+json, never a raw stack trace to the client and never a
//     crashed connection with no response at all; sits as close to the
//     top as possible so nothing below it can panic outside its reach.
//  6. CORS              — a preflight OPTIONS request must be answered
//     before authentication even looks at it — a browser's preflight
//     carries no Authorization header by design.
//  7. BodySizeLimit      — bounds what every later step (decode,
//     idempotency hashing) has to process; applying it before auth means
//     an oversized unauthenticated request is rejected without spending
//     a JWKS round trip on it first.
//  8. Timeout            — bounds the whole remaining chain, including
//     authentication's JWKS fetch and the eventual service call.
//  9. Authentication      — resolves the principal (and, for OIDC, the
//     tenant — see auth.Authenticator). Everything below this may assume
//     a principal is present.
//  10. TenantResolved     — a second, narrow check that authentication
//     actually attached a tenant-scoped principal; defence in depth
//     against a future auth path that forgets to.
//  11. RBAC               — applied per-route (routes.go), not globally
//     here, because the permission a route requires is a property of the
//     route, not a single value every request shares; conceptually it is
//     still "here" in the chain.
//  12. RateLimit           — a request that will be rejected as
//     unauthorized should not spend a token-bucket slot shared across the
//     tenant, so this runs after authentication, keyed by tenant.
//  13. AuditRecord         — records only after every earlier step that
//     could still reject the request has run, so a 401/403/429 is never
//     misrecorded as an audited mutation; and before the handler, using a
//     response-capturing wrapper, so the recorded outcome reflects what
//     actually happened.

// Deps bundles everything the middleware chain and the route table need.
type Deps struct {
	Services       ports.Services
	Auth           *auth.Authenticator
	Metrics        *telemetry.Metrics
	Logger         *slog.Logger
	Idempotency    IdempotencyStore
	RateLimiter    *resilience.KeyedLimiter // nil disables rate limiting
	MaxBodyBytes   int64
	RequestTimeout time.Duration
	CORSOrigins    []string
	// AuditAction, when the audit service is configured, is called for every
	// mutating request after it completes, so the whole platform's mutating
	// surface is audited from one place rather than each handler
	// remembering to call AuditService.Record itself.
	AuditEnabled bool
	// OpenAPISpec is the raw bytes of api/openapi.yaml, loaded once at
	// startup and served verbatim from GET /openapi.yaml (router.go) — see
	// serveOpenAPISpec's doc comment for why this is passed in rather than
	// read from disk per request.
	OpenAPISpec []byte
}

// Chain applies the full middleware stack, in the documented order, around
// handler.
func Chain(deps Deps, handler http.Handler) http.Handler {
	h := handler
	h = auditMiddleware(deps)(h)
	h = rateLimitMiddleware(deps)(h)
	h = tenantResolvedMiddleware(h)
	h = authenticationMiddleware(deps)(h)
	h = timeoutMiddleware(deps)(h)
	h = bodySizeLimitMiddleware(deps)(h)
	h = corsMiddleware(deps)(h)
	h = recovererMiddleware(deps)(h)
	h = accessLogMiddleware(deps)(h)
	h = otelSpanMiddleware(deps)(h)
	h = chimw.RealIP(h)
	h = chimw.RequestID(h)
	return h
}

// PublicChain applies every step of the documented order except the three
// that assume a principal exists — Authentication, TenantResolved and
// RateLimit (which is keyed by tenant) — plus AuditRecord, which has nothing
// to attribute a mutation to without one. router.go uses it for the routes
// that are unauthenticated by design (health, readiness, metrics, the
// OpenAPI document, and the whole Onboarding tag — see
// authenticationMiddleware's doc comment for why onboarding belongs here in
// full) rather than duplicating a hand-picked subset of the chain at each
// call site.
func PublicChain(deps Deps, handler http.Handler) http.Handler {
	h := handler
	h = timeoutMiddleware(deps)(h)
	h = bodySizeLimitMiddleware(deps)(h)
	h = corsMiddleware(deps)(h)
	h = recovererMiddleware(deps)(h)
	h = accessLogMiddleware(deps)(h)
	h = otelSpanMiddleware(deps)(h)
	h = chimw.RealIP(h)
	h = chimw.RequestID(h)
	return h
}

// --- 3. OTel span ----------------------------------------------------------

func otelSpanMiddleware(deps Deps) func(http.Handler) http.Handler {
	tracer := telemetry.Tracer("cloudoptix/transport/http")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), r.Method+" "+routePattern(r),
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.target", r.URL.Path),
				))
			defer span.End()
			rec := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(ctx))
			span.SetAttributes(attribute.Int("http.status_code", rec.status))
			if rec.status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(rec.status))
			}
		})
	}
}

func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
		return rctx.RoutePattern()
	}
	return r.URL.Path
}

// --- 4. Access log -----------------------------------------------------

func accessLogMiddleware(deps Deps) func(http.Handler) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			elapsed := time.Since(start)

			route := routePattern(r)
			if deps.Metrics != nil {
				deps.Metrics.ObserveHTTPRequest(route, r.Method, rec.status, elapsed)
			}
			attrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", route),
				slog.Int("status", rec.status),
				slog.Duration("duration", elapsed),
				slog.String("remote_addr", r.RemoteAddr),
				slog.String("request_id", chimw.GetReqID(r.Context())),
			}
			if p, ok := PrincipalFrom(r.Context()); ok {
				attrs = append(attrs, slog.String("tenant_id", p.TenantID.String()), slog.String("actor", p.Describe()))
			}
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			} else if rec.status >= 400 {
				level = slog.LevelWarn
			}
			logger.LogAttrs(r.Context(), level, "http request", toSlogAttrs(attrs)...)
		})
	}
}

func toSlogAttrs(vals []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(vals))
	for _, v := range vals {
		if a, ok := v.(slog.Attr); ok {
			out = append(out, a)
		}
	}
	return out
}

// --- 5. Recoverer --------------------------------------------------------

func recovererMiddleware(deps Deps) func(http.Handler) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// The stack trace goes to the operator's log, never to
					// the client — a stack trace in an API response is a
					// disclosure bug, not a debugging convenience.
					logger.ErrorContext(r.Context(), "panic recovered",
						slog.Any("panic", rec), slog.String("stack", string(debug.Stack())))
					WriteProblem(w, r, core.NewError(core.ErrUnavailable, "internal_panic", "an unexpected error occurred"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// --- 6. CORS ---------------------------------------------------------------

func corsMiddleware(deps Deps) func(http.Handler) http.Handler {
	origins := deps.CORSOrigins
	if len(origins) == 0 {
		// No CORS origins configured means no browser-based cross-origin
		// caller is expected — same-origin and server-to-server calls are
		// unaffected either way, since CORS is enforced by the browser, not
		// this server, against everyone else.
		return func(next http.Handler) http.Handler { return next }
	}
	return cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", auth.TenantHeader, IdempotencyHeader, "X-CloudOptix-Api-Key"},
		ExposedHeaders:   []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After", RequestIDHeader},
		AllowCredentials: true,
		MaxAge:           300,
	})
}

// RequestIDHeader is the response header carrying the request id chi's
// RequestID middleware generates, for a client to include when reporting an
// issue.
const RequestIDHeader = "X-Request-Id"

// --- 7. Body size limit ------------------------------------------------

func bodySizeLimitMiddleware(deps Deps) func(http.Handler) http.Handler {
	limit := deps.MaxBodyBytes
	if limit <= 0 {
		limit = 5 << 20
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// --- 8. Timeout --------------------------------------------------------

func timeoutMiddleware(deps Deps) func(http.Handler) http.Handler {
	d := deps.RequestTimeout
	if d <= 0 {
		d = 25 * time.Second
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// --- 9. Authentication ---------------------------------------------------

// authenticationMiddleware guards every route except the small set mounted
// outside auth entirely (health, metrics, the OpenAPI document itself, and
// the whole Onboarding tag). Onboarding is unauthenticated end to end, not
// just at Start: a prospective customer has no CloudOptix identity and no
// tenant for the entire conversation, right up until Approve is the single
// call that creates one — a mid-conversation principal requirement would
// lock out exactly the signups this flow exists for. The conversation id
// Start hands back is what actually gates every later onboarding call (see
// handlers_onboarding.go's type doc comment), which is also why none of
// those handlers read a principal from context. router.go mounts the
// Onboarding tag in a router group that never applies this middleware, RBAC,
// tenant resolution or tenant-keyed rate limiting at all; which routes skip
// auth is not a header or a path convention this middleware checks, because
// that decision belongs in the router next to the rest of the route table,
// not duplicated as a second list here.
func authenticationMiddleware(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if deps.Auth == nil {
				WriteProblem(w, r, core.NewError(core.ErrUnavailable, "auth_not_configured", "authentication is not configured on this deployment"))
				return
			}
			res, err := deps.Auth.Authenticate(r)
			if err != nil {
				WriteProblem(w, r, err)
				return
			}
			ctx := withAuthResult(r.Context(), res)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// --- 10. Tenant resolved check ------------------------------------------

func tenantResolvedMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := core.TenantFrom(r.Context()); !ok {
			WriteProblem(w, r, core.NewError(core.ErrUnauthenticated, "tenant_unresolved", "request has no resolved tenant scope"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- 12. Rate limit ------------------------------------------------------

func rateLimitMiddleware(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if deps.RateLimiter == nil {
				next.ServeHTTP(w, r)
				return
			}
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			bucket := deps.RateLimiter.Bucket(p.TenantID.String())
			w.Header().Set("X-RateLimit-Remaining", itoa(bucket.Remaining()))
			if !bucket.Allow() {
				retryAfter := bucket.RetryAfter(1)
				w.Header().Set("Retry-After", itoa(int(retryAfter.Seconds())+1))
				WriteProblem(w, r, core.NewError(core.ErrThrottled, "rate_limited", "tenant %s exceeded its API rate limit", p.TenantID))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func itoa(n int) string {
	if n < 0 {
		n = 0
	}
	// Avoids importing strconv twice across this file for one tiny
	// conversion used only for header values.
	buf := [20]byte{}
	i := len(buf)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// --- 13. Audit -------------------------------------------------------------

func auditMiddleware(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !deps.AuditEnabled || deps.Services.Audit == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isMutating(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			rec := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			p, ok := PrincipalFrom(r.Context())
			if !ok {
				return // an unauthenticated mutating request never reached a handler worth auditing
			}
			outcome := "success"
			if rec.status >= 400 {
				outcome = "failure"
			}
			entry := ports.AuditEntry{
				Action:  r.Method + " " + routePattern(r),
				Outcome: outcome,
				Actor:   p.Describe(),
				Machine: p.Machine,
				Subject: routePattern(r),
				Message: "API request",
				Metadata: map[string]any{
					"status": rec.status, "path": r.URL.Path,
					"request_id": chimw.GetReqID(r.Context()),
				},
				At: time.Now().UTC(),
			}
			// Recording is best-effort and asynchronous with respect to the
			// response the client already received: a slow or failing audit
			// sink must never turn into added request latency or a failed
			// mutation that otherwise succeeded. The audit service's own
			// implementation is responsible for its durability guarantees;
			// this call site's job is only to never block the response on it.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = core.WithPrincipal(ctx, p)
				if err := deps.Services.Audit.Record(ctx, entry); err != nil && deps.Logger != nil {
					deps.Logger.Error("audit record failed", slog.String("error", err.Error()))
				}
			}()
		})
	}
}

// statusCapturingWriter records the status code written so middleware
// wrapping a handler (access log, audit, OTel) can report it, without
// buffering the body the way idempotency.go's capturingWriter must.
type statusCapturingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusCapturingWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush lets a statusCapturingWriter sit in front of an SSE handler without
// breaking streaming — http.ResponseController (Go 1.20+) prefers this over
// a type assertion, but chi's own middleware and several proxies still probe
// for http.Flusher directly, so implementing it here keeps every ordering of
// this middleware in the chain safe for a streaming response.
func (w *statusCapturingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
