package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/server"
)

// NewRouter builds the whole HTTP surface: the three health endpoints, the
// Prometheus scrape endpoint, the OpenAPI document, the unauthenticated
// Onboarding tag, and every route in BuildRoutes wrapped in the full
// authenticated middleware chain plus its per-route RBAC check.
//
// This is the one place the platform's routing decisions come together —
// which surfaces skip authentication and why is documented once, on
// authenticationMiddleware in middleware.go; what each route requires is
// documented once, in routes.go; how they compose is this function and
// nothing else.
func NewRouter(deps Deps, health *server.Health) http.Handler {
	r := chi.NewRouter()

	// Health, readiness and metrics are polled by infrastructure (the
	// orchestrator, Prometheus) that has no CloudOptix credential and no
	// tenant — requiring auth on them would mean the load balancer's own
	// health check needs a bearer token, which defeats the point.
	if health != nil {
		r.Get("/healthz", health.LivenessHandler())
		r.Get("/readyz", health.ReadinessHandler())
		r.Get("/health", health.HealthHandler())
	}
	if deps.Metrics != nil {
		r.Handle("/metrics", deps.Metrics.Handler())
	}
	r.Get("/openapi.yaml", serveOpenAPISpec(deps.OpenAPISpec))

	// Onboarding: public end to end (see authenticationMiddleware's doc
	// comment in middleware.go for why), mounted under PublicChain rather
	// than Chain, and never wrapped in RequirePermission.
	r.Route("/api/v1", func(api chi.Router) {
		api.Group(func(pub chi.Router) {
			for _, rt := range BuildPublicRoutes(deps.Services) {
				pub.Method(rt.Method, rt.Pattern, PublicChain(deps, rt.Handler))
			}
		})

		api.Group(func(auth chi.Router) {
			for _, rt := range BuildRoutes(deps.Services) {
				handler := RequirePermission(rt.Permission)(rt.Handler)
				auth.Method(rt.Method, rt.Pattern, Chain(deps, handler))
			}
		})
	})

	return r
}

// serveOpenAPISpec serves a pre-loaded OpenAPI document from memory rather
// than reading api/openapi.yaml off disk on every request — the document is
// small, changes only on deploy, and this keeps the handler independent of
// the process's working directory. deps.OpenAPISpec is expected to be
// loaded once at startup (main reads api/openapi.yaml) and passed in; an
// empty spec renders 404 rather than an empty 200, so a misconfigured
// deployment fails obviously instead of serving a blank document that looks
// like an empty API.
func serveOpenAPISpec(spec []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(spec) == 0 {
			WriteProblem(w, r, core.NotFound("openapi_spec", "openapi.yaml"))
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(spec)
	}
}
