# CloudOptix — Product Requirements Document

## Summary

CloudOptix is a cloud economics platform for AWS. It discovers a customer's estate, attributes cost to the business capability and transaction that caused it (not just the tag on the invoice line), decides — deterministically — what would cost less, prices infrastructure changes before they ship, and executes approved changes through a policy engine an LLM cannot override. It targets engineering and FinOps organizations already past the "look at a Cost Explorer dashboard" stage, who need to know what a *feature* costs, not just what a *service* costs.

## Problem statement

See the root [`README.md`](../README.md#why-traditional-finops-tooling-is-insufficient) for the full argument with a comparison table. In one sentence: billing-dashboard FinOps tools answer "what did this service cost," and an engineering organization actually needs answers to "what does this business capability cost," "what would this architectural change cost before we ship it," and "did last month's optimization actually show up on the bill" — none of which a billing dashboard is built to answer.

## Target users and jobs-to-be-done

| Persona (`core.Role`) | Primary job | What CloudOptix gives them that a billing dashboard doesn't |
|---|---|---|
| FinOps Analyst (`finops_analyst`) | Explain cost changes to finance and engineering leadership in the same meeting | Architecture Economics attribution, cost-per-transaction trend, anomaly explanations tied to a cause, not just a delta |
| Architect (`architect`) | Decide between architectural options before committing | The Mutation Engine's eight-dimension scoring, the Counterfactual Engine, the Cost Compiler on a design's IaC |
| SRE (`sre`) | Keep production safe while acting on cost findings | Blast radius, risk assessment tied to SLO headroom, the four-phase execute/validate/rollback discipline |
| Developer (`developer`) | Know what a PR will cost before merging | The Cost Compiler in CI, with a PR comment and a pass/fail regression gate |
| Tenant Admin (`tenant_admin`) | Own the platform's configuration for their org | Spec-driven onboarding, policy pack selection, automation posture |
| Auditor (`auditor`) | Verify every consequential action was authorized and unaltered | The hash-chained audit log, `audit.VerifyChain` |

## Goals

1. Attribute cost along the architecture graph — direct, indirect, shared, and an honest unattributed remainder — down to a cost-per-transaction figure a team can put an SLO on.
2. Generate optimization recommendations from deterministic rules with auditable confidence, risk and blast-radius scoring — never from an LLM's own judgement.
3. Price an infrastructure change before it ships, with a pass/fail regression gate usable in CI.
4. Execute an approved change safely: plan a rollback before anything runs, re-check policy immediately before the AWS call, validate against baseline, and roll back on regression.
5. Track a savings claim through six honest stages, from "potential" to "confirmed by next month's invoice" — never report the top of that funnel as if it were the bottom.
6. Let a customer answer questions about their own spend conversationally, with every claim in the answer traceable to a tool result from that conversation.
7. Keep an LLM structurally incapable of causing an AWS mutation, at every layer that touches one.

## Non-goals (as of this codebase)

- **Multi-cloud.** CloudOptix is AWS-only. `cloud.Resource`, `cloud.RoleScope`, and every adapter in `internal/adapters/aws` are AWS-specific; there is no Azure or GCP port.
- **Being a general-purpose BI tool.** CloudOptix does not aim to replace a data warehouse or a generic dashboarding tool; its object model is architecture-and-cost-shaped, not arbitrary-metric-shaped.
- **Autonomous execution as the default experience.** Every reference policy pack defaults `default_effect: require_approval`, and validation refuses to let a tenant set it to `auto_execute`. Autonomy is an opt-in narrowing, never the starting posture.
- **Being the system of record for infrastructure.** CloudOptix discovers and (with permission) mutates AWS resources; it does not replace Terraform/CloudFormation as the source of truth for what should exist — the Cost Compiler prices *their* output, it does not generate infrastructure definitions.
- **A finished, deployable product**, as of this documentation pass. See the root README's [Current limitations](../README.md#current-limitations-and-what-production-hardening-would-still-require) section — there is no `cmd/` entrypoint, no exercised production deployment, and no real-AWS-account track record yet.

## Key differentiators (detail in dedicated specs)

| Differentiator | Spec |
|---|---|
| Architecture Economics (direct/indirect/shared attribution, cost per transaction) | [`architecture-economics-spec.md`](architecture-economics-spec.md) |
| Cost SLOs and the Economic Error Budget | [`architecture-economics-spec.md`](architecture-economics-spec.md) |
| Deterministic optimization rule engine | [`optimization-spec.md`](optimization-spec.md) |
| Cost Compiler (price IaC before it ships) | [`optimization-spec.md`](optimization-spec.md) |
| Architecture Mutation Engine / Counterfactual Engine | [`optimization-spec.md`](optimization-spec.md) |
| AI Cost Copilot, grounded | [`ai-spec.md`](ai-spec.md) |
| Six-stage savings ladder, safe execution | [`automation-spec.md`](automation-spec.md) |
| Spec-driven onboarding | [`onboarding-spec.md`](onboarding-spec.md) |

## Success metrics (as designed into the domain model — not yet measured against a live tenant)

- **Waste identified vs. estate spend.** The demo estate's own target envelope, enforced by test (`internal/adapters/awssim/demo_test.go`): 18–20% of total spend identified as waste (verified: $44,206.84 of $185,978.41, ≈23.8%).
- **Savings-funnel conversion**, tracked at every rung (`potential → approved → planned → executed → validated → realized`), not just the top-line "potential savings" number.
- **Cost-per-transaction stability**, tracked against a tenant's declared Cost SLOs and their error-budget burn rate.
- **Grounding rate** of copilot answers (fraction requiring zero regeneration pass) — the copilot's own internal quality signal.
- **Time-to-decision on a recommendation** (potential → approved), which the savings funnel makes directly visible per rung.

None of these have been measured against a real tenant; they are the metrics the domain model is built to make measurable once one exists.

## Release scope of this codebase

This is a from-scratch, single-build engineering effort with no external deployment. Every application service, every domain package, and the full HTTP API surface are implemented and unit/integration tested against in-memory and simulated adapters (`memstore`, `awssim`, `deterministic` LLM, in-process events). The production-grade adapters (`postgres`, real AWS, `anthropic`/`bedrock`) are implemented and independently tested against recorded/mocked interfaces, but the whole stack has never been assembled into a running process or exercised against a live AWS account. See the requirements catalog ([`requirements.md`](requirements.md)) for exactly what is and is not implemented, requirement by requirement, and the root README's limitations section for the honest summary.
