# ADR-0007: In-memory adapters as a first-class runtime, not a testing convenience

## Status

Accepted, implemented.

## Context

Most platforms with a database and external API dependencies build a "fake" or "mock" version of each purely to make unit tests fast and hermetic — a `FakeCostRepository` that returns canned data, useful for testing a caller's error handling but not much else. CloudOptix additionally needs a way to run a complete, working demo of the whole platform (onboarding through execution and rollback) with no infrastructure at all, for a public demo tenant, for CI, and for local development.

## Decision

Build `internal/adapters/memstore`, `internal/adapters/awssim`, `internal/adapters/llm/deterministic`, and `internal/adapters/events.InProcess` as genuine, non-stub implementations of their respective ports — not mocks. `memstore` is one shared `*Store` (not twenty independent fakes) because several repositories must answer cross-aggregate questions a single map cannot (cost attribution by application requires joining a cost record to the resource it billed), with per-aggregate `sync.RWMutex` locking rather than one global lock, and every value deep-copied in and out via a JSON round-trip so a caller can never mutate committed state through a returned pointer. `awssim` maintains one real, mutable `Estate`: discovery walks real attachment state, cost ingestion bills real hourly rates against a real pricing catalog, metrics collection samples real declared statistical distributions, and executed mutations really, reversibly change what the estate would bill next. `deterministic` independently drives a realistic multi-turn onboarding conversation and answers the copilot's promised questions by reading actual numbers out of real tool results, not a fixed script.

## Consequences

**Positive:**
- `REQ-TEST-002` — the whole platform runs end to end with zero external dependencies — falls out of this decision for free, rather than requiring a separate "demo mode" code path that could drift from the real one.
- Every application-layer test exercises the *actual* application logic against a *real* (if simulated) dependency, catching classes of bug a canned-response mock cannot: a rule reading the wrong percentile, a validator that can't distinguish a real improvement from noise, an attribution algorithm that silently mishandles an edge case a mock would never happen to produce.
- The public demo tenant and the CI test suite are, mechanically, the same code path — a bug visible in the demo is reproducible in CI and vice versa.
- New engineers, and this documentation effort itself, can run and reason about the whole platform with no AWS account, no LLM API key, and no database — dramatically lowering the bar to understanding what the system actually does.

**Negative:**
- A larger engineering investment than a thin mock would have required — `awssim` alone is thousands of lines implementing real cost models, real attachment semantics, and real reversible executors, not a few canned JSON fixtures.
- "Built to match the real ports' contract" and "verified against the real AWS API's actual behaviour" remain two different claims — the real AWS adapters (`internal/adapters/aws/*`) have never been exercised against a live account in this environment, so `awssim`'s fidelity is only as good as its own package doc's claims until that comparison happens. See the root README's [Current limitations](../README.md#current-limitations-and-what-production-hardening-would-still-require).
- The simulator's seeded randomness (`fixture.yaml`'s `seed`) means "deterministic" is a property of one fixed seed, not an argument that the simulator's *behavior* generalizes to arbitrary real-world estates — it generalizes exactly as well as the fixture's own tunable parameters were designed to.

## Alternatives considered

**Thin mocks per port, generated or hand-written, returning canned responses.** Rejected as the *only* testing strategy: it would make the demo tenant a separate, second implementation of "what a working CloudOptix looks like" that could silently drift from the real application logic — the exact risk this decision's package docs call out directly ("a simulator that merely returned plausible-looking data would let a whole class of bugs pass every test and then fail on the first real customer").

**Requiring a real Postgres/Redis/AWS-sandbox-account for all local development and CI.** Rejected: it would make the barrier to running the platform at all — for a new contributor, for CI, for this documentation effort's own verification of the numbers quoted throughout it — far higher, for a benefit (testing against "the real thing") better achieved incrementally by also maintaining the production adapters to the same port contracts, which this codebase does.
