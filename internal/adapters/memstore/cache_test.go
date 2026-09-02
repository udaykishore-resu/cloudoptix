package memstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestCache_SetGetDelete(t *testing.T) {
	s := New()
	c := s.Cache()
	ctx := ctxFor(tenantA)

	require.NoError(t, c.Set(ctx, tenantA, "greeting", "hello", time.Minute))

	var got string
	ok, err := c.Get(ctx, tenantA, "greeting", &got)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "hello", got)

	require.NoError(t, c.Delete(ctx, tenantA, "greeting"))
	ok, err = c.Get(ctx, tenantA, "greeting", &got)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCache_TTLExpiry(t *testing.T) {
	s := New()
	c := s.Cache()
	ctx := ctxFor(tenantA)

	require.NoError(t, c.Set(ctx, tenantA, "short-lived", 42, 10*time.Millisecond))

	var got int
	ok, err := c.Get(ctx, tenantA, "short-lived", &got)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 42, got)

	time.Sleep(25 * time.Millisecond)

	ok, err = c.Get(ctx, tenantA, "short-lived", &got)
	require.NoError(t, err)
	assert.False(t, ok, "entry must be treated as absent once its TTL has elapsed")
}

func TestCache_ZeroTTLNeverExpires(t *testing.T) {
	s := New()
	c := s.Cache()
	ctx := ctxFor(tenantA)

	require.NoError(t, c.Set(ctx, tenantA, "forever", "value", 0))
	time.Sleep(5 * time.Millisecond)

	var got string
	ok, err := c.Get(ctx, tenantA, "forever", &got)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "value", got)
}

func TestCache_ValueIsSerializedNotAliased(t *testing.T) {
	s := New()
	c := s.Cache()
	ctx := ctxFor(tenantA)

	original := []string{"a", "b"}
	require.NoError(t, c.Set(ctx, tenantA, "list", original, time.Minute))
	original[0] = "mutated-after-set"

	var got []string
	ok, err := c.Get(ctx, tenantA, "list", &got)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, got, "Set must not retain a live reference to the caller's value")
}

func TestCache_TenantIsolation(t *testing.T) {
	s := New()
	c := s.Cache()

	require.NoError(t, c.Set(ctxFor(tenantA), tenantA, "key", "tenant-a-value", time.Minute))

	var got string
	ok, err := c.Get(ctxFor(tenantB), tenantB, "key", &got)
	require.NoError(t, err)
	assert.False(t, ok, "tenant B must not see a cache entry set under the same key by tenant A")

	_, err = c.Get(ctxFor(tenantB), tenantA, "key", &got)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTenantMismatch)
}

func TestCache_InvalidatePrefix(t *testing.T) {
	s := New()
	c := s.Cache()
	ctx := ctxFor(tenantA)

	require.NoError(t, c.Set(ctx, tenantA, "dashboard:cost", "a", time.Minute))
	require.NoError(t, c.Set(ctx, tenantA, "dashboard:recs", "b", time.Minute))
	require.NoError(t, c.Set(ctx, tenantA, "other:thing", "c", time.Minute))

	require.NoError(t, c.InvalidatePrefix(ctx, tenantA, "dashboard:"))

	var got string
	ok, _ := c.Get(ctx, tenantA, "dashboard:cost", &got)
	assert.False(t, ok)
	ok, _ = c.Get(ctx, tenantA, "dashboard:recs", &got)
	assert.False(t, ok)
	ok, _ = c.Get(ctx, tenantA, "other:thing", &got)
	assert.True(t, ok)
}

func TestLocker_AcquireConflictsAndReleaseFrees(t *testing.T) {
	s := New()
	l := s.Locker()
	ctx := ctxFor(tenantA)

	release, err := l.Acquire(ctx, "discovery:acct-1", time.Minute)
	require.NoError(t, err)

	_, err = l.Acquire(ctx, "discovery:acct-1", time.Minute)
	require.Error(t, err, "a second acquire of the same key must fail while the first is held")

	release()

	release2, err := l.Acquire(ctx, "discovery:acct-1", time.Minute)
	require.NoError(t, err, "releasing must free the key for the next acquirer")
	release2()
}

func TestLocker_TTLExpiryLetsAnotherWorkerAcquire(t *testing.T) {
	s := New()
	l := s.Locker()
	ctx := ctxFor(tenantA)

	_, err := l.Acquire(ctx, "discovery:acct-2", 10*time.Millisecond)
	require.NoError(t, err)

	time.Sleep(25 * time.Millisecond)

	release2, err := l.Acquire(ctx, "discovery:acct-2", time.Minute)
	require.NoError(t, err, "an expired lock must be acquirable by another worker")
	release2()
}

func TestLocker_ExpiredThenReacquiredLockSurvivesStaleRelease(t *testing.T) {
	s := New()
	l := s.Locker()
	ctx := ctxFor(tenantA)

	staleRelease, err := l.Acquire(ctx, "discovery:acct-3", 10*time.Millisecond)
	require.NoError(t, err)
	time.Sleep(25 * time.Millisecond)

	_, err = l.Acquire(ctx, "discovery:acct-3", time.Minute)
	require.NoError(t, err)

	// The original holder's release function must be a no-op now: it no
	// longer owns the lock, and calling it must not free the new holder's
	// lock out from under them.
	staleRelease()

	_, err = l.Acquire(ctx, "discovery:acct-3", time.Minute)
	require.Error(t, err, "the new holder's lock must still be held after the stale release call")
}
