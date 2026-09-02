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

func TestRateLimit_ThrottlesAfterBucketExhausted(t *testing.T) {
	inner := newMockProvider()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewRateLimitProvider(inner, RateLimitConfig{
		RequestsPerMinute: 2, DailyTokenQuota: 1_000_000, Clock: func() time.Time { return now },
	})
	req := ports.CompletionRequest{TenantID: "tenant-a"}

	_, err := rl.Complete(context.Background(), req)
	require.NoError(t, err)
	_, err = rl.Complete(context.Background(), req)
	require.NoError(t, err)

	// Bucket of 2 is now empty; a third immediate call is throttled.
	_, err = rl.Complete(context.Background(), req)
	require.Error(t, err)
	require.True(t, errors.Is(err, core.ErrThrottled))
	require.Equal(t, 2, inner.callCount())
}

func TestRateLimit_BucketRefillsOverTime(t *testing.T) {
	inner := newMockProvider()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	rl := NewRateLimitProvider(inner, RateLimitConfig{
		RequestsPerMinute: 60, DailyTokenQuota: 1_000_000, Clock: clock,
	})
	req := ports.CompletionRequest{TenantID: "tenant-a"}

	for i := 0; i < 60; i++ {
		_, err := rl.Complete(context.Background(), req)
		require.NoError(t, err)
	}
	_, err := rl.Complete(context.Background(), req)
	require.Error(t, err, "bucket of 60 exhausted after 60 calls in the same instant")

	// One second later, at 60 rpm (1/sec), exactly one token has refilled.
	now = now.Add(time.Second)
	_, err = rl.Complete(context.Background(), req)
	require.NoError(t, err)

	_, err = rl.Complete(context.Background(), req)
	require.Error(t, err, "only one token refilled after one second")
}

func TestRateLimit_DailyQuotaExceeded(t *testing.T) {
	inner := newMockProvider()
	inner.responses = []ports.CompletionResponse{{InputTokens: 400, OutputTokens: 400}}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewRateLimitProvider(inner, RateLimitConfig{
		RequestsPerMinute: 1000, DailyTokenQuota: 1000, Clock: func() time.Time { return now },
	})
	req := ports.CompletionRequest{TenantID: "tenant-a"}

	_, err := rl.Complete(context.Background(), req) // consumes 800 tokens
	require.NoError(t, err)

	_, err = rl.Complete(context.Background(), req) // 800 >= 1000? no, still under; consumes another 800 -> 1600
	require.NoError(t, err)

	_, err = rl.Complete(context.Background(), req)
	require.Error(t, err)
	require.True(t, errors.Is(err, core.ErrThrottled))
}

func TestRateLimit_QuotaResetsNextUTCDay(t *testing.T) {
	inner := newMockProvider()
	inner.responses = []ports.CompletionResponse{{InputTokens: 600, OutputTokens: 0}}
	now := time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	rl := NewRateLimitProvider(inner, RateLimitConfig{
		RequestsPerMinute: 1000, DailyTokenQuota: 1000, Clock: clock,
	})
	req := ports.CompletionRequest{TenantID: "tenant-a"}

	_, err := rl.Complete(context.Background(), req) // usedDay: 0 -> 600 (0 < 1000, allowed)
	require.NoError(t, err)
	_, err = rl.Complete(context.Background(), req) // usedDay: 600 -> 1200 (600 < 1000, still allowed: soft quota)
	require.NoError(t, err)
	_, err = rl.Complete(context.Background(), req) // usedDay already 1200 >= 1000: rejected
	require.Error(t, err)

	now = now.Add(2 * time.Minute) // crosses into 2026-01-02
	_, err = rl.Complete(context.Background(), req)
	require.NoError(t, err, "quota resets on UTC calendar day rollover")
}

func TestRateLimit_SystemCallsWithoutTenantBypassLimits(t *testing.T) {
	inner := newMockProvider()
	rl := NewRateLimitProvider(inner, RateLimitConfig{RequestsPerMinute: 1, DailyTokenQuota: 1})
	req := ports.CompletionRequest{} // no TenantID

	for i := 0; i < 5; i++ {
		_, err := rl.Complete(context.Background(), req)
		require.NoError(t, err)
	}
	require.Equal(t, 5, inner.callCount())
}

func TestRateLimit_PerTenantOverride(t *testing.T) {
	inner := newMockProvider()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewRateLimitProvider(inner, RateLimitConfig{
		RequestsPerMinute: 1, DailyTokenQuota: 1_000_000,
		Override: func(tenant core.TenantID) (int, int64) {
			if tenant == "vip" {
				return 100, 0
			}
			return 0, 0
		},
		Clock: func() time.Time { return now },
	})

	// "vip" gets a 100-request bucket via Override.
	for i := 0; i < 10; i++ {
		_, err := rl.Complete(context.Background(), ports.CompletionRequest{TenantID: "vip"})
		require.NoError(t, err)
	}

	// A regular tenant is limited to the 1 rpm default.
	_, err := rl.Complete(context.Background(), ports.CompletionRequest{TenantID: "regular"})
	require.NoError(t, err)
	_, err = rl.Complete(context.Background(), ports.CompletionRequest{TenantID: "regular"})
	require.Error(t, err)
}
