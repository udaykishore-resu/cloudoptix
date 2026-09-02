// Package fallback wraps a primary ports.LLMProvider (Anthropic or Bedrock)
// with the deterministic provider as a degrade target, so a model outage
// never becomes a user-visible outage.
//
// KEY DESIGN DECISION: degradation is judged by Healthy(), checked once per
// call rather than only after Complete fails. This lets a caller that has
// its own reason to prefer speed (a circuit breaker already open, per
// internal/adapters/llm/middleware) skip straight to the deterministic
// answer instead of paying for one more doomed network attempt. A primary
// call that fails despite reporting healthy (a genuine one-off transient
// error) still falls back rather than surfacing an error to the end
// user — CloudOptix would rather hand back a slightly less nuanced,
// template-composed answer than tell a user "the AI is down, try again"
// when a perfectly good deterministic path exists to answer the same
// question.
//
// This does not weaken "AI-assisted, not AI-controlled": the deterministic
// provider is not a stub, it is a fully real (if template-driven) answer
// path exercised by CI, and Fallback never falls back into anything that
// mutates state — both providers on either side of this decorator only ever
// produce prose, structured extraction and tool-call requests.
//
// Traceability: REQ-AI-007, SPEC-AI-002.
package fallback

import (
	"context"
	"log/slog"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/deterministic"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Provider tries primary first and falls back to a deterministic responder
// when primary is unhealthy or its call fails.
type Provider struct {
	primary       ports.LLMProvider
	deterministic ports.LLMProvider
	logger        *slog.Logger
}

var _ ports.LLMProvider = (*Provider)(nil)

// New wraps primary with a fallback. det may be nil, in which case a fresh
// deterministic.New() provider is used — the common case, since the
// deterministic provider carries no configuration and is safe to share.
func New(primary ports.LLMProvider, det ports.LLMProvider, logger *slog.Logger) *Provider {
	if det == nil {
		det = deterministic.New()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{primary: primary, deterministic: det, logger: logger}
}

// Name reports the primary's name; callers that need to know whether an
// individual response was degraded should read CompletionResponse.Model,
// which the deterministic provider stamps with its own model name.
func (p *Provider) Name() string { return p.primary.Name() }

func (p *Provider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	if !p.primary.Healthy(ctx) {
		p.logger.WarnContext(ctx, "llm_fallback_degraded",
			slog.String("primary", p.primary.Name()),
			slog.String("reason", "primary_unhealthy"),
			slog.String("purpose", req.Purpose))
		return p.deterministic.Complete(ctx, req)
	}

	resp, err := p.primary.Complete(ctx, req)
	if err != nil {
		p.logger.WarnContext(ctx, "llm_fallback_degraded",
			slog.String("primary", p.primary.Name()),
			slog.String("reason", "primary_call_failed"),
			slog.String("error", err.Error()),
			slog.String("purpose", req.Purpose))
		return p.deterministic.Complete(ctx, req)
	}
	return resp, nil
}

// Embed follows the same degrade rule as Complete. Embeddings from the two
// providers are not guaranteed to share a vector space or dimensionality —
// a caller mixing embeddings across a degrade boundary (e.g. indexing with
// the primary, then falling back mid-outage) should re-embed once the
// primary recovers, the same operational expectation as any embedding model
// upgrade.
func (p *Provider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if !p.primary.Healthy(ctx) {
		return p.deterministic.Embed(ctx, texts)
	}
	vecs, err := p.primary.Embed(ctx, texts)
	if err != nil {
		return p.deterministic.Embed(ctx, texts)
	}
	return vecs, nil
}

// Healthy reports true whenever either provider can serve — the whole point
// of Fallback is that primary being down does not make the composed
// provider unhealthy, since the deterministic provider is always available.
func (p *Provider) Healthy(ctx context.Context) bool {
	return p.primary.Healthy(ctx) || p.deterministic.Healthy(ctx)
}
