package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestCache_HitAvoidsSecondCall(t *testing.T) {
	inner := newMockProvider()
	inner.responses = []ports.CompletionResponse{{Content: "first"}}
	c := NewCachingProvider(inner, CacheConfig{TTLDuration: time.Minute, MaxEntries: 10})
	req := ports.CompletionRequest{TenantID: "t1", Purpose: "copilot", Messages: []ports.Message{{Role: ports.RoleUser, Content: "hi"}}}

	resp1, err := c.Complete(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "first", resp1.Content)

	resp2, err := c.Complete(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "first", resp2.Content, "cached response returned verbatim")
	require.Equal(t, 1, inner.callCount(), "second identical call must not reach inner")
}

func TestCache_DifferentRequestsAreDifferentKeys(t *testing.T) {
	inner := newMockProvider()
	c := NewCachingProvider(inner, DefaultCacheConfig())

	_, err := c.Complete(context.Background(), ports.CompletionRequest{TenantID: "t1", Messages: []ports.Message{{Content: "a"}}})
	require.NoError(t, err)
	_, err = c.Complete(context.Background(), ports.CompletionRequest{TenantID: "t1", Messages: []ports.Message{{Content: "b"}}})
	require.NoError(t, err)

	require.Equal(t, 2, inner.callCount())
}

func TestCache_NonzeroTemperatureBypassesCache(t *testing.T) {
	inner := newMockProvider()
	c := NewCachingProvider(inner, DefaultCacheConfig())
	req := ports.CompletionRequest{Temperature: 0.7, Messages: []ports.Message{{Content: "hi"}}}

	_, err := c.Complete(context.Background(), req)
	require.NoError(t, err)
	_, err = c.Complete(context.Background(), req)
	require.NoError(t, err)

	require.Equal(t, 2, inner.callCount(), "temperature>0 requests must never be served from cache")
}

func TestCache_EntryExpiresAfterTTL(t *testing.T) {
	inner := newMockProvider()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewCachingProvider(inner, CacheConfig{TTLDuration: time.Minute, MaxEntries: 10, Clock: func() time.Time { return now }})
	req := ports.CompletionRequest{Messages: []ports.Message{{Content: "hi"}}}

	_, err := c.Complete(context.Background(), req)
	require.NoError(t, err)
	_, err = c.Complete(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, inner.callCount())

	now = now.Add(2 * time.Minute)
	_, err = c.Complete(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 2, inner.callCount(), "expired entry must be re-fetched")
}

func TestCache_ErrorResponsesAreNotCached(t *testing.T) {
	inner := newMockProvider()
	inner.errs = []error{context.DeadlineExceeded}
	c := NewCachingProvider(inner, DefaultCacheConfig())
	req := ports.CompletionRequest{Messages: []ports.Message{{Content: "hi"}}}

	_, err := c.Complete(context.Background(), req)
	require.Error(t, err)

	_, err = c.Complete(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 2, inner.callCount(), "a failed call must not poison the cache")
}

func TestCache_EvictsOldestWhenFull(t *testing.T) {
	inner := newMockProvider()
	c := NewCachingProvider(inner, CacheConfig{TTLDuration: time.Hour, MaxEntries: 2})

	req1 := ports.CompletionRequest{Messages: []ports.Message{{Content: "1"}}}
	req2 := ports.CompletionRequest{Messages: []ports.Message{{Content: "2"}}}
	req3 := ports.CompletionRequest{Messages: []ports.Message{{Content: "3"}}}

	_, _ = c.Complete(context.Background(), req1)
	_, _ = c.Complete(context.Background(), req2)
	_, _ = c.Complete(context.Background(), req3) // evicts req1's entry
	require.Equal(t, 3, inner.callCount())

	_, _ = c.Complete(context.Background(), req1)
	require.Equal(t, 4, inner.callCount(), "evicted entry must be recomputed")
}
