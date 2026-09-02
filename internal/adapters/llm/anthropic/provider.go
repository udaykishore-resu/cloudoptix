// Package anthropic implements ports.LLMProvider over the Anthropic Messages
// API (api.anthropic.com) using net/http directly rather than a generated SDK
// client, because the module proxy CloudOptix builds against is blocked and
// no Anthropic Go SDK is in the allowed dependency set. The wire format
// itself — the part that actually needs to be correct — lives in the sibling
// anthropicwire package, shared with the Bedrock transport in
// internal/adapters/llm/bedrock, so this file is concerned only with HTTP:
// authentication, retries, timeouts and token accounting.
//
// Traceability: REQ-AI-001, REQ-AI-006, SPEC-AI-001.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/anthropicwire"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const (
	defaultBaseURL       = "https://api.anthropic.com"
	defaultAPIVersion    = "2023-06-01"
	defaultModel         = "claude-sonnet-4-5-20250929"
	defaultTimeout       = 60 * time.Second
	defaultMaxRetries    = 4
	defaultRetryBase     = 500 * time.Millisecond
	defaultRetryMax      = 8 * time.Second
	messagesPathTemplate = "/v1/messages"
)

// Config holds everything the provider needs, read from the environment by
// ConfigFromEnv so that no code path ever hard-codes a key.
type Config struct {
	APIKey     string
	BaseURL    string
	APIVersion string
	Model      string
	Timeout    time.Duration
	MaxRetries int
}

// ConfigFromEnv reads ANTHROPIC_API_KEY (required), ANTHROPIC_BASE_URL,
// ANTHROPIC_MODEL, ANTHROPIC_API_VERSION and ANTHROPIC_TIMEOUT_SECONDS. It
// returns ok=false when no API key is set, which is how callers decide
// whether this provider is even constructible — CloudOptix runs perfectly
// well with none configured, defaulting to the deterministic provider.
func ConfigFromEnv() (Config, bool) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return Config{}, false
	}
	cfg := Config{
		APIKey:     key,
		BaseURL:    envOr("ANTHROPIC_BASE_URL", defaultBaseURL),
		APIVersion: envOr("ANTHROPIC_API_VERSION", defaultAPIVersion),
		Model:      envOr("ANTHROPIC_MODEL", defaultModel),
		Timeout:    defaultTimeout,
		MaxRetries: defaultMaxRetries,
	}
	if s := os.Getenv("ANTHROPIC_TIMEOUT_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.Timeout = time.Duration(n) * time.Second
		}
	}
	if s := os.Getenv("ANTHROPIC_MAX_RETRIES"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			cfg.MaxRetries = n
		}
	}
	return cfg, true
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Provider implements ports.LLMProvider over the Anthropic Messages API.
type Provider struct {
	cfg    Config
	client *http.Client
}

var _ ports.LLMProvider = (*Provider)(nil)

// New builds a Provider. httpClient may be nil, in which case one is built
// from cfg.Timeout.
func New(cfg Config, httpClient *http.Client) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = defaultAPIVersion
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &Provider{cfg: cfg, client: httpClient}
}

// Name satisfies ports.LLMProvider.
func (p *Provider) Name() string { return "anthropic:" + p.cfg.Model }

// Complete satisfies ports.LLMProvider: builds the wire request, retries on
// 429 and 5xx with exponential backoff, and parses the wire response back
// into the port's shape.
func (p *Provider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	wireReq := anthropicwire.BuildRequest(req)
	wireReq.Model = p.cfg.Model

	body, err := json.Marshal(wireReq)
	if err != nil {
		return ports.CompletionResponse{}, core.Invalid("anthropic: encoding request: %v", err)
	}

	maxRetries := p.cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := anthropicwire.Backoff(attempt-1, defaultRetryBase, defaultRetryMax)
			select {
			case <-ctx.Done():
				return ports.CompletionResponse{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		start := time.Now()
		resp, status, retryable, err := p.doRequest(ctx, body)
		latency := time.Since(start).Milliseconds()
		if err == nil {
			out := anthropicwire.ParseResponse(resp, p.cfg.Model, latency)
			return out, nil
		}
		lastErr = err
		if !retryable || attempt == maxRetries {
			break
		}
		_ = status
	}
	return ports.CompletionResponse{}, lastErr
}

// doRequest performs one HTTP round trip. retryable reports whether the
// failure is worth another attempt: 429 (rate limited) and any 5xx are
// retried; 4xx other than 429 is a client error that will not improve on
// retry.
func (p *Provider) doRequest(ctx context.Context, body []byte) (anthropicwire.Response, int, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+messagesPathTemplate, bytes.NewReader(body))
	if err != nil {
		return anthropicwire.Response{}, 0, false, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", p.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", p.cfg.APIVersion)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		// A network error (timeout, connection refused, DNS failure) is
		// always worth retrying: it carries no HTTP status to distinguish
		// client from server fault.
		return anthropicwire.Response{}, 0, true, core.NewError(core.ErrUnavailable, "anthropic_transport",
			"anthropic: request failed: %v", err).Wrap(err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return anthropicwire.Response{}, resp.StatusCode, true, readErr
	}

	if resp.StatusCode != http.StatusOK {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		msg := anthropicwire.ParseErrorBody(respBody)
		sentinel := core.ErrUnavailable
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			sentinel = core.ErrThrottled
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			sentinel = core.ErrForbidden
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			sentinel = core.ErrInvalidInput
		}
		return anthropicwire.Response{}, resp.StatusCode, retryable,
			core.NewError(sentinel, "anthropic_api_error", "anthropic: %s (status %d)", msg, resp.StatusCode)
	}

	var wireResp anthropicwire.Response
	if err := json.Unmarshal(respBody, &wireResp); err != nil {
		return anthropicwire.Response{}, resp.StatusCode, false,
			fmt.Errorf("anthropic: decoding response: %w", err)
	}
	return wireResp, resp.StatusCode, false, nil
}

// Embed satisfies ports.LLMProvider. The Anthropic Messages API has no
// embeddings endpoint — Anthropic does not publish one — so this returns
// ErrNotImplemented rather than fabricating a call that does not exist.
// internal/adapters/rag.Store treats an Embed failure as the documented
// signal to fall back to its deterministic hashing embedder, so retrieval
// keeps working with this provider configured; only the semantic-similarity
// half of hybrid search loses provider-quality embeddings.
func (p *Provider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, core.NewError(core.ErrNotImplemented, "anthropic_no_embeddings",
		"anthropic: the Messages API has no embeddings endpoint")
}

// Healthy satisfies ports.LLMProvider with a lightweight, cheap probe: the
// smallest legal Messages request the API accepts. It intentionally does not
// use the tenant's budget for a real completion; a 1-token response is
// enough to prove the key and network path both work.
func (p *Provider) Healthy(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req := ports.CompletionRequest{
		Purpose:   "health_check",
		Messages:  []ports.Message{{Role: ports.RoleUser, Content: "ping"}},
		MaxTokens: 1,
	}
	_, err := p.Complete(probeCtx, req)
	return err == nil
}
