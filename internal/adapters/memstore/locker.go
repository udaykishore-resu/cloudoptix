package memstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// lockState is one held (or recently expired) distributed lock.
//
// token exists so that Release cannot clobber a lock it no longer owns: if
// holder A's TTL expires, holder B acquires the same key, and A's caller then
// invokes the release function it was handed, that call must be a no-op —
// otherwise A would release a lock it no longer holds out from under B, and
// two workers would believe they both hold exclusive access to the same
// resource, which is exactly the failure Locker exists to prevent.
type lockState struct {
	token     string
	expiresAt time.Time
}

func (l *lockState) expired(now time.Time) bool { return !now.Before(l.expiresAt) }

type lockerRepo struct{ s *Store }

// Acquire implements ports.Locker.
func (l *lockerRepo) Acquire(ctx context.Context, key string, ttl time.Duration) (func(), error) {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	token := newLockToken()
	now := time.Now().UTC()

	l.s.lockMu.Lock()
	if existing, held := l.s.locks[key]; held && !existing.expired(now) {
		l.s.lockMu.Unlock()
		return nil, core.NewError(core.ErrConflict, "lock_held", "lock %q is held by another worker", key)
	}
	state := &lockState{token: token, expiresAt: now.Add(ttl)}
	l.s.locks[key] = state
	l.s.lockMu.Unlock()

	release := func() {
		l.s.lockMu.Lock()
		if cur, ok := l.s.locks[key]; ok && cur.token == token {
			delete(l.s.locks, key)
		}
		l.s.lockMu.Unlock()
	}
	return release, nil
}

func newLockToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Falling back to the time is safe here: a token collision only risks
		// a release racing a fresh acquire within the same nanosecond, and
		// crypto/rand failing at all indicates a broken host environment the
		// lock itself cannot fix.
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return hex.EncodeToString(b[:])
}
