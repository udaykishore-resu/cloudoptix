package fallback

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/deterministic"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// stubProvider is a tiny controllable ports.LLMProvider for exercising
// Fallback's degrade decisions without any network dependency.
type stubProvider struct {
	name    string
	healthy bool
	err     error
	resp    ports.CompletionResponse
	calls   int32
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Complete(_ context.Context, _ ports.CompletionRequest) (ports.CompletionResponse, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.err != nil {
		return ports.CompletionResponse{}, s.err
	}
	return s.resp, nil
}
func (s *stubProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	return out, nil
}
func (s *stubProvider) Healthy(_ context.Context) bool { return s.healthy }

func TestFallback_UsesPrimaryWhenHealthy(t *testing.T) {
	primary := &stubProvider{name: "anthropic", healthy: true, resp: ports.CompletionResponse{Content: "from primary"}}
	f := New(primary, deterministic.New(), nil)

	resp, err := f.Complete(context.Background(), ports.CompletionRequest{Messages: []ports.Message{{Role: ports.RoleUser, Content: "hi"}}})
	require.NoError(t, err)
	require.Equal(t, "from primary", resp.Content)
	require.EqualValues(t, 1, primary.calls)
}

func TestFallback_DegradesWhenPrimaryUnhealthy(t *testing.T) {
	primary := &stubProvider{name: "anthropic", healthy: false}
	f := New(primary, nil, nil)

	req := ports.CompletionRequest{
		Purpose:  "copilot",
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "what is my most expensive service"}},
	}
	resp, err := f.Complete(context.Background(), req)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Content)
	require.EqualValues(t, 0, primary.calls, "an unhealthy primary must not be called at all")
}

func TestFallback_DegradesWhenPrimaryCallFails(t *testing.T) {
	primary := &stubProvider{name: "anthropic", healthy: true, err: errors.New("timeout")}
	f := New(primary, nil, nil)

	resp, err := f.Complete(context.Background(), ports.CompletionRequest{Messages: []ports.Message{{Role: ports.RoleUser, Content: "hello"}}})
	require.NoError(t, err, "a primary failure must be masked by the deterministic fallback")
	require.NotEmpty(t, resp.Content)
	require.EqualValues(t, 1, primary.calls)
}

func TestFallback_EmbedDegrades(t *testing.T) {
	primary := &stubProvider{name: "anthropic", healthy: false}
	f := New(primary, nil, nil)

	vecs, err := f.Embed(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, vecs, 2)
}

func TestFallback_HealthyIfEitherProviderIsHealthy(t *testing.T) {
	primary := &stubProvider{name: "anthropic", healthy: false}
	f := New(primary, nil, nil)
	require.True(t, f.Healthy(context.Background()), "deterministic provider is always healthy")
}

func TestFallback_NameReportsPrimary(t *testing.T) {
	primary := &stubProvider{name: "anthropic", healthy: true}
	f := New(primary, nil, nil)
	require.Equal(t, "anthropic", f.Name())
}
