# ADR-0001: Modular monolith over microservices

## Status

Accepted, implemented.

## Context

CloudOptix has twelve application-layer engines (onboarding, discovery, costing, twin, economics, optimization, simulation, compiler, governance, automation, learning, copilot) that are tightly coupled by data: discovery feeds cost attribution, cost attribution feeds optimization, optimization feeds governance, governance feeds execution, execution feeds the savings ladder, and the savings ladder feeds learning, which recalibrates optimization. A recommendation flowing end to end touches most of these engines in one logical operation.

## Decision

Ship one Go binary — every application service in one process, behind one `chi` router (`internal/transport/http`) — plus a small number of background workers (discovery, execution, notification, learning) that consume the same event bus rather than run as separate services. Internally, the codebase is still strictly layered (`internal/domain` → `internal/ports` → `internal/application` → `internal/adapters`/`internal/transport`), so a future extraction into services remains possible along the same seams if it is ever needed — see [`architecture.md`](../architecture.md)'s layering diagram.

## Consequences

**Positive:**
- A recommendation's full lifecycle (discover → attribute → decide → govern → execute → validate → learn) is a sequence of in-process function calls, not a chain of network hops each with its own latency, serialization cost, and partial-failure mode.
- One deployable artifact, one set of migrations, one place to reason about a request's full trace.
- The strict `ports`-interface boundary between application and adapter layers means the *option* to split a service out later is preserved without a rewrite — the interface a hypothetical `optimization-service` would expose already exists as `ports.OptimizationService`.

**Negative:**
- The whole platform scales as one unit for CPU/memory-bound work inside the API tier (the worker tier already scales independently — see the deployment diagram).
- A bug in one engine's code path shares a process with every other engine; there is no service-level blast-radius containment the way there would be with independent services (mitigated at the *AWS mutation* level by the four-phase execution discipline and IAM role scoping, not at the *process* level).
- No service can be deployed, rolled back, or scaled independently of the others in this shape.

## Alternatives considered

**Microservices per engine.** Rejected for this stage: twelve services with their own APIs, their own data ownership boundaries, and their own deployment pipelines would multiply operational surface area for a platform that does not yet have a single production tenant, in exchange for independent scalability the current workload does not need (see [`docs/system-requirements.md`](../system-requirements.md)'s scale assumptions — untested at any scale that would justify the split).

**A single undivided package.** Rejected outright: the strict `internal/domain`/`internal/ports`/`internal/application`/`internal/adapters` layering is what makes `internal/adapters/memstore` and `internal/adapters/postgres` interchangeable, and what makes the domain layer's determinism (and therefore its auditability) possible in the first place. The modular monolith is "one process," not "one undifferentiated codebase."
