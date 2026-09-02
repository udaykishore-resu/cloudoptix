package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeHealth(t *testing.T, rec *httptest.ResponseRecorder) healthResponse {
	t.Helper()
	var resp healthResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestLivenessHandler_HealthyWhenNoChecksConfigured(t *testing.T) {
	h := NewHealth("cloudoptix-api", "test", nil, nil)
	rec := httptest.NewRecorder()
	h.LivenessHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", decodeHealth(t, rec).Status)
}

func TestReadinessHandler_ReportsFailingDependency(t *testing.T) {
	checks := []NamedCheck{
		{Name: "postgres", Check: func(ctx context.Context) error { return nil }},
		{Name: "redis", Check: func(ctx context.Context) error { return errors.New("connection refused") }},
	}
	h := NewHealth("cloudoptix-api", "test", nil, checks)
	rec := httptest.NewRecorder()
	h.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	resp := decodeHealth(t, rec)
	assert.Equal(t, "degraded", resp.Status)
	require.Len(t, resp.Checks, 2)

	var sawFailure bool
	for _, c := range resp.Checks {
		if c.Name == "redis" {
			sawFailure = true
			assert.Equal(t, "error", c.Status)
			assert.Contains(t, c.Error, "connection refused")
		}
	}
	assert.True(t, sawFailure)
}

func TestReadinessHandler_AllHealthy(t *testing.T) {
	checks := []NamedCheck{
		{Name: "postgres", Check: func(ctx context.Context) error { return nil }},
		{Name: "redis", Check: func(ctx context.Context) error { return nil }},
	}
	h := NewHealth("cloudoptix-api", "test", nil, checks)
	rec := httptest.NewRecorder()
	h.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", decodeHealth(t, rec).Status)
}

func TestLivenessHandler_DoesNotRunReadinessChecks(t *testing.T) {
	var readinessCalled bool
	liveness := []NamedCheck{{Name: "goroutines", Check: func(ctx context.Context) error { return nil }}}
	readiness := []NamedCheck{{Name: "database", Check: func(ctx context.Context) error { readinessCalled = true; return nil }}}
	h := NewHealth("cloudoptix-api", "test", liveness, readiness)

	rec := httptest.NewRecorder()
	h.LivenessHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.False(t, readinessCalled, "liveness must never invoke readiness (dependency) checks")
	resp := decodeHealth(t, rec)
	require.Len(t, resp.Checks, 1)
	assert.Equal(t, "goroutines", resp.Checks[0].Name)
}

func TestCheckWithTimeout_ConvertsHangToError(t *testing.T) {
	slow := CheckWithTimeout("slow-dep", 10*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	err := slow.Check(context.Background())
	require.Error(t, err)
}

func TestCheckWithTimeout_FastCheckSucceeds(t *testing.T) {
	fast := CheckWithTimeout("fast-dep", time.Second, func(ctx context.Context) error { return nil })
	require.NoError(t, fast.Check(context.Background()))
}

func TestRunChecks_RunsConcurrently(t *testing.T) {
	const n = 20
	checks := make([]NamedCheck, n)
	for i := 0; i < n; i++ {
		checks[i] = NamedCheck{Name: "c", Check: func(ctx context.Context) error {
			time.Sleep(20 * time.Millisecond)
			return nil
		}}
	}
	start := time.Now()
	_, healthy := runChecks(context.Background(), checks)
	elapsed := time.Since(start)

	assert.True(t, healthy)
	// If these ran serially, 20 checks * 20ms would be at least 400ms.
	// Concurrently they should all finish in well under that.
	assert.Less(t, elapsed, 200*time.Millisecond)
}
