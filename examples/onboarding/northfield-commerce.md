# Onboarding transcript: Northfield Commerce Group

Larger multi-service retail platform, multiple AWS accounts across two
countries, an aggressive optimization appetite set deliberately by a
finance-driven mandate. Companion specification:
[`specs/examples/production-ecommerce.yaml`](../../specs/examples/production-ecommerce.yaml).

## Transcript

> **CloudOptix:** Hi — I'll help you set up CloudOptix. What's your company called, and what does the business do?
>
> **User:** Northfield Commerce Group. We run four regional storefront brands under one platform — northfieldcommerce.com plus three others. About 4,500 employees total, cloud platform team is 60 people.
>
> **CloudOptix:** `organization.name = "Northfield Commerce Group"` **[CONFIRMED]**, `organization.industry = "retail"` **[CONFIRMED]**, `organization.size = "4500 employees"` **[CONFIRMED]**. Four brands on one platform is a useful detail — I'll capture that as context for the application description. What AWS accounts are involved?
>
> **User:** We're in AWS Organizations. Separate prod accounts for US and Canada, a shared staging account, and a sandbox for experiments. Compute is a mix — EKS for most services, some ECS Fargate, Lambda for the event-driven stuff.
>
> **CloudOptix:** `aws.organizationId` noted, four accounts expected **[CONFIRMED — count]**, though I'll need account IDs and role ARNs at connection time for each. `application.architecture.computePlatforms = ["eks", "ecs-fargate", "lambda"]` **[CONFIRMED]**. Given four accounts across two countries, I'm inferring `application.architecture.deploymentModel = "multi-az-active-active"` **[INFERRED — rationale: "separate production accounts per country with no mention of a passive/DR pattern is most consistent with active-active regional deployment; confirm if one region is actually a failover target for the other"]** — is that right, or is one of the two actually a DR target for the other?
>
> **User:** No, both are fully active — Canadian customers get served from the Canada account, US from the US ones. No failover between them.
>
> **CloudOptix:** Good, `deploymentModel` confirmed as stated **[CONFIRMED — supersedes inference]**. Tell me about the transactions that matter — checkout obviously, but you mentioned search matters a lot for a retail platform this size.
>
> **User:** Checkout does about 28 million completed orders a month across everything. Search is enormous — nearly a billion queries a month — and it's a huge chunk of our compute bill even though each query costs almost nothing individually.
>
> **CloudOptix:** `business.transactions[0] = {name: "checkout", monthlyVolume: 28000000, critical: true}` **[CONFIRMED]**, `business.transactions[1] = {name: "search-query", monthlyVolume: 950000000, critical: true}` **[CONFIRMED]**. Do you have target unit costs in mind for either, or should we set those once discovery gives us a real baseline?
>
> **User:** We don't have hard targets yet — we know search is expensive in aggregate but haven't broken it down per-query. Let's leave targets open and revisit after discovery.
>
> **CloudOptix:** That's fine — I'll set up the Cost SLOs with `kind: cost_per_transaction` for both once real numbers exist; for now I'll mark `objectives.costSlos` **[UNKNOWN — pending discovery]** rather than guess at targets. Let's talk risk posture — given the scale here, how aggressive do you want CloudOptix to be about acting on recommendations?
>
> **User:** Pretty aggressive, actually. We have a mandate from finance to get infrastructure spend down materially this year, and staging/sandbox can absolutely auto-remediate waste without a human in the loop every time.
>
> **CloudOptix:** `optimization.riskTolerance = "high"` **[CONFIRMED]**, `automation.enabled = true` **[CONFIRMED]**, scoped to `automation.environments = ["staging", "sandbox", "development"]` **[CONFIRMED]**. Production changes will still require approval regardless of risk tolerance — that's a platform invariant, not a per-tenant setting. What's your target for overall cost reduction this year?
>
> **User:** Finance wants 20% off the current run rate within the fiscal year.
>
> **CloudOptix:** `objectives.costReductionTarget = 0.20` **[CONFIRMED]**, `objectives.timeframe = "current fiscal year"` **[CONFIRMED]**. That's enough for a first draft. Completeness: 64% — the Cost SLO targets and the four AWS account IDs are the main gaps, both expected to close once discovery runs. Ready to review?

## Resulting specification (excerpt, version 1)

```yaml
apiVersion: cloudoptix.io/v1
kind: CloudOptixSpec

organization:
  name: Northfield Commerce Group
  industry: retail
  size: "4500 employees"

application:
  name: storefront-platform
  architecture:
    computePlatforms: [eks, ecs-fargate, lambda]
    deploymentModel: multi-az-active-active

aws:
  accessMode: assume_role
  accounts: []   # four accounts expected; IDs/role ARNs captured at connection time, after approval

business:
  transactions:
    - name: checkout
      monthlyVolume: 28000000
      critical: true
    - name: search-query
      monthlyVolume: 950000000
      critical: true

objectives:
  costReductionTarget: 0.20
  timeframe: "current fiscal year"
  # costSlos intentionally empty — targets pending discovery, tracked as an
  # open question rather than a guessed default

optimization:
  riskTolerance: high

automation:
  enabled: true
  environments: [staging, sandbox, development]

governance:
  productionChangesRequireApproval: true
```

## Provenance breakdown

| Path | Provenance | Source | Note |
|---|---|---|---|
| `organization.name` / `industry` / `size` | CONFIRMED | user | — |
| `application.architecture.computePlatforms` | CONFIRMED | user | — |
| `application.architecture.deploymentModel` | CONFIRMED | user | superseded the agent's own `INFERRED` value after the user corrected the DR assumption — a case where the inference was reasonable but wrong, and the confirmation flow caught it before approval |
| `aws.organizationId` (account count) | CONFIRMED | user | account-level detail (IDs, role ARNs, regions per account) explicitly deferred to the post-approval connection step |
| `business.transactions[0,1].monthlyVolume` | CONFIRMED | user | — |
| `objectives.costSlos` | UNKNOWN | user declined to set targets pending real data | this is the deliberate "I don't know, and that's fine" path described in `docs/runbooks/tenant-onboarding-support.md` — not a blocking gap |
| `optimization.riskTolerance` | CONFIRMED | user | — |
| `automation.enabled` / `environments` | CONFIRMED | user | — |
| `objectives.costReductionTarget` / `timeframe` | CONFIRMED | user | — |

Note the contrast with the Meridian Retail transcript: here the *unit cost
targets* are the field left `UNKNOWN`, not the deployment model — Northfield
came in with clear architecture knowledge but no unit-economics baseline yet,
the inverse of Meridian's situation. This is intentional variety in these
worked examples, not an inconsistency: different companies genuinely know
different things about their own estates at onboarding time, and the
specification format has to represent both honestly.

By the time this tenant reaches the shape of
[`specs/examples/production-ecommerce.yaml`](../../specs/examples/production-ecommerce.yaml),
several further revisions have populated the Cost SLO targets from real
discovery data, added the full nine-workload inventory, and set the
`aggressive` policy pack once the finance mandate's risk appetite was
formalized into governance — each one a separate, diffed, approved version,
not a single large edit.
