# Architecture

Covers `SPEC-ARCH-001..005` and `SPEC-API-001..020`.

## SPEC-ARCH-001 — System context

See the root README's [system context diagram](../README.md#system-context) and [container/component view](../README.md#container--component-view). In one paragraph: CloudOptix is a single Go service (plus workers) that sits between a human user (or CI pipeline), a customer's AWS account (reached only via `AssumeRole`), an LLM provider (used for narration/extraction/tool-routing, never mutation), the customer's own OIDC identity provider, and their notification channels (Slack/email/webhook).

## SPEC-ARCH-002 — Domain layering

`internal/domain` is layered as a directed acyclic dependency graph, enforced by convention (`tools/depguard` is referenced in `core`'s package doc as the intended enforcement mechanism; no such tool is present in this repository — see [Current limitations](../README.md#current-limitations-and-what-production-hardening-would-still-require)):

```
core            (no dependencies but the standard library — Money, TenantID, Provenance, Principal, errors)
  ↑
cloud           (resources, topology, AWS accounts — depends on core)
  ↑
├─ cost         (billed spend — depends on core)
├─ econ         (attributed spend, SLOs, error budgets — depends on core)
├─ optimize     (findings, recommendations — depends on cloud, core)
├─ simulate     (mutation/counterfactual/compiler models — depends on core)
├─ govern       (policy-as-code — depends on core, optimize)
└─ execute      (plans, rollback, savings ladder — depends on core, optimize)
  ↑
spec            (the specification artefact — depends on core, govern for diff-impact)
tenancy         (multi-tenant hierarchy — depends on core)
audit           (tamper-evident log — depends on core)
```

No file in `internal/domain` performs I/O: no HTTP call, no database query, no clock read outside an explicit parameter, no model completion. This is what makes `govern.Evaluate`, `econ.EvaluateBudget`, `optimize.ComputeConfidence` and every other domain function reproducible from their inputs alone — the property [ADR-0005](adr/0005-deterministic-rules-ai-narration.md) and the audit story both depend on.

`internal/application` sits one ring out: one package per engine (`onboarding`, `discovery`, `costing`, `twin`, `economics`, `optimization`, `simulation`, `compiler`, `governance`, `automation`, `learning`, `copilot`), each implementing exactly one interface from `internal/ports/usecases.go`. An application package may call `internal/ports` interfaces and `internal/domain` types; it never imports a concrete adapter package directly (`internal/adapters/postgres`, `internal/adapters/aws/*`, etc.) — that binding happens only in the composition root that would live in `cmd/` (see [Current limitations](../README.md#current-limitations-and-what-production-hardening-would-still-require) — this composition root does not exist yet).

## SPEC-ARCH-003 — Hexagonal ports and adapters

`internal/ports` (`repositories.go`, `services.go`, `usecases.go`, `ai.go`) is the complete capability surface: every persistence operation, every external system interaction, and every application-layer use case is an interface here, with nothing else in the codebase allowed to define a competing shape for the same capability. Two adapter families implement the same ports side by side, verified interchangeable by running the identical application-layer test suite against both:

| Port family | Reference (in-memory / simulated) | Production |
|---|---|---|
| Repositories | `internal/adapters/memstore` (one `*Store`, deep-copies every value in/out via JSON round-trip so a caller can never mutate committed state through a returned pointer) | `internal/adapters/postgres` |
| AWS interaction (discovery, cost, metrics, execution) | `internal/adapters/awssim` (a real, stateful in-memory AWS account — not a stub; see [ADR-0007](adr/0007-in-memory-adapters-as-first-class-runtime.md)) | `internal/adapters/aws/{discovery,costing,metrics,executor,sts}` |
| LLM | `internal/adapters/llm/deterministic` | `internal/adapters/llm/{anthropic,bedrock}` behind `fallback` and `middleware.Chain` |
| Events | `internal/adapters/events.InProcess` | `internal/adapters/events.{EventBridgePublisher,SQSSubscriber}` |
| Knowledge / RAG | `internal/adapters/rag` — this is the *only* implementation; there is no separate production vector-DB adapter | — |

`internal/transport/http` depends only on `ports.Services` — no handler holds a repository or an adapter directly, so the only way to reach persistence is through an application service that has already applied the tenant guard, business rules, and audit trail.

## SPEC-ARCH-004 — Event-driven integration

`internal/adapters/events` implements `ports.EventPublisher`/`ports.EventSubscriber`. `InProcess` is an in-memory worker pool used for local development, the demo tenant, and every test that needs a real (not mocked) bus with no AWS account — it delivers at least once, retries a failing handler with backoff, and dead-letters once retries are exhausted. The production pair, `EventBridgePublisher` (publish) and `SQSSubscriber` (consume), form one logical bus split across a publish side and a consume side because that is how EventBridge and SQS actually work: a rule on the bus routes matching events to one or more queues, each an independent, replayable subscription. Neither implementation promises exactly-once delivery — `ports.Event.IdempotencyKey` exists so a handler can recognise and no-op a redelivery rather than double-apply it. Every event carries `core.TenantID`; `Publish` refuses an empty one rather than treating it as platform-wide.

`SQSSubscriber` deliberately does not reimplement dead-letter bookkeeping client-side: it never deletes a message its handler failed to process, and lets SQS's own `RedrivePolicy`/`maxReceiveCount` own that mechanism, rather than risk a message being "helped" into a client-side DLQ while SQS's own count is still ticking toward the same destination.

## SPEC-ARCH-005 — Notification dispatch architecture

`internal/adapters/notify` implements `ports.Notifier` (SMTP, SES, Slack incoming webhook, generic HMAC-signed webhook) plus a `Dispatcher` that decides, per domain event, which tenant-configured channels should hear about it, what to say, and whether now is an appropriate time to say it. Dispatch and delivery are deliberately two separate steps — `Dispatch` only renders and persists `ports.Notification` records; a separate `SendPending` call performs the send — which is what makes delivery durable across a process restart and independently retryable, without re-deriving "who should hear this and what should it say" every time. A `spec.NotificationChannel.SecretRef` is a reference resolved only at send time via `ports.SecretResolver`; no webhook URL, Slack token, or SMTP credential is ever stored in the specification itself, consistent with the specification's design to be safely committed to a customer's git repository.

## SPEC-API-001..020 — HTTP contract

`internal/transport/http` mounts a `chi` router under `/api/v1`, plus unauthenticated operational endpoints (`/healthz`, `/readyz`, `/metrics`, `/openapi.yaml`). The full contract is `api/openapi.yaml`; see [`docs/traceability.md`](traceability.md) Table C for every operation mapped to a `REQ-API-*` requirement.

**SPEC-API-001 — Declarative RBAC route table.** Authorization is data (`routes.go`'s `[]Route`), not scattered `if !principal.Can(...)` checks in handler bodies. Every route names its method, pattern, and exactly one `core.Permission`; `rbac_test.go` asserts every mutating route in the table carries a non-empty permission — a property that would otherwise only be discoverable by reading every handler by hand. Handlers still re-check the identical permission via `core.Principal.Authorize` before touching a service, in case a future refactor makes a handler reachable by a path the router doesn't know about.

**SPEC-API-002 — Cursor pagination.** `ParseListOptions` reads `limit`, `cursor`, `sort`, `order` from the query string into `ports.ListOptions`; the default and cap are applied once, by `ports.ListOptions.Normalize()`, called from every application service — so this parsing layer cannot drift from the service-layer default by inventing a second one.

**SPEC-API-003 — Idempotency keys on mutating requests.** A client sets `Idempotency-Key` on a request with a real side effect (execution start, plan execution, an approval decision); the response is cached keyed by `(tenant, key, request-body-hash)`. Reusing a key with a materially different body — a bug, or an attempt to smuggle a different request under an old key's cached success — returns `422`, not the stale cached response.

**SPEC-API-004 — RFC 7807 problem+json error format.** Every error response is a `Problem` (`type`, `title`, `status`, `detail`, `instance`, plus a platform-specific `code`, `request_id`, and — when the failure was a validation failure — the full `[]core.ValidationIssue` list). One consistent shape serves both "one thing was wrong" and "several things were wrong," so the API never needs a second error format for the multi-issue case; `problemBase` maps every HTTP status to the same `type`/`title` pair everywhere in the API, so a client integrating once sees the same vocabulary regardless of which handler produced the error.

**SPEC-API-005 — Server-sent event streaming.** `/onboarding/{id}/messages/stream`, `/discovery/runs/{id}/stream`, `/executions/{id}/stream`, and `/copilot/ask/stream` stream incremental progress over SSE (`internal/transport/http/sse_test.go`) rather than requiring a client to poll.

**SPEC-API-006..020** — the remainder of the numbered contract sections (request/response schemas per resource, versioning policy, rate-limit headers, CORS policy, request-size limits, and the OpenAPI document's own structure) are defined directly by `api/openapi.yaml`, which is the canonical machine-readable contract; this document does not restate it field-by-field.

## The 190-odd-entry Route table, at a glance

103 authenticated operations (see [`docs/traceability.md`](traceability.md) Table C for the full list) across 20 tag groups mirroring the application-service list, plus 8 public onboarding operations mounted on a separate table specifically so `rbac_test.go`'s "every mutating route has a permission" assertion never has to special-case onboarding's intentionally-open surface.

## Deployment topology

See the root README's [deployment diagram](../README.md#deployment-modular-monolith--workers). API-tier pods are stateless and horizontally scaled behind a load balancer; the worker tier (discovery, execution, notification, learning) scales independently and consumes the same event bus the API tier publishes to — the split is about where a process runs, not a different code path for "the safe way" versus "the fast way," since both call the identical `ports.Services` bundle.
