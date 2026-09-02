package notify

import (
	"sync"
	"time"
)

// alertDedup is a bounded, TTL-based "have I already dispatched this exact
// alert recently" set, the same shape and reasoning as
// internal/adapters/events/dedup.go's dedupCache — deliberately not shared
// code between the two packages (each is small enough that the
// duplication costs less than a cross-adapter-package dependency would).
// It exists to protect a human's attention from a noisy, repeating
// condition (a metric flapping above and below a threshold every few
// minutes), not to promise exactly-once delivery: it is in-memory and
// per-process, so it is silently reset by a restart, which is the correct
// failure mode for "don't page someone five times for one incident", not
// something that needs durability.
type alertDedup struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func newAlertDedup() *alertDedup {
	return &alertDedup{entries: make(map[string]time.Time)}
}

// seen reports whether key was already recorded within window (and refreshes
// its timestamp either way, so a steadily repeating alert stays suppressed
// for the full window after its most recent occurrence rather than aging
// out mid-incident).
func (d *alertDedup) seen(key string, window time.Duration, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	for k, at := range d.entries {
		if now.Sub(at) > window {
			delete(d.entries, k)
		}
	}

	at, ok := d.entries[key]
	d.entries[key] = now
	return ok && now.Sub(at) <= window
}
