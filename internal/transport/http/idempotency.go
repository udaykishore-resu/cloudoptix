package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// IdempotencyHeader is the header a client sets on a mutating request to
// make retries safe: execution start, plan execution, approval decisions and
// every other action with a real-world side effect can otherwise be
// double-fired by a client retry racing a slow response.
const IdempotencyHeader = "Idempotency-Key"

// idempotencyRecord is one cached response, keyed by (tenant, key, request
// body hash). The body hash is included so a client reusing a key with a
// materially different body — a bug, or an attempt to smuggle a different
// request under an old key's cached success — gets a 422 instead of the
// stale response for the original request.
type idempotencyRecord struct {
	bodyHash   string
	status     int
	body       []byte
	contentTyp string
	storedAt   time.Time
}

// IdempotencyStore caches mutating-endpoint responses by idempotency key.
// The in-process map implementation here is what a single-instance
// deployment uses directly; a multi-instance deployment backs the same
// interface with Redis (ports.Cache is the natural fit — same TTL,
// tenant-namespaced keys — see the store's doc comment for why this
// interface, not ports.Cache directly, is what the middleware depends on).
type IdempotencyStore interface {
	Get(tenant core.TenantID, key string) (idempotencyRecord, bool)
	Put(tenant core.TenantID, key string, rec idempotencyRecord)
}

// MemoryIdempotencyStore is an in-memory IdempotencyStore with a fixed TTL,
// swept lazily on access rather than by a background goroutine — simpler,
// and sufficient because a stale entry only ever causes one extra duplicate
// execution in the (already narrow) window between TTL expiry and the next
// access, not a leak: WithTTL bounds the map's growth on its own since every
// Get opportunistically evicts expired peers it happens to touch is not
// guaranteed, so Put also prunes on a cheap interval.
type MemoryIdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]idempotencyRecord
	ttl     time.Duration
	now     func() time.Time
}

// NewMemoryIdempotencyStore builds a store retaining entries for ttl.
func NewMemoryIdempotencyStore(ttl time.Duration) *MemoryIdempotencyStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &MemoryIdempotencyStore{entries: map[string]idempotencyRecord{}, ttl: ttl, now: time.Now}
}

func idemKey(tenant core.TenantID, key string) string { return tenant.String() + "/" + key }

// Get implements IdempotencyStore.
func (s *MemoryIdempotencyStore) Get(tenant core.TenantID, key string) (idempotencyRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.entries[idemKey(tenant, key)]
	if !ok || s.now().Sub(rec.storedAt) > s.ttl {
		return idempotencyRecord{}, false
	}
	return rec, true
}

// Put implements IdempotencyStore.
func (s *MemoryIdempotencyStore) Put(tenant core.TenantID, key string, rec IdempotencyRecordPublic) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.storedAt = s.now()
	s.entries[idemKey(tenant, key)] = idempotencyRecord(rec)
	s.prune()
}

// prune removes expired entries. Called with mu held.
func (s *MemoryIdempotencyStore) prune() {
	if len(s.entries)%256 != 0 { // amortise the sweep instead of scanning on every write
		return
	}
	cutoff := s.now().Add(-s.ttl)
	for k, v := range s.entries {
		if v.storedAt.Before(cutoff) {
			delete(s.entries, k)
		}
	}
}

// IdempotencyRecordPublic is the exported alias idempotencyRecord's
// unexported type would otherwise force MemoryIdempotencyStore.Put to hide
// from callers outside this file — kept as a distinct name rather than
// exporting idempotencyRecord itself, so the middleware below (the only
// production caller) stays the one place constructing one.
type IdempotencyRecordPublic = idempotencyRecord

// bodyHash renders a stable content hash for idempotency comparison.
func bodyHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Idempotency returns middleware that, for requests carrying an
// Idempotency-Key header, replays a cached response for a repeat of the same
// (tenant, key, body) rather than re-executing the handler — the mechanism
// that makes it safe for a client to retry a POST /executions/{id}/execute
// after a timeout without risking a second AWS mutation.
//
// It only ever caches successful (2xx) responses: caching a transient 5xx
// would turn one bad moment into a permanently stuck idempotency key, and a
// 4xx is a client error the client should be free to correct and retry
// under the same key.
func Idempotency(store IdempotencyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyHeader)
			if key == "" || !isMutating(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			tenant, _ := core.TenantFrom(r.Context())

			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				WriteProblem(w, r, core.Invalid("could not read request body: %s", err.Error()))
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			hash := bodyHash(bodyBytes)

			if rec, ok := store.Get(tenant, key); ok {
				if rec.bodyHash != hash {
					WriteProblem(w, r, core.NewError(core.ErrInvalidInput, "idempotency_key_reused",
						"Idempotency-Key %q was already used for a different request body", key))
					return
				}
				w.Header().Set("Content-Type", rec.contentTyp)
				w.Header().Set("Idempotency-Replayed", "true")
				w.WriteHeader(rec.status)
				_, _ = w.Write(rec.body)
				return
			}

			rec := newCapturingWriter()
			next.ServeHTTP(rec, r)
			rec.copyTo(w)

			if rec.status >= 200 && rec.status < 300 {
				store.Put(tenant, key, idempotencyRecord{
					bodyHash: hash, status: rec.status, body: rec.buf.Bytes(),
					contentTyp: rec.Header().Get("Content-Type"),
				})
			}
		})
	}
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// capturingWriter buffers a handler's response so Idempotency can inspect
// the status before deciding whether to cache it, and then copies the
// buffered bytes to the real ResponseWriter exactly once. It deliberately
// does not implement http.Flusher — an idempotency-keyed endpoint is, by
// definition, a request/response mutation, never one of the two SSE
// surfaces (sse.go), so there is nothing to flush incrementally.
type capturingWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func newCapturingWriter() *capturingWriter {
	return &capturingWriter{header: http.Header{}, status: http.StatusOK}
}

func (c *capturingWriter) Header() http.Header         { return c.header }
func (c *capturingWriter) Write(b []byte) (int, error) { return c.buf.Write(b) }
func (c *capturingWriter) WriteHeader(status int)      { c.status = status }

func (c *capturingWriter) copyTo(w http.ResponseWriter) {
	dst := w.Header()
	for k, v := range c.header {
		dst[k] = v
	}
	w.WriteHeader(c.status)
	_, _ = w.Write(c.buf.Bytes())
}
