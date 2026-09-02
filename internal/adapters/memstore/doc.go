// Package memstore is the in-memory implementation of every repository port
// in internal/ports: the persistence layer the demo tenant and the whole test
// suite run on when no Postgres, Redis or AWS is reachable.
//
// The key design decision is that this is one Store, not twenty independent
// fakes. Every repository interface is implemented by a small struct that
// holds a pointer back to a single *Store, because several repositories must
// answer questions no single aggregate can answer alone — cost attribution by
// application requires joining a cost.Record to the resource it billed, and a
// resource lookup requires knowing which application claimed it. A real
// database answers that with a join; a collection of independent maps behind
// separate interfaces cannot, so the store is shared and each aggregate gets
// its own sync.RWMutex rather than the whole store sharing one lock. That
// keeps unrelated operations (claiming an execution plan, listing resources)
// from serialising behind each other while still letting cross-aggregate
// reads take a second lock when they need one. Lock acquisition is always
// ordered from the "owning" aggregate outward (e.g. cost read-locks resources,
// never the reverse), which is what keeps that safe from deadlock.
//
// Every stored value is deep-copied on the way in and the way out. Handing a
// caller a pointer into the store's own maps would let it mutate committed
// state without going through a repository method — a bug that is invisible
// here but would be a silent data-race or a phantom write against Postgres.
// The copy is done by round-tripping the value through encoding/json (see
// clone.go); every domain type already carries json tags for the API layer,
// so this needs no hand-written copy function per struct and can never miss a
// field a struct grows later the way a maintained-by-hand clone would.
//
// Traceability: SPEC-ARCH-003 (hexagonal ports), SPEC-SEC-003 (tenant
// isolation), REQ-TEST-002 (the platform runs end to end with no external
// dependency).
package memstore
