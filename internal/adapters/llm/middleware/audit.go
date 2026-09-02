package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// AuditingProvider wraps a ports.LLMProvider with a structured log/slog
// record of every call: who (tenant, purpose), what (provider, model, tool
// names offered), and what came back (tokens, latency, stop reason, error).
//
// This is deliberately a plain slog record rather than an entry in the
// tamper-evident audit.Record chain (ports.AuditRepository): that chain
// exists for consequential domain actions — a spec approved, a role
// assumed, a policy changed — where a tenant needs a verifiable history of
// what CloudOptix did to their account. An LLM call by itself does nothing:
// it produces text and read-only tool results, never a mutation. Logging it
// through slog gives operators the same call-by-call visibility (searchable,
// exportable to any log sink) without growing the compliance chain with an
// entry for every "what is my most expensive service" question, and without
// requiring every provider-chain construction site to be handed a
// tenant-scoped AuditRepository just to make a model call.
type AuditingProvider struct {
	inner  ports.LLMProvider
	logger *slog.Logger
}

var _ ports.LLMProvider = (*AuditingProvider)(nil)

// NewAuditingProvider wraps inner. A nil logger falls back to slog.Default().
func NewAuditingProvider(inner ports.LLMProvider, logger *slog.Logger) *AuditingProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuditingProvider{inner: inner, logger: logger}
}

func (a *AuditingProvider) Name() string { return a.inner.Name() }

func (a *AuditingProvider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	start := time.Now()
	resp, err := a.inner.Complete(ctx, req)
	elapsed := time.Since(start)

	toolNames := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		toolNames = append(toolNames, t.Name)
	}

	if err != nil {
		a.logger.LogAttrs(ctx, slog.LevelWarn, "llm_call",
			slog.String("provider", a.inner.Name()),
			slog.String("tenant_id", string(req.TenantID)),
			slog.String("purpose", req.Purpose),
			slog.Any("tools_offered", toolNames),
			slog.Duration("elapsed", elapsed),
			slog.String("error", err.Error()),
		)
		return resp, err
	}

	a.logger.LogAttrs(ctx, slog.LevelInfo, "llm_call",
		slog.String("provider", a.inner.Name()),
		slog.String("tenant_id", string(req.TenantID)),
		slog.String("purpose", req.Purpose),
		slog.String("model", resp.Model),
		slog.Any("tools_offered", toolNames),
		slog.Int("tool_calls_made", len(resp.ToolCalls)),
		slog.String("stop_reason", resp.StopReason),
		slog.Int("input_tokens", resp.InputTokens),
		slog.Int("output_tokens", resp.OutputTokens),
		slog.Int64("provider_latency_ms", resp.LatencyMS),
		slog.Duration("elapsed", elapsed),
		slog.Bool("structured_output", req.ResponseSchema != nil),
	)
	return resp, nil
}

func (a *AuditingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	start := time.Now()
	vecs, err := a.inner.Embed(ctx, texts)
	elapsed := time.Since(start)
	if err != nil {
		a.logger.LogAttrs(ctx, slog.LevelWarn, "llm_embed",
			slog.String("provider", a.inner.Name()),
			slog.Int("text_count", len(texts)),
			slog.Duration("elapsed", elapsed),
			slog.String("error", err.Error()),
		)
		return vecs, err
	}
	a.logger.LogAttrs(ctx, slog.LevelInfo, "llm_embed",
		slog.String("provider", a.inner.Name()),
		slog.Int("text_count", len(texts)),
		slog.Duration("elapsed", elapsed),
	)
	return vecs, nil
}

func (a *AuditingProvider) Healthy(ctx context.Context) bool { return a.inner.Healthy(ctx) }
