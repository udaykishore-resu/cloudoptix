// Package redisstore implements ports.Cache and ports.Locker against Redis.
//
// It exists because the in-memory implementations in internal/adapters/memstore
// satisfy those two interfaces but not their contracts once a second replica
// exists: an in-process lock table gives two API pods two independent locks,
// which is precisely the scenario ports.Locker was introduced to prevent (two
// workers executing the same plan against a customer's account). Correctness
// there is not a caching optimisation — it is the reason the platform can hold
// an execute role at all.
//
// KEY DESIGN DECISION — the lock is released by a compare-and-delete, not a
// bare DEL. A worker whose lease expired mid-work must not be able to delete
// the lock a second worker has since acquired; releasing by token means a
// late release is a no-op instead of a silent hand-off of a lock nobody holds.
// The obvious alternative, DEL on the key, corrupts exactly the case the lock
// exists for.
//
// Values are stored JSON-encoded rather than gob-encoded so that an operator
// debugging a stale entry can read it with redis-cli, and so a cached shape
// that gains a field does not become undecodable to a rolling deployment's
// older replicas — an unknown JSON field is ignored, an unknown gob field is
// an error.
//
// Traceability: REQ-OPS-004, SPEC-ARCH-003, SPEC-OPS-002.
package redisstore
