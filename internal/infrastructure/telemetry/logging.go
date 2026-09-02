package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

func defaultOutput() io.Writer { return os.Stdout }

// LogConfig configures the platform's slog handler.
type LogConfig struct {
	Level  string // debug | info | warn | error
	Format string // json | text
	Output io.Writer
}

// NewLogger builds the process-wide *slog.Logger: JSON output (structured
// log aggregation is how CloudOptix's own operators find an incident, and
// text output is only for a developer's terminal), trace-id/span-id
// correlation pulled from the context on every call, and redaction of
// anything that looks like a secret, token or AWS credential.
//
// The redaction is a wrapping slog.Handler, not a convention teams are asked
// to follow at each call site. A rule enforced by review ("never log the
// Authorization header") gets broken the first time someone logs a whole
// struct with %+v; a rule enforced by the handler that inspects every
// attribute before it reaches the sink cannot be broken that way.
func NewLogger(cfg LogConfig) *slog.Logger {
	if cfg.Output == nil {
		cfg.Output = defaultOutput()
	}
	level := parseLevel(cfg.Level)

	var base slog.Handler
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr}
	if cfg.Format == "text" {
		base = slog.NewTextHandler(cfg.Output, opts)
	} else {
		base = slog.NewJSONHandler(cfg.Output, opts)
	}
	return slog.New(&traceCorrelatingHandler{next: base})
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// --- trace correlation ---------------------------------------------------

// traceCorrelatingHandler adds trace_id and span_id attributes drawn from the
// context's active OpenTelemetry span, when one is present, so every log
// line emitted while handling a traced request can be joined against that
// request's spans (including the ones the slog span exporter itself writes)
// in any log aggregator without the caller remembering to add them.
type traceCorrelatingHandler struct {
	next slog.Handler
}

func (h *traceCorrelatingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *traceCorrelatingHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.next.Handle(ctx, r)
}

func (h *traceCorrelatingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceCorrelatingHandler{next: h.next.WithAttrs(attrs)}
}

func (h *traceCorrelatingHandler) WithGroup(name string) slog.Handler {
	return &traceCorrelatingHandler{next: h.next.WithGroup(name)}
}

// --- redaction -------------------------------------------------------------

const redactedPlaceholder = "***REDACTED***"

// sensitiveKeyPattern matches attribute keys that name a secret outright:
// password, token, api_key, secret, authorization, credential(s), and their
// common variants (case-insensitive, with or without underscores).
var sensitiveKeyPattern = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|authoriz(a|e)tion|credential|private[_-]?key|access[_-]?key|session[_-]?key|client[_-]?secret|bearer)`)

// awsAccessKeyPattern matches an AWS access key id embedded in a value even
// when the attribute's own key gives no hint (e.g. a raw error message that
// happened to include one) — AKIA/ASIA followed by 16 alphanumerics is
// specific enough not to false-positive on ordinary text.
var awsAccessKeyPattern = regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)

// awsSecretKeyHintPattern matches the shape of an AWS secret access key (40
// base64-alphabet characters) only when it appears after something that
// looks like it is being assigned as a secret — a bare 40-character string
// is too common in hashes and IDs to redact unconditionally.
var awsSecretKeyHintPattern = regexp.MustCompile(`(?i)(secret[_-]?access[_-]?key\s*[:=]\s*)([A-Za-z0-9/+=]{40})`)

// bearerTokenPattern matches an inline "Bearer <token>" or "Basic <token>" in
// a free-text value such as a copied Authorization header.
var bearerTokenPattern = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9\-_.=/+]{8,}`)

// redactAttr is the slog.HandlerOptions.ReplaceAttr hook: it runs for every
// attribute at every level (including inside nested groups), before the
// attribute reaches the JSON/text encoder.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	if sensitiveKeyPattern.MatchString(a.Key) {
		return slog.String(a.Key, redactedPlaceholder)
	}
	if a.Value.Kind() == slog.KindString {
		if redacted, changed := redactString(a.Value.String()); changed {
			return slog.String(a.Key, redacted)
		}
	}
	return a
}

// redactString scrubs a free-text value for embedded secrets that a key-name
// check alone would miss — an error message that echoes back a URL with a
// credential in it, or a raw Authorization header value logged under an
// innocuous key like "header".
func redactString(s string) (string, bool) {
	changed := false
	if awsAccessKeyPattern.MatchString(s) {
		s = awsAccessKeyPattern.ReplaceAllString(s, redactedPlaceholder)
		changed = true
	}
	if awsSecretKeyHintPattern.MatchString(s) {
		s = awsSecretKeyHintPattern.ReplaceAllString(s, "${1}"+redactedPlaceholder)
		changed = true
	}
	if bearerTokenPattern.MatchString(s) {
		s = bearerTokenPattern.ReplaceAllStringFunc(s, func(m string) string {
			scheme, _, _ := strings.Cut(m, " ")
			return scheme + " " + redactedPlaceholder
		})
		changed = true
	}
	return s, changed
}
