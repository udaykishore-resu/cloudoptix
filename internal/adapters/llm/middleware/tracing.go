package middleware

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// tracerName is the OpenTelemetry instrumentation scope every span from this
// package is recorded under.
const tracerName = "github.com/udaykishore-resu/cloudoptix/internal/adapters/llm"

// TracingProvider wraps a ports.LLMProvider with an OpenTelemetry span per
// call, carrying the attributes an operator actually reaches for when
// diagnosing a slow or expensive AI path: provider name, tenant, purpose,
// token counts and stop reason. It registers no exporter itself — that is a
// process-wide concern configured once at startup — so with no SDK
// configured these spans are simply discarded by the default no-op tracer,
// and the provider behaves exactly as if untraced.
type TracingProvider struct {
	inner  ports.LLMProvider
	tracer trace.Tracer
}

var _ ports.LLMProvider = (*TracingProvider)(nil)

// NewTracingProvider wraps inner.
func NewTracingProvider(inner ports.LLMProvider) *TracingProvider {
	return &TracingProvider{inner: inner, tracer: otel.Tracer(tracerName)}
}

func (t *TracingProvider) Name() string { return t.inner.Name() }

func (t *TracingProvider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	ctx, span := t.tracer.Start(ctx, "llm.complete", trace.WithAttributes(
		attribute.String("llm.provider", t.inner.Name()),
		attribute.String("llm.purpose", req.Purpose),
		attribute.String("cloudoptix.tenant_id", string(req.TenantID)),
		attribute.Int("llm.tools_offered", len(req.Tools)),
		attribute.Bool("llm.structured_output", req.ResponseSchema != nil),
	))
	defer span.End()

	resp, err := t.inner.Complete(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}
	span.SetAttributes(
		attribute.Int("llm.input_tokens", resp.InputTokens),
		attribute.Int("llm.output_tokens", resp.OutputTokens),
		attribute.String("llm.stop_reason", resp.StopReason),
		attribute.Int("llm.tool_calls", len(resp.ToolCalls)),
	)
	span.SetStatus(codes.Ok, "")
	return resp, nil
}

func (t *TracingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, span := t.tracer.Start(ctx, "llm.embed", trace.WithAttributes(
		attribute.String("llm.provider", t.inner.Name()),
		attribute.Int("llm.embed_count", len(texts)),
	))
	defer span.End()
	vecs, err := t.inner.Embed(ctx, texts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return vecs, err
}

func (t *TracingProvider) Healthy(ctx context.Context) bool { return t.inner.Healthy(ctx) }
