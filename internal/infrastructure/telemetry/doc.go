// Package telemetry wires OpenTelemetry tracing, Prometheus metrics and
// structured logging for the whole platform.
//
// The design decision that shapes this package: the module proxy is blocked,
// so no OTLP exporter package is available (go.opentelemetry.io/otel/exporters/...
// cannot be fetched). Rather than leaving tracing unimplemented, this package
// writes its own minimal span exporter that renders finished spans as
// structured slog records — every span still gets sampled, batched and
// flushed through the real SDK's BatchSpanProcessor, and correlates with logs
// automatically because it *is* a log line. The seam for a real OTLP exporter
// is exactly one interface (trace.SpanExporter, from the SDK) — see
// tracer.go — so swapping in go.opentelemetry.io/otel/exporters/otlp/... once
// the module proxy is available is a one-line change at NewTracerProvider's
// call site, not a redesign.
//
// Traceability: REQ-OPS-004 (observability), SPEC-OPS-002.
package telemetry
