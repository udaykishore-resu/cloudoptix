package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/anthropicwire"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func testProvider(t *testing.T, handler http.HandlerFunc) (*Provider, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	p := New(Config{
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		APIVersion: defaultAPIVersion,
		Model:      "claude-test",
		Timeout:    5 * time.Second,
		MaxRetries: 3,
	}, srv.Client())
	return p, &calls
}

func TestComplete_HeadersAndAuth(t *testing.T) {
	p, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, defaultAPIVersion, r.Header.Get("anthropic-version"))
		assert.Equal(t, "application/json", r.Header.Get("content-type"))
		writeJSON(w, http.StatusOK, anthropicwire.Response{
			Model:      "claude-test",
			StopReason: "end_turn",
			Content:    []anthropicwire.ContentBlock{{Type: "text", Text: "hello"}},
			Usage:      anthropicwire.Usage{InputTokens: 10, OutputTokens: 5},
		})
	})
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", resp.Content)
	assert.Equal(t, 10, resp.InputTokens)
	assert.Equal(t, 5, resp.OutputTokens)
}

func TestComplete_ToolUseRoundTrips(t *testing.T) {
	p, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var req anthropicwire.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Len(t, req.Tools, 1)
		assert.Equal(t, "get_cost_summary", req.Tools[0].Name)
		writeJSON(w, http.StatusOK, anthropicwire.Response{
			StopReason: "tool_use",
			Content: []anthropicwire.ContentBlock{
				{Type: "text", Text: "Let me check."},
				{Type: "tool_use", ID: "call_1", Name: "get_cost_summary", Input: map[string]any{"period": "30d"}},
			},
			Usage: anthropicwire.Usage{InputTokens: 20, OutputTokens: 8},
		})
	})
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "why did cost go up"}},
		Tools: []ports.ToolDefinition{{
			Name: "get_cost_summary", Description: "cost summary", ReadOnly: true,
			Parameters: map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "get_cost_summary", resp.ToolCalls[0].Name)
	assert.Equal(t, "30d", resp.ToolCalls[0].Arguments["period"])
	assert.Equal(t, "Let me check.", resp.Content)
}

func TestComplete_StructuredOutputForcesToolChoice(t *testing.T) {
	p, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var req anthropicwire.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.NotNil(t, req.ToolChoice)
		assert.Equal(t, "tool", req.ToolChoice.Type)
		assert.Equal(t, anthropicwire.StructuredToolName, req.ToolChoice.Name)
		writeJSON(w, http.StatusOK, anthropicwire.Response{
			StopReason: "tool_use",
			Content: []anthropicwire.ContentBlock{
				{Type: "tool_use", ID: "call_1", Name: anthropicwire.StructuredToolName, Input: map[string]any{"organization_name": "Acme"}},
			},
		})
	})
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages:       []ports.Message{{Role: ports.RoleUser, Content: "We are Acme Corp"}},
		ResponseSchema: map[string]any{"type": "object", "properties": map[string]any{"organization_name": map[string]any{"type": "string"}}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Structured)
	assert.Equal(t, "Acme", resp.Structured["organization_name"])
	assert.Empty(t, resp.ToolCalls, "the structured-output tool call must not also appear as an ordinary tool call")
}

func TestComplete_RetriesOn429ThenSucceeds(t *testing.T) {
	p, calls := testProvider(t, func() http.HandlerFunc {
		n := 0
		return func(w http.ResponseWriter, r *http.Request) {
			n++
			if n < 3 {
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
				return
			}
			writeJSON(w, http.StatusOK, anthropicwire.Response{
				Content: []anthropicwire.ContentBlock{{Type: "text", Text: "ok after retry"}},
			})
		}
	}())
	// Speed the test up: use a provider with tiny effective backoff by
	// shrinking MaxRetries' wait indirectly is not exposed, so we just allow
	// the real (small, capped) backoff to elapse — it is at most a couple of
	// seconds across two retries given defaultRetryBase/Max.
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok after retry", resp.Content)
	assert.Equal(t, int32(3), atomic.LoadInt32(calls))
}

func TestComplete_DoesNotRetryOn400(t *testing.T) {
	p, calls := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
	})
	_, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "hi"}},
	})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(calls), "a 4xx other than 429 must not be retried")
}

func TestHealthy(t *testing.T) {
	p, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, anthropicwire.Response{Content: []anthropicwire.ContentBlock{{Type: "text", Text: "pong"}}})
	})
	assert.True(t, p.Healthy(context.Background()))
}

func TestEmbed_NotImplemented(t *testing.T) {
	p := New(Config{APIKey: "k"}, http.DefaultClient)
	_, err := p.Embed(context.Background(), []string{"x"})
	require.Error(t, err)
}

func TestConfigFromEnv_RequiresAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	_, ok := ConfigFromEnv()
	assert.False(t, ok)

	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	cfg, ok := ConfigFromEnv()
	require.True(t, ok)
	assert.Equal(t, "sk-test", cfg.APIKey)
	assert.Equal(t, defaultModel, cfg.Model)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
