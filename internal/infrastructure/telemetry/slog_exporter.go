package telemetry

import (
	"context"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SlogExporter is a trace.SpanExporter that renders each finished span as one
// structured slog record instead of shipping it to a collector. It exists
// because no OTLP exporter package is reachable through the blocked module
// proxy — see the package doc comment for the seam this leaves for a real one.
//
// Every span still goes through the SDK's real sampling and batching; this
// only replaces the last-mile "send bytes somewhere" step, which is exactly
// the piece of the OTLP exporter that isn't otherwise available to CloudOptix
// in this environment. The record includes trace_id and span_id in a form
// that matches the correlation fields the redacting slog handler
// (handler.go) adds to every request log line, so a trace and its request
// logs can be joined on those fields in any log aggregator.
type SlogExporter struct {
	logger *slog.Logger

	mu       sync.Mutex
	shutdown bool
}

// NewSlogExporter builds an exporter that writes to logger at Info level
// under the "span" log source.
func NewSlogExporter(logger *slog.Logger) *SlogExporter {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogExporter{logger: logger.With(slog.String("log_source", "span"))}
}

// ExportSpans implements sdktrace.SpanExporter.
func (e *SlogExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	shutdown := e.shutdown
	e.mu.Unlock()
	if shutdown {
		// The SDK batches spans asynchronously; a batch already in flight
		// when Shutdown is called must not panic writing to a closed sink.
		// slog has no "closed" state to hit, but honouring shutdown here
		// keeps the exporter's contract identical to one that does (e.g. an
		// OTLP exporter with a closed connection).
		return nil
	}
	for _, s := range spans {
		e.logSpan(s)
	}
	return nil
}

func (e *SlogExporter) logSpan(s sdktrace.ReadOnlySpan) {
	sc := s.SpanContext()
	attrs := []any{
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
		slog.String("span_name", s.Name()),
		slog.String("span_kind", s.SpanKind().String()),
		slog.Time("start_time", s.StartTime()),
		slog.Duration("duration", s.EndTime().Sub(s.StartTime())),
		slog.String("status_code", s.Status().Code.String()),
	}
	if parent := s.Parent(); parent.IsValid() {
		attrs = append(attrs, slog.String("parent_span_id", parent.SpanID().String()))
	}
	if s.Status().Code == codes.Error {
		attrs = append(attrs, slog.String("status_message", s.Status().Description))
	}
	for _, kv := range s.Attributes() {
		attrs = append(attrs, slog.String("attr."+string(kv.Key), kv.Value.Emit()))
	}
	for _, ev := range s.Events() {
		attrs = append(attrs, slog.String("event", ev.Name))
	}
	e.logger.LogAttrs(context.Background(), slog.LevelInfo, "span finished", toAttrs(attrs)...)
}

func toAttrs(vals []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(vals))
	for _, v := range vals {
		if a, ok := v.(slog.Attr); ok {
			out = append(out, a)
		}
	}
	return out
}

// Shutdown marks the exporter closed. It never errors: writing to slog cannot
// fail in a way this exporter should surface as a shutdown failure.
func (e *SlogExporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.shutdown = true
	e.mu.Unlock()
	return nil
}

var _ sdktrace.SpanExporter = (*SlogExporter)(nil)
