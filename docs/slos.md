# CloudOptix's own SLIs, SLOs, error budgets and alerts

CloudOptix asks a tenant to hold their AWS estate to Cost SLOs and error budgets ([`architecture-economics-spec.md`](architecture-economics-spec.md)). This document applies the same discipline to the platform itself. Because CloudOptix has never been operated in production (see the root README's [Current limitations](../README.md#current-limitations-and-what-production-hardening-would-still-require)), every number below is a **design target**, not an observed measurement — stated explicitly so this document is not mistaken for an operational report.

## Service level indicators

| SLI | Definition | Measured by |
|---|---|---|
| API availability | Successful (`2xx`/`4xx`, i.e. not `5xx`/timeout) responses ÷ total requests to `/api/v1/*` | `internal/infrastructure/server`'s readiness probe + HTTP metrics |
| API latency | p50/p95/p99 request duration, per operation | `internal/transport/http` request middleware metrics |
| Discovery run success rate | Discovery runs completing with zero permanently-failed jobs ÷ total runs | `DiscoveryRun` status, `internal/application/discovery` |
| Discovery job success rate (finer grain) | Successful (service × region) jobs ÷ total jobs attempted | Same |
| Cost ingestion freshness | Time since the most recent successfully ingested cost period | `ports.CostIngestor` / `IngestResult` |
| Execution plan success rate | Plans reaching `PlanValidated` ÷ plans reaching `PlanExecuting` | `execute.Plan` state |
| Rollback success rate | Rollbacks reaching `PlanRolledBack` ÷ rollbacks attempted | Same |
| LLM completion availability | `ports.LLMProvider.Healthy()` uptime, per provider | `internal/adapters/llm/middleware` |
| Audit chain integrity | `audit.VerifyChain` passes on every scheduled verification sweep | `internal/domain/audit` |
| Copilot grounding rate | Answers requiring zero regeneration pass ÷ total answers | `internal/application/copilot/grounding.go` |

## Service level objectives (design targets)

| SLO | Target | Rationale |
|---|---|---|
| API availability | 99.9% monthly | Standard for a platform that gates infrastructure changes; not so high it demands multi-region active-active before anyone has used it once |
| API p99 latency (read operations) | < 800ms | Twin/cost/economics reads back a graph or a computed footprint, not a raw table scan |
| API p99 latency (write operations) | < 2s | Spec approval, policy save, execution-plan creation do real validation work synchronously |
| Discovery job success rate | ≥ 98% | A single throttled/denied (service × region) job should be rare and isolated (SPEC-DSC-002), not the common case |
| Cost ingestion freshness | < 24h behind AWS's own CUR delivery cadence | CUR itself typically delivers with up to a 24h lag; CloudOptix should not add materially to that |
| Execution plan success rate | ≥ 95% (of plans that reach execution) | A plan that reaches `Executing` has already passed governance and preflight; a meaningful failure rate there signals a preflight-check gap |
| Rollback success rate | 100% | A failed rollback is the single worst operational outcome this platform can produce — see [`docs/runbooks/failed-rollback.md`](runbooks/failed-rollback.md). Anything short of 100% here is a P0 incident, not a statistic to trend |
| LLM completion availability (effective, via fallback) | 99.99% | The `fallback` provider degrading to `deterministic` means the *effective* availability of "a completion returns" should be far higher than any single provider's own uptime |
| Audit chain integrity | 100% | A single verification failure is a security incident, not a budget to spend down |
| Copilot grounding rate | ≥ 97% | The 3% allowance covers legitimate "I don't have enough information" caveated answers, not silent hallucination |

## Error budgets

Following [`architecture-economics-spec.md`](architecture-economics-spec.md)'s own `econ.EvaluateBudget` model, applied reflexively to the platform:

- **API availability (99.9%/month):** a 0.1% monthly error budget ≈ 43 minutes of `5xx`/timeout budget. Burn rate computed the same way `EconomicErrorBudget.BurnRate` is: consumption pace ÷ elapsed-window pace.
- **Discovery job success rate (98%):** budget consumed per failed job that is *not* a correctly-classified permission or throttle failure (those are expected, isolated, and by design do not count against the estate's model quality — see SPEC-DSC-002's tombstone-scoping guarantee). A budget breach here means jobs are failing for reasons discovery's own retry/backoff logic should have absorbed.
- **Rollback success rate and audit chain integrity carry no error budget at all** — both are treated as hard `0` tolerance, matching how a `breached` Cost SLO (target itself exceeded, not merely the budget) is handled: immediate incident response, not a burn-rate chart. See [`docs/runbooks/failed-rollback.md`](runbooks/failed-rollback.md).

## Alerting (design intent — no alertmanager/PagerDuty integration is wired in this codebase)

| Condition | Severity | Response |
|---|---|---|
| API availability burn rate > 2x over a rolling hour | Page | On-call investigates `/readyz` dependency checks first (the split from `/healthz` is designed to make this immediate) |
| Any rollback reaches `PlanRollbackFailed` | Page, immediately | See [`docs/runbooks/failed-rollback.md`](runbooks/failed-rollback.md) |
| `audit.VerifyChain` reports a break | Page, immediately, treat as a security incident | Chain break at sequence N implicates every record from N onward for that tenant |
| Discovery job failure rate > 5% over a rolling day for one tenant | Notify (not page) | Usually an IAM permission gap — see [`docs/runbooks/discovery-iam-gaps.md`](runbooks/discovery-iam-gaps.md) |
| LLM provider `Healthy()` false for > 5 minutes | Notify | `fallback` is already degrading traffic; this is "the primary needs attention," not "the platform is down" — see [`docs/runbooks/llm-provider-outage.md`](runbooks/llm-provider-outage.md) |
| Cost ingestion freshness > 48h | Notify | See [`docs/runbooks/cost-data-staleness.md`](runbooks/cost-data-staleness.md) |
| Copilot grounding rate < 90% over a rolling day | Notify | Investigate whether a corpus change or a provider change degraded retrieval quality |
| Any tenant's `EconomicErrorBudget` reaches `breached` | Notify tenant (this is the tenant's own SLO alerting, `econ.BreachActions`, not a platform-operations alert) | Handled entirely inside the tenant's own notification configuration — see [`architecture-economics-spec.md`](architecture-economics-spec.md) |

## What this document is not

It is not evidence any of these targets have ever been met, monitored against, or alerted on in a live environment. It is the SLO design a production rollout of this codebase would start from, written down before that rollout exists rather than after — for the same reason a Cost SLO is more useful declared in advance than reconstructed after the fact.
