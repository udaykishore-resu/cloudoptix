package bedrock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
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
		Region:      "us-east-1",
		ModelID:     "anthropic.claude-3-5-sonnet-test",
		EmbedModel:  "amazon.titan-embed-text-v1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIATEST", "secretkey1234567890", ""),
		Timeout:     5 * time.Second,
		MaxRetries:  3,
		Endpoint:    srv.URL,
	}, srv.Client())
	return p, &calls
}

func TestComplete_SignsRequestWithSigV4(t *testing.T) {
	p, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		require.NotEmpty(t, auth)
		assert.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256 "))
		assert.Contains(t, auth, "Credential=AKIATEST/")
		assert.Contains(t, auth, "/us-east-1/bedrock/aws4_request")
		require.NotEmpty(t, r.Header.Get("X-Amz-Date"))
		require.Equal(t, "/model/anthropic.claude-3-5-sonnet-test/invoke", r.URL.Path)

		var req anthropicwire.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "bedrock-2023-05-31", req.AnthropicVersion)
		assert.Empty(t, req.Model, "bedrock names the model in the URL, not the body")

		writeJSON(w, http.StatusOK, anthropicwire.Response{
			Content: []anthropicwire.ContentBlock{{Type: "text", Text: "signed ok"}},
			Usage:   anthropicwire.Usage{InputTokens: 4, OutputTokens: 2},
		})
	})
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "signed ok", resp.Content)
}

func TestComplete_RetriesOn500ThenSucceeds(t *testing.T) {
	n := 0
	p, calls := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"internal error"}`))
			return
		}
		writeJSON(w, http.StatusOK, anthropicwire.Response{
			Content: []anthropicwire.ContentBlock{{Type: "text", Text: "recovered"}},
		})
	})
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "recovered", resp.Content)
	assert.Equal(t, int32(2), atomic.LoadInt32(calls))
}

func TestEmbed_CallsTitanPerText(t *testing.T) {
	var seen []string
	p, calls := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var req titanEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		seen = append(seen, req.InputText)
		writeJSON(w, http.StatusOK, titanEmbedResponse{Embedding: []float32{0.1, 0.2, 0.3}})
	})
	vecs, err := p.Embed(context.Background(), []string{"first", "second"})
	require.NoError(t, err)
	require.Len(t, vecs, 2)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, vecs[0])
	assert.Equal(t, int32(2), atomic.LoadInt32(calls))
	assert.Equal(t, []string{"first", "second"}, seen)
}

func TestHealthy(t *testing.T) {
	p, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, anthropicwire.Response{Content: []anthropicwire.ContentBlock{{Type: "text", Text: "pong"}}})
	})
	assert.True(t, p.Healthy(context.Background()))
}

func TestConfigFromEnv_RequiresAccessKey(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	_, ok := ConfigFromEnv()
	assert.False(t, ok)

	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAABC")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "eu-west-1")
	cfg, ok := ConfigFromEnv()
	require.True(t, ok)
	assert.Equal(t, "eu-west-1", cfg.Region)
	require.NotNil(t, cfg.Credentials)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
