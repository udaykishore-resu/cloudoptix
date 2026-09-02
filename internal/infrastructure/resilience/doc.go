// Package resilience provides the primitives that keep CloudOptix stable when
// AWS, an LLM provider, or a downstream dependency slows down, throttles, or
// fails outright: retry with full-jitter exponential backoff, a circuit
// breaker, a token-bucket rate limiter, a bounded worker pool, and a deadline
// propagation helper.
//
// The key design decision is that every primitive here is generic — none of
// them import internal/domain or internal/ports. An AWS SDK call, an LLM
// completion and an outbound webhook all fail in the same shapes (timeout,
// throttle, 5xx), and coupling the retry loop to core.Retryable would force
// every unrelated caller to accept CloudOptix's error taxonomy. Instead each
// primitive takes a classifier function, and the adapters that call AWS and
// the LLM provider supply their own using core.Retryable as the default. That
// keeps this package reusable by anything and testable without constructing a
// single domain object.
//
// Traceability: REQ-API-041 (resilient outbound calls), SPEC-API-014.
package resilience
