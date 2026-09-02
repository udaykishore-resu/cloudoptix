package memstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// cacheEntry stores a value as its JSON encoding, not as the original Go
// value. That is what makes ports.Cache safe to hand out: if Set kept the
// caller's value (or a pointer to it) and Get returned it back, a caller
// mutating what Get gave it would silently corrupt the cache, and two
// unrelated callers sharing one cached slice would alias each other's edits
// through it — exactly the class of bug tenant isolation and deep-copying
// exist elsewhere in this package to prevent. Round-tripping through JSON on
// every Set and Get costs a little CPU and forecloses that whole bug class.
type cacheEntry struct {
	Data      json.RawMessage
	ExpiresAt time.Time // zero means "never expires"
}

func (e cacheEntry) expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt)
}

type cacheRepo struct{ s *Store }

// cacheKey namespaces every key by tenant, so there is no way to construct a
// key string that reads across the tenant boundary — the namespacing lives
// inside the adapter rather than being a convention callers must remember.
func cacheKey(tenant core.TenantID, key string) string {
	return fmt.Sprintf("%s\x00%s", tenant, key)
}

func (c *cacheRepo) Get(ctx context.Context, tenant core.TenantID, key string, dest any) (bool, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return false, err
	}
	full := cacheKey(tenant, key)

	c.s.cacheMu.RLock()
	entry, ok := c.s.cache[full]
	c.s.cacheMu.RUnlock()
	if !ok {
		return false, nil
	}
	if entry.expired(time.Now().UTC()) {
		c.s.cacheMu.Lock()
		// Re-check under the write lock: another goroutine may have already
		// refreshed this key between the RUnlock above and this Lock.
		if cur, still := c.s.cache[full]; still && cur.expired(time.Now().UTC()) {
			delete(c.s.cache, full)
		}
		c.s.cacheMu.Unlock()
		return false, nil
	}
	if dest == nil {
		return true, nil
	}
	if err := json.Unmarshal(entry.Data, dest); err != nil {
		return false, core.Invalid("cache: stored value for %q does not fit destination type: %v", key, err)
	}
	return true, nil
}

func (c *cacheRepo) Set(ctx context.Context, tenant core.TenantID, key string, value any, ttl time.Duration) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return core.Invalid("cache: value for %q is not JSON-serialisable: %v", key, err)
	}
	entry := cacheEntry{Data: raw}
	if ttl > 0 {
		entry.ExpiresAt = time.Now().UTC().Add(ttl)
	}
	full := cacheKey(tenant, key)

	c.s.cacheMu.Lock()
	c.s.cache[full] = entry
	c.s.cacheMu.Unlock()
	return nil
}

func (c *cacheRepo) Delete(ctx context.Context, tenant core.TenantID, key string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	full := cacheKey(tenant, key)
	c.s.cacheMu.Lock()
	delete(c.s.cache, full)
	c.s.cacheMu.Unlock()
	return nil
}

func (c *cacheRepo) InvalidatePrefix(ctx context.Context, tenant core.TenantID, prefix string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	full := cacheKey(tenant, prefix)
	c.s.cacheMu.Lock()
	for k := range c.s.cache {
		if len(k) >= len(full) && k[:len(full)] == full {
			delete(c.s.cache, k)
		}
	}
	c.s.cacheMu.Unlock()
	return nil
}
