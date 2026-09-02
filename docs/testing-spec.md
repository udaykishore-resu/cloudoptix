# Testing specification

Covers `SPEC-DEMO-001` and `REQ-TEST-001..002`.

## SPEC-DEMO-001 — Demo tenant / simulator fidelity

`internal/adapters/awssim` is a deterministic, in-repo simulation of an AWS account — not a stub. The design decision it turns on: the demo tenant, the entire integration test suite, and CI all run against this package instead of a real AWS account, which is only trustworthy if `awssim` behaves like the ports it implements in every way that matters to the engines built on top of it — a discovered resource must cost what the cost ingestor bills for it, a metric profile declared "spiky" must actually produce a spiky series, and an executed mutation must actually change what the estate bills next. A simulator that merely returned plausible-looking data would let a whole class of bugs (a rule reading the wrong percentile, a validator that can't tell a real improvement from noise) pass every test and then fail on the first real customer. Every adapter in the package reads and writes the same in-memory `Estate`: `Discover` walks real attachment state, `Fetch` bills real hourly rates, `Collect` samples real declared distributions, and `Apply` performs a real, reversible mutation of the estate.

**Enforced by test, not merely claimed:**

- `demo_test.go`'s `TestBuildDemoEstate_TotalWithinTargetRange` asserts total monthly cost lands in `[$180K, $190K]` — verified by running it: **$185,978.41**.
- `TestBuildDemoEstate_IdentifiableWasteWithinTargetRange` asserts identifiable waste lands in `[$40K, $50K]`, with every one of thirteen waste categories individually asserted nonzero (a zeroed category would mean part of the demo story silently isn't costing anything, invisible to a rule engine) — verified: **$44,206.84**, with every category populated (largest: EKS packing waste $20,034.12, NAT-without-endpoint $12,348.00; smallest: unattached EIPs $32.85).
- A determinism test builds the estate twice from the same seed and asserts identical `TotalMonthlyCost` — the demo tenant, and therefore every test and CI run built on it, is byte-reproducible.

## REQ-TEST-002 — The platform runs end to end with no external dependency

`internal/adapters/memstore` (every repository port) + `internal/adapters/awssim` (a full simulated AWS account) + `internal/adapters/llm/deterministic` (no API key) + `internal/adapters/events.InProcess` (a real, non-mocked event bus) together are sufficient to exercise every application service, with no Postgres, Redis, or AWS account reachable. This is a first-class runtime target — see [ADR-0007](adr/0007-in-memory-adapters-as-first-class-runtime.md) — not merely a testing convenience that happens to also work.

## REQ-TEST-001 — Deterministic reproducibility across the AI-dependent surface

Every AI-dependent code path — onboarding's stage machine, extraction, inference; the copilot's tool selection, grounding verification, degraded mode — runs against the deterministic LLM provider in CI, with no API key, and produces the same output on the same input every time. This is what makes those paths testable at all without a live model in the loop.

## Test inventory (153 `_test.go` files, by area)

| Area | Files | Notable coverage |
|---|---:|---|
| `internal/domain/*` | 1 | Only `core/money_yaml_test.go` — the other ten domain packages have **no dedicated unit test file** (see below) |
| `internal/application/*` | 45 | One or more test files per engine; `optimization` has the most granular per-rule coverage (5 of 48 rules individually tested) |
| `internal/adapters/aws/*` | 34 | Per-service discoverer tests (19), per-action executor tests (6), costing/metrics/sts |
| `internal/adapters/{awssim,memstore,events,llm,notify,postgres,pricing,rag}` | 55 | Broadest adapter-level coverage in the repository |
| `internal/infrastructure/*` | 11 | auth (5), config, resilience (2), server (2), telemetry (2) |
| `internal/transport/http` | 6 | Routing, RBAC exhaustiveness, auth, pagination, problem format, SSE, tenant isolation |

## The domain-layer test gap

`find internal/domain -name '*_test.go'` returns exactly one file: `internal/domain/core/money_yaml_test.go`. `internal/domain/{cloud, cost, econ, optimize, simulate, govern, execute, spec, tenancy, audit}` — the pure, dependency-free, deterministic-by-design layer this whole platform's auditability argument rests on — have **no test file of their own**. Every one of these packages is exercised, sometimes thoroughly, through the application-layer and adapter test suites that call them (see [`docs/traceability.md`](traceability.md) Table D for exactly which application tests cover which domain package). That is a real form of coverage, but it means a defect isolated to pure domain logic is only caught if an application-layer test happens to route through that exact path — `govern.Evaluate`'s rule-fold correctness (the exact class of bug `policies/README.md` mistakenly still claims exists — see [`automation-spec.md`](automation-spec.md)) is a concrete example of the kind of thing a direct `govern_test.go` would catch faster and more precisely than an indirect `governance/evaluate_test.go` pass.

**Recommendation, not yet acted on in this codebase:** add a direct test file for each of the ten untested domain packages, prioritized by the ones with the highest audit/safety stakes — `govern` (policy evaluation correctness), `execute` (savings-ladder and rollback-plan invariants), `audit` (hash-chain verification), `spec` (validation and diffing).

## What has never been tested

- **Any full stack, running as a process.** No `cmd/` entrypoint exists (see the root README's [Current limitations](../README.md#current-limitations-and-what-production-hardening-would-still-require)), so there is no such thing as an end-to-end HTTP-request-in, AWS-mutation-out integration test in this codebase — every test exercises a service or handler directly in-process.
- **Against a real AWS account.** Every AWS-adjacent test uses mocked/recorded SDK responses or `awssim`.
- **At scale.** The demo estate is ~500 resources. `tenancy.QuotasFor(PlanEnterprise)` declares an intended ceiling of 500,000 resources; nothing has verified behaviour anywhere near that.
- **Concurrency/load.** No load test, no chaos test, no multi-tenant concurrent-execution test exists.
- **A real LLM provider**, live. `anthropic`/`bedrock` are unit-tested against mocked wire formats only.
- **The frontend.** It is an unmodified `create-next-app` scaffold with no tests, because no UI has been built against this API.

## CI-shaped guarantees the test suite already provides

- `routes_test.go`/`rbac_test.go`: every mutating HTTP route names a real, non-empty permission — a property that would otherwise only be discoverable by reading every handler by hand.
- `demo_test.go`: the demo tenant's spend and waste envelope, and its determinism across rebuilds.
- `money_yaml_test.go`: `core.Money` round-trips through YAML at several input formats (`5000`, `5000.50`, `'$5,000'`, `USD 5000`, `'5000 USD'`) without precision loss, and rejects an unparseable amount rather than silently zeroing it.
- Provider-parity by construction: onboarding and copilot tests run against the deterministic provider, which shares the exact interpreter/schema path a real provider would use — so a passing test is evidence about the *interpreter*, though not about the real provider's own output quality.
