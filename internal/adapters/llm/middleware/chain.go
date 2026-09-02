// Package middleware wraps any ports.LLMProvider with the cross-cutting
// concerns every call needs regardless of which model backend answers it:
// tracing, metrics, per-tenant rate limiting and quota enforcement, a
// circuit breaker, a response cache, structured audit logging, and defence
// against prompt injection carried in tool results. Each concern is its own
// decorator implementing ports.LLMProvider around an inner one; Chain
// composes them in the one order that makes each layer's guarantee hold.
//
// KEY DESIGN DECISION: order is load-bearing, not cosmetic. Sanitizing must
// run innermost (closest to the network call) of the request-mutating
// layers so nothing downstream of it — the cache key, the audit log, the
// model itself — ever sees an unescaped tool result. Caching sits just
// outside sanitizing so a cache hit is keyed on the exact sanitized bytes
// that were (or would be) sent. Rate limiting and the circuit breaker sit
// outside the cache, so a cache hit costs neither a token from the tenant's
// quota nor a trip through the breaker — a cached answer is free by
// definition, it made no call. Metrics and tracing sit outermost so they
// observe the full behaviour of every layer beneath them, including
// throttling and breaker rejections, which are exactly the events an
// operator most wants a span and a counter for.
//
// This package builds providers; it never becomes one that mutates
// anything. Every layer here only observes, limits, caches or logs a call —
// none of it can turn a read into a write, which keeps "AI-assisted, not
// AI-controlled" true at the one place all model traffic passes through.
//
// Traceability: REQ-AI-004, REQ-AI-005, REQ-AI-007, SPEC-AI-002.
package middleware

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ChainConfig collects the tunables for every layer Chain installs. A zero
// ChainConfig is legal — every layer falls back to its own package default —
// so callers only need to set what they want to override.
type ChainConfig struct {
	RateLimit      RateLimitConfig
	CircuitBreaker CircuitBreakerConfig
	Cache          CacheConfig
	// MetricsRegisterer receives the Prometheus collectors; nil disables
	// metrics registration but still wraps the provider (Complete/Embed just
	// pass through without recording).
	MetricsRegisterer prometheus.Registerer
	// Logger receives structured audit records; nil uses slog.Default().
	Logger *slog.Logger
}

// Chain wraps inner with the full standard middleware stack in the fixed
// order described in the package doc: Tracing(Metrics(Audit(RateLimit(
// CircuitBreaker(Cache(Sanitize(inner))))))).
//
// Read outside-in, a call passes: trace span opens -> metric timer starts ->
// audit log will record the outcome -> rate limit and quota checked ->
// circuit breaker checked -> cache checked (short-circuits here on a hit) ->
// tool-result content sanitized -> the real provider is called.
func Chain(inner ports.LLMProvider, cfg ChainConfig) ports.LLMProvider {
	var p ports.LLMProvider = inner
	p = NewSanitizingProvider(p)
	p = NewCachingProvider(p, cfg.Cache)
	p = NewCircuitBreakerProvider(p, cfg.CircuitBreaker)
	p = NewRateLimitProvider(p, cfg.RateLimit)
	p = NewAuditingProvider(p, cfg.Logger)
	p = NewMetricsProvider(p, cfg.MetricsRegisterer)
	p = NewTracingProvider(p)
	return p
}
