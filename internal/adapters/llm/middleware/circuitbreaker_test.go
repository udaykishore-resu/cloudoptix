package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestCircuitBreaker_TripsAfterThresholdFailures(t *testing.T) {
	inner := newMockProvider()
	inner.errs = []error{errors.New("boom"), errors.New("boom"), errors.New("boom")}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cb := NewCircuitBreakerProvider(inner, CircuitBreakerConfig{
		FailureThreshold: 3, OpenDuration: 30 * time.Second, Clock: func() time.Time { return now },
	})

	for i := 0; i < 3; i++ {
		_, err := cb.Complete(context.Background(), ports.CompletionRequest{})
		require.Error(t, err)
	}
	require.Equal(t, 3, inner.callCount())

	// Breaker is now open: the fourth call must fail fast, without touching inner.
	_, err := cb.Complete(context.Background(), ports.CompletionRequest{})
	require.Error(t, err)
	require.True(t, errors.Is(err, core.ErrUnavailable))
	require.Equal(t, 3, inner.callCount(), "open breaker must not call inner")
}

func TestCircuitBreaker_HalfOpenAllowsSingleTrial(t *testing.T) {
	inner := newMockProvider()
	inner.errs = []error{errors.New("boom"), errors.New("boom")}
	inner.responses = []ports.CompletionResponse{{}, {}, {Content: "recovered"}}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cb := NewCircuitBreakerProvider(inner, CircuitBreakerConfig{
		FailureThreshold: 2, OpenDuration: 10 * time.Second, Clock: clock,
	})

	_, _ = cb.Complete(context.Background(), ports.CompletionRequest{})
	_, _ = cb.Complete(context.Background(), ports.CompletionRequest{})
	require.Equal(t, 2, inner.callCount())

	// Still within OpenDuration: rejected without calling inner.
	_, err := cb.Complete(context.Background(), ports.CompletionRequest{})
	require.Error(t, err)
	require.Equal(t, 2, inner.callCount())

	// Advance past OpenDuration: exactly one trial call should reach inner and succeed.
	now = now.Add(11 * time.Second)
	resp, err := cb.Complete(context.Background(), ports.CompletionRequest{})
	require.NoError(t, err)
	require.Equal(t, "recovered", resp.Content)
	require.Equal(t, 3, inner.callCount())

	// Breaker closed again: subsequent calls pass straight through.
	_, err = cb.Complete(context.Background(), ports.CompletionRequest{})
	require.NoError(t, err)
	require.Equal(t, 4, inner.callCount())
}

func TestCircuitBreaker_HealthyReflectsOpenState(t *testing.T) {
	inner := newMockProvider()
	inner.errs = []error{errors.New("boom")}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cb := NewCircuitBreakerProvider(inner, CircuitBreakerConfig{
		FailureThreshold: 1, OpenDuration: time.Minute, Clock: func() time.Time { return now },
	})

	require.True(t, cb.Healthy(context.Background()))
	_, _ = cb.Complete(context.Background(), ports.CompletionRequest{})
	require.False(t, cb.Healthy(context.Background()), "open breaker reports unhealthy without asking inner")
}
