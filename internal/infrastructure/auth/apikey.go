package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// APIKeyPrincipal is what a service API key resolves to: a principal plus
// the metadata a client would want back for its own bookkeeping (a key
// label, so "which integration is this" shows up in the audit log's actor
// field instead of an opaque hash).
type APIKeyPrincipal struct {
	Principal core.Principal
	Label     string
}

// APIKeyStore resolves a presented API key to the principal it authorizes.
// Implementations hash the key before lookup (see HashAPIKey) — the store
// never holds plaintext keys, only their hashes, matching how a leaked
// database dump must not itself be a usable credential set.
type APIKeyStore interface {
	Lookup(ctx context.Context, keyHash string) (APIKeyPrincipal, bool, error)
}

// HashAPIKey renders the SHA-256 hex digest of an API key. Callers issuing a
// new key store HashAPIKey(key) and return key to the caller exactly once;
// CloudOptix itself never persists or logs the plaintext value again.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// StaticAPIKeyStore is an in-memory APIKeyStore, for tests, the demo tenant,
// and any deployment that provisions a small fixed set of service keys
// through configuration rather than a database table.
type StaticAPIKeyStore struct {
	keys map[string]APIKeyPrincipal // keyed by hash
}

// NewStaticAPIKeyStore builds a store from a set of plaintext keys. It
// hashes each one on construction; from this point on, only the hash is
// retained in memory.
func NewStaticAPIKeyStore(entries map[string]APIKeyPrincipal) *StaticAPIKeyStore {
	hashed := make(map[string]APIKeyPrincipal, len(entries))
	for plaintext, p := range entries {
		hashed[HashAPIKey(plaintext)] = p
	}
	return &StaticAPIKeyStore{keys: hashed}
}

// Lookup implements APIKeyStore.
func (s *StaticAPIKeyStore) Lookup(ctx context.Context, keyHash string) (APIKeyPrincipal, bool, error) {
	for h, p := range s.keys {
		// subtle.ConstantTimeCompare over the map's own hash keeps the
		// lookup itself free of a timing channel that could otherwise leak
		// how many leading hex characters of a guessed key were correct;
		// the map index above is still an O(1) hash-table probe, this loop
		// only runs for the (small, operator-provisioned) static key set.
		if len(h) == len(keyHash) && subtle.ConstantTimeCompare([]byte(h), []byte(keyHash)) == 1 {
			return p, true, nil
		}
	}
	return APIKeyPrincipal{}, false, nil
}

// ValidateAPIKey resolves a presented plaintext key via store, stamping a
// fresh IssuedAt/ExpiresAt window (service API keys do not carry their own
// expiry the way a JWT does — the store's revocation is what ends a key's
// life, not a client-visible timestamp).
func ValidateAPIKey(ctx context.Context, store APIKeyStore, plaintext string, ttl time.Duration) (core.Principal, error) {
	entry, ok, err := store.Lookup(ctx, HashAPIKey(plaintext))
	if err != nil {
		return core.Principal{}, core.NewError(core.ErrUnavailable, "api_key_lookup_failed", "could not validate API key").Wrap(err)
	}
	if !ok {
		return core.Principal{}, core.NewError(core.ErrUnauthenticated, "invalid_api_key", "API key not recognised")
	}
	p := entry.Principal
	now := time.Now().UTC()
	p.IssuedAt = now
	if ttl > 0 {
		p.ExpiresAt = now.Add(ttl)
	}
	return p, nil
}
