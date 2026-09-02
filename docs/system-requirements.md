# System requirements

This document covers the non-functional and platform-level requirements that sit above any single feature area — the constraints [`requirements.md`](requirements.md)'s functional requirements are built inside. Where a specific NFR is already a numbered `REQ-*` (mostly the `REQ-OPS-*` and `REQ-SEC-*` prefixes), this document explains the reasoning; `requirements.md` remains the source of the acceptance criteria.

## Runtime environment

- **Language and module.** Go 1.24 (`go.mod`), module `github.com/udaykishore-resu/cloudoptix`.
- **Deployment shape.** A modular monolith exposing one HTTP API, plus a small number of background workers consuming the same event bus — see [`architecture.md`](architecture.md) and [ADR-0001](adr/0001-modular-monolith.md) for why this was chosen over microservices.
- **No compiled entrypoint exists yet.** There is no `cmd/` directory or `main.go` anywhere in this repository. This is the platform's single largest gap between "implemented" and "running" — see the root README's [Current limitations](../README.md#current-limitations-and-what-production-hardening-would-still-require).

## Data stores

| Store | Interface | Reference implementation | Production implementation |
|---|---|---|---|
| Relational (specs, tenants, resources, cost, audit, ...) | `ports.*Repository` (`internal/ports/repositories.go`) | `internal/adapters/memstore` (one `*Store`, one `sync.RWMutex` per aggregate) | `internal/adapters/postgres`, behind 13 migration pairs in `migrations/` |
| Vector/lexical knowledge index | `ports.KnowledgeStore` | `internal/adapters/rag` (in-process, hybrid search) | Same package — it is the reference and only implementation; no external vector database is used |
| Cache / rate limits / locks | `ports.Cache`, `ports.Locker` | in-memory (bundled with `memstore`) | Redis (declared in `internal/infrastructure/config.RedisConfig`, not independently verified here) |

`REQ-TEST-002` (in-memory adapters + simulated AWS + deterministic LLM + in-process events are together sufficient to run the whole platform with no external dependency) is a first-class runtime target, not a testing convenience that happens to also work — see [ADR-0007](adr/0007-in-memory-adapters-as-first-class-runtime.md).

## Configuration

`internal/infrastructure/config` layers `defaults → config.yaml → environment variables → command-line flags` (`REQ-OPS-002`), matching how the same container image is expected to be promoted through dev/staging/prod with only its environment changed. Secret-shaped fields use a `Secret` type whose YAML unmarshaller refuses a literal value (`REQ-OPS-003`) — a `config.yaml` containing `database.password: hunter2` fails to load rather than working, so the invariant is enforced by the type system rather than code review.

## Observability

`REQ-OPS-004` — see [`observability-spec.md`](observability-spec.md) for SLIs/SLOs. In brief: OpenTelemetry tracing (with a hand-written span exporter rendering to structured `slog` records, since the module proxy blocks fetching a real OTLP exporter package — see [`internal/infrastructure/telemetry/doc.go`](../internal/infrastructure/telemetry/doc.go)), Prometheus metrics, and structured logging, wired once for the whole platform.

## Reliability primitives

`internal/infrastructure/resilience` provides retry-with-full-jitter-backoff, a circuit breaker, a token-bucket rate limiter, a bounded worker pool, and deadline propagation — as generic primitives with no dependency on `internal/domain` or `internal/ports`, so an AWS call, an LLM completion and an outbound webhook can each supply their own error classifier (defaulting to `core.Retryable`) without coupling this package to CloudOptix's own error taxonomy.

## Server lifecycle

`internal/infrastructure/server` (`REQ-OPS-001`) wraps `net/http.Server` with sane timeouts, a TLS floor, graceful shutdown that waits for in-flight requests, and a liveness/readiness/health triple where liveness never depends on an external dependency (so a database outage never triggers a pod restart storm) and readiness always does (so the load balancer only routes to a pod that can actually serve).

## Multi-tenancy and quotas

`tenancy.QuotasFor(plan)` sets numeric limits per commercial tier (trial/standard/enterprise/internal) — `MaxAWSAccounts`, `MaxResources`, `MaxConcurrentDiscovery`, `MaxSimulationsPerDay`, `MaxCopilotTokensPerDay`, `MaxAutomationsPerDay`, `RetentionDays`. CloudOptix does not gate a safety feature (policy, approvals, rollback, audit) by tier — every tenant on every plan gets the full safety machinery; only throughput and retention scale with plan.

## Security posture (summary — full detail in [`security-spec.md`](security-spec.md))

AssumeRole-only AWS access with external-ID confused-deputy defence, four separate IAM role scopes, tenant isolation enforced structurally at every repository and the knowledge store, table-driven RBAC across nine roles, and a hash-chained audit log. No credential of any kind is accepted as a literal in configuration or in a specification.

## Scale assumptions (untested)

The demo estate is ~500 resources; `tenancy.QuotasFor(PlanEnterprise)` declares an intended ceiling of 500,000 resources and 500 AWS accounts per tenant. Nothing in this codebase has verified the platform holds up at that scale — this is a stated assumption in the domain model's quota design, not a load-tested claim. See [`docs/testing-spec.md`](testing-spec.md).

## Compliance-adjacent capabilities (not compliance certification)

- **Audit trail**: hash-chained, tenant-scoped, closed action vocabulary (`REQ-AUD-*`).
- **Segregation of duties**: `govern.Rule.RequireDistinctApprover`, used by the `regulated` and `conservative` policy packs.
- **Data residency declaration**: `spec.Security.DataResidency`, a declared field with no enforcement engine wired to it in this codebase — a gap, not a claim of enforcement.
- **PII handling declaration**: `spec.Security.PIIHandling`, similarly declarative.

None of the above should be read as a compliance certification (SOC 2, PCI-DSS, HIPAA, FedRAMP); they are the structural building blocks a compliance program would use, present in the domain model, not independently audited or certified.
