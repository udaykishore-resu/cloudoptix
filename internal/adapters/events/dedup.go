package events

import (
	"sync"
	"time"
)

// dedupCache is a bounded, TTL-based "have I seen this idempotency key
// recently" set. It is intentionally simple: an in-memory map guarded by a
// mutex, with expired entries swept out lazily on each lookup rather than
// by a background goroutine. It is explicitly NOT a substitute for handler
// idempotency — see SQSSubscriber.DedupWindow's doc comment — only a
// best-effort layer that avoids re-invoking a handler for a redelivery this
// same process already saw, which is the common case (a visibility timeout
// that was slightly too short, not a process restart).
type dedupCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func newDedupCache() *dedupCache {
	return &dedupCache{entries: make(map[string]time.Time)}
}

// seen reports whether key was already recorded within window, recording it
// (with the current time) if not. The sweep of expired entries happens
// inline so the cache cannot grow without bound across a long-running
// process even though nothing calls it on a timer.
func (c *dedupCache) seen(key string, window time.Duration) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) > 0 {
		for k, at := range c.entries {
			if now.Sub(at) > window {
				delete(c.entries, k)
			}
		}
	}

	if at, ok := c.entries[key]; ok && now.Sub(at) <= window {
		return true
	}
	c.entries[key] = now
	return false
}
