package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/anthropic"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/bedrock"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/deterministic"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/fallback"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/middleware"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/rag"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/telemetry"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// buildLLM resolves the model provider and wraps it in the full stack.
//
// Two wrappers are applied unconditionally and in a fixed order, and both
// matter:
//
//   - middleware.Chain (tracing, metrics, audit, rate limit, circuit
//     breaker, cache, sanitizing) goes *inside*, closest to the network, so
//     that every real provider call is observed, throttled, breaker-guarded
//     and sanitized.
//   - fallback.Provider goes *outside* that, so a chain rejection — a
//     tripped breaker, an exhausted quota — is itself a fallback trigger and
//     the answer degrades to the deterministic provider rather than
//     surfacing as an error. Reversing the two would put the breaker
//     downstream of the fallback, where it would never see the failure it is
//     supposed to count.
//
// The scripted provider is wrapped identically rather than short-circuited.
// It costs almost nothing, and it means the demo and CI exercise the same
// middleware code paths production does — a rate-limit or sanitizer bug
// found in CI is a bug found before it reaches a customer's model traffic.
func buildLLM(cfg *config.Config, metrics *telemetry.Metrics, cache ports.Cache, logger *slog.Logger) (ports.LLMProvider, error) {
	var primary ports.LLMProvider

	switch cfg.LLM.Provider {
	case config.LLMProviderScripted:
		primary = deterministic.New()

	case config.LLMProviderAnthropic:
		key := cfg.LLM.APIKey.Value()
		if key == "" {
			return nil, fmt.Errorf("app: llm.provider is %q but llm.api_key resolved to an empty value", cfg.LLM.Provider)
		}
		primary = anthropic.New(anthropic.Config{
			APIKey:     key,
			Model:      cfg.LLM.Model,
			Timeout:    cfg.LLM.RequestTimeout,
			MaxRetries: cfg.LLM.MaxRetries,
		}, &http.Client{Timeout: cfg.LLM.RequestTimeout})

	case config.LLMProviderBedrock:
		bcfg, ok := bedrock.ConfigFromEnv()
		if !ok {
			return nil, fmt.Errorf("app: llm.provider is %q but no AWS credentials are present in the environment "+
				"for Bedrock to sign with (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY)", cfg.LLM.Provider)
		}
		if cfg.LLM.BedrockRegion != "" {
			bcfg.Region = cfg.LLM.BedrockRegion
		}
		if cfg.LLM.Model != "" {
			bcfg.ModelID = cfg.LLM.Model
		}
		if cfg.LLM.RequestTimeout > 0 {
			bcfg.Timeout = cfg.LLM.RequestTimeout
		}
		primary = bedrock.New(bcfg, &http.Client{Timeout: bcfg.Timeout})

	default:
		return nil, fmt.Errorf("app: unknown llm.provider %q (want anthropic, bedrock or scripted)", cfg.LLM.Provider)
	}

	chainCfg := middleware.ChainConfig{
		Cache:  middleware.DefaultCacheConfig(),
		Logger: logger,
	}
	chainCfg.RateLimit = middleware.DefaultRateLimitConfig()
	if cfg.LLM.RateLimitPerSecond > 0 {
		// The config expresses a per-second rate; the middleware works in
		// requests per minute. Rounding up rather than down means a config
		// of 0.5/s (one call every two seconds) still permits one call a
		// minute instead of zero, which would be an outage dressed as a
		// setting.
		rpm := int(cfg.LLM.RateLimitPerSecond*60 + 0.999)
		if rpm < 1 {
			rpm = 1
		}
		chainCfg.RateLimit.RequestsPerMinute = rpm
	}
	if metrics != nil {
		chainCfg.MetricsRegisterer = metrics.Registry
	}

	chained := middleware.Chain(primary, chainCfg)
	return fallback.New(chained, deterministic.New(), logger), nil
}

// buildKnowledge builds the RAG index and seeds it with the platform corpus
// (AWS pricing notes, FinOps principles, the rule catalogue, safe-change
// practice). Seeding at startup rather than lazily on the first copilot
// question means an empty index is a startup failure an operator sees, not a
// silently worse answer a user gets.
//
// The provider is passed so real embeddings are used when one is configured;
// rag.Store falls back to its deterministic hash embedder per call on any
// provider error, so a model outage degrades retrieval quality rather than
// breaking retrieval.
func buildKnowledge(ctx context.Context, provider ports.LLMProvider, logger *slog.Logger) (ports.KnowledgeStore, error) {
	store := rag.New(provider)
	if err := rag.SeedPlatformCorpus(ctx, store); err != nil {
		return nil, fmt.Errorf("app: seeding the platform knowledge corpus: %w", err)
	}
	n, err := store.Count(ctx, "")
	if err == nil {
		logger.Debug("indexed platform knowledge corpus", slog.Int("chunks", n))
	}
	return store, nil
}
