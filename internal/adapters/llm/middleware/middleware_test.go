package middleware

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// mockProvider is a minimal, fully controllable ports.LLMProvider used across
// this package's tests. It counts calls and lets a test script canned
// responses or a scripted error per call, so each middleware layer's
// decision logic (throttle, trip, cache, sanitize) can be tested without any
// network dependency.
type mockProvider struct {
	name string

	mu        sync.Mutex
	calls     int32
	lastReq   ports.CompletionRequest
	responses []ports.CompletionResponse
	errs      []error
	healthy   bool
}

var _ ports.LLMProvider = (*mockProvider)(nil)

func newMockProvider() *mockProvider {
	return &mockProvider{name: "mock", healthy: true}
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Complete(_ context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	idx := int(atomic.AddInt32(&m.calls, 1)) - 1

	m.mu.Lock()
	m.lastReq = req
	var (
		resp ports.CompletionResponse
		err  error
	)
	if idx < len(m.errs) && m.errs[idx] != nil {
		err = m.errs[idx]
	} else if idx < len(m.responses) {
		resp = m.responses[idx]
	} else if len(m.responses) > 0 {
		resp = m.responses[len(m.responses)-1]
	} else {
		resp = ports.CompletionResponse{Content: "ok", InputTokens: 10, OutputTokens: 5}
	}
	m.mu.Unlock()
	return resp, err
}

func (m *mockProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	atomic.AddInt32(&m.calls, 1)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func (m *mockProvider) Healthy(_ context.Context) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthy
}

func (m *mockProvider) callCount() int {
	return int(atomic.LoadInt32(&m.calls))
}

func (m *mockProvider) setHealthy(v bool) {
	m.mu.Lock()
	m.healthy = v
	m.mu.Unlock()
}
