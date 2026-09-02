package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// TracerConfig configures the tracer provider.
type TracerConfig struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	// SampleRatio is the fraction of root spans recorded, in [0,1]. Non-root
	// spans always follow their parent's sampling decision (ParentBased),
	// which is what keeps a single trace from being split across the sampled
	// and unsampled worlds.
	SampleRatio float64
	// Exporter overrides the default slog exporter — this is the documented
	// seam for a future OTLP exporter. Pass nil to use NewSlogExporter(logger).
	Exporter sdktrace.SpanExporter
	Logger   *slog.Logger

	BatchTimeout   time.Duration
	MaxExportBatch int
	MaxQueueSize   int
	ExportTimeout  time.Duration
}

func (c TracerConfig) withDefaults() TracerConfig {
	if c.BatchTimeout <= 0 {
		c.BatchTimeout = 5 * time.Second
	}
	if c.MaxExportBatch <= 0 {
		c.MaxExportBatch = 256
	}
	if c.MaxQueueSize <= 0 {
		c.MaxQueueSize = 2048
	}
	if c.ExportTimeout <= 0 {
		c.ExportTimeout = 30 * time.Second
	}
	if c.SampleRatio < 0 {
		c.SampleRatio = 0
	}
	if c.SampleRatio > 1 {
		c.SampleRatio = 1
	}
	return c
}

// NewTracerProvider builds an SDK tracer provider with resource attributes, a
// batch span processor, and either the given exporter or the built-in
// slog-based one. The returned provider is also installed as the global
// provider via otel.SetTracerProvider, so internal/transport/http and the
// adapters can call otel.Tracer(name) without threading the provider through
// every constructor.
func NewTracerProvider(cfg TracerConfig) *sdktrace.TracerProvider {
	cfg = cfg.withDefaults()
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	res := resource.NewWithAttributes(
		"https://opentelemetry.io/schemas/1.26.0",
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.version", cfg.ServiceVersion),
		attribute.String("deployment.environment", cfg.Environment),
	)

	exporter := cfg.Exporter
	if exporter == nil {
		exporter = NewSlogExporter(cfg.Logger)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(cfg.BatchTimeout),
			sdktrace.WithMaxExportBatchSize(cfg.MaxExportBatch),
			sdktrace.WithMaxQueueSize(cfg.MaxQueueSize),
			sdktrace.WithExportTimeout(cfg.ExportTimeout),
		),
	)
	otel.SetTracerProvider(tp)
	return tp
}

// Shutdown flushes any buffered spans and stops the provider. Callers pass a
// context bounded by the server's own shutdown timeout so a stuck exporter
// cannot hang process termination indefinitely.
func Shutdown(ctx context.Context, tp *sdktrace.TracerProvider) error {
	if tp == nil {
		return nil
	}
	return tp.Shutdown(ctx)
}

// Tracer is sugar for otel.Tracer(name), kept here so call sites in transport
// and infrastructure import one package for both the provider and the
// accessor.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }
