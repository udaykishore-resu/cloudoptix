# Onboarding transcript: Meridian Retail

Mid-market e-commerce, single AWS account, conservative posture at first
approval. This is the same conversation quoted in the root
[`README.md`](../../README.md#example-onboarding-conversation), reproduced
here in full alongside its resulting specification and provenance
breakdown. A later, discovery-informed revision of this same tenant —
after real cost data confirmed the checkout unit economics and the team
grew comfortable enabling automation in non-production — is
[`specs/v1/cloudoptix.yaml`](../../specs/v1/cloudoptix.yaml); the diff
between the two is described at the end of this file.

## Transcript

> **CloudOptix:** Hi — I'll help you set up CloudOptix. What's your company called, and what does the business do?
>
> **User:** We're Meridian Retail. Mid-size e-commerce, mostly apparel. About 40 people in engineering.
>
> **CloudOptix:** Got it. `organization.name = "Meridian Retail"` **[CONFIRMED]**, `organization.industry = "e-commerce/retail"` **[CONFIRMED]**. Based on a 40-person engineering org, I'll tentatively size you as `organization.size = "mid-market"` **[INFERRED — rationale: "40 engineers is consistent with a mid-market classification (50-500 employees) rather than enterprise; confirm if company headcount differs materially from engineering headcount"]**. Does that sound right, or is total headcount larger?
>
> **User:** Total headcount is about 300, so yeah mid-market is fine. We run everything on AWS in us-east-1, one account for now.
>
> **CloudOptix:** `aws.accounts[0].regions = ["us-east-1"]` **[CONFIRMED]**. I'll need the account ID and a role ARN once you're ready to connect — we can do that after the spec is drafted. What does your architecture look like — microservices, a monolith, something else?
>
> **User:** Mostly microservices on ECS Fargate, Postgres via RDS, Redis for sessions. Honestly I don't know our exact deployment model — we've got three teams and I'm not sure they all deploy the same way.
>
> **CloudOptix:** That's fine, we'll leave `application.architecture.deploymentModel` **[UNKNOWN]** and discovery can often infer it from what's actually running (mixed deployment models show up as a finding, not a blocker). `application.architecture.style = "microservices"` **[CONFIRMED]**, `computePlatforms = ["ecs_fargate"]` **[CONFIRMED]**, `databases = ["rds_postgres"]` **[CONFIRMED]**, `caches = ["redis"]` **[CONFIRMED]**. Let's talk about what matters commercially — do you have a checkout or order-placement flow you'd want to track cost against specifically?
>
> **User:** Yes, checkout is the big one — we do around 10 million checkouts a month. We'd like checkout cost to stay under two cents each, we've been burned by cost creep there before.
>
> **CloudOptix:** `business.transactions[0] = {name: "checkout", monthlyVolume: 10000000}` **[CONFIRMED]**, and I'll set a Cost SLO: `objectives.costSlos[0] = {name: "checkout-unit-cost", kind: "cost_per_transaction", transaction: "checkout", target: 0.02, window: "calendar_month"}` **[CONFIRMED]**. What should happen if that budget is at risk of being blown — just an alert, or something stronger?
>
> **User:** Alert first, and if it actually breaches, freeze anything that would make it worse.
>
> **CloudOptix:** `breachActions = ["notify", "freeze_cost_increases"]` **[CONFIRMED]**. One more on risk appetite: how comfortable are you with CloudOptix making changes automatically, versus asking every time?
>
> **User:** Let's start conservative. Nothing auto-executes, everyone approves everything, even in staging.
>
> **CloudOptix:** `optimization.riskTolerance = "low"` **[CONFIRMED]**, `automation.enabled = false` **[CONFIRMED]**, policy pack set to `conservative`. Actually — before we finish, want to revisit the checkout target? A lot of teams start at 2 cents and find it's tighter than their actual architecture supports once discovery runs.
>
> **User:** Good call, let's loosen it to 2.5 cents for now and we'll tighten it once we've seen real numbers.
>
> **CloudOptix:** Updated: `objectives.costSlos[0].target = 0.025` **[CONFIRMED — supersedes prior value]**. That's everything I need to draft a complete specification. Completeness: 78% (2 fields need your confirmation, 1 is intentionally unknown pending discovery). Ready to review?

## Resulting specification (version 1, approved)

```yaml
apiVersion: cloudoptix.io/v1
kind: CloudOptixSpec

organization:
  name: Meridian Retail
  industry: e-commerce/retail
  size: mid-market

application:
  name: meridian-storefront
  domain: e-commerce
  architecture:
    style: microservices
    computePlatforms: [ecs_fargate]
    databases: [rds_postgres]
    caches: [redis]
    deploymentModel: ""   # UNKNOWN — pending discovery

aws:
  accessMode: assume_role
  accounts:
    - id: "PENDING"       # captured at AWS account connection, not in this conversation
      environment: production
      regions: [us-east-1]
      production: true

business:
  transactions:
    - name: checkout
      monthlyVolume: 10000000
      critical: true

objectives:
  costSlos:
    - name: checkout-unit-cost
      kind: cost_per_transaction
      transaction: checkout
      target: 0.025
      window: calendar_month
      errorBudgetPct: 0.05
      breachActions: [notify, freeze_cost_increases]

optimization:
  riskTolerance: low
  spotAllowed: false

automation:
  enabled: false

governance:
  productionChangesRequireApproval: true
  policyPack: conservative
```

## Provenance breakdown

| Path | Provenance | Source | Note |
|---|---|---|---|
| `organization.name` | CONFIRMED | user | — |
| `organization.industry` | CONFIRMED | user | — |
| `organization.size` | CONFIRMED | user | overrode the agent's initial `INFERRED` value of `mid-market`, coincidentally landing on the same value |
| `application.architecture.style` | CONFIRMED | user | — |
| `application.architecture.computePlatforms` | CONFIRMED | user | — |
| `application.architecture.databases` | CONFIRMED | user | — |
| `application.architecture.caches` | CONFIRMED | user | — |
| `application.architecture.deploymentModel` | UNKNOWN | user said they didn't know | expected to be resolved by discovery; see below |
| `aws.accounts[0].regions` | CONFIRMED | user | account ID and role ARN not yet captured — set at account-connection time, a separate step after approval |
| `business.transactions[0].monthlyVolume` | CONFIRMED | user | — |
| `objectives.costSlos[0].target` | CONFIRMED | user | revised mid-conversation from `0.02` to `0.025`; the later value supersedes the earlier one per `spec.Field[T]` semantics, no diff entry for the abandoned intermediate value |
| `objectives.costSlos[0].breachActions` | CONFIRMED | user | — |
| `optimization.riskTolerance` | CONFIRMED | user | — |
| `automation.enabled` | CONFIRMED | user | — |
| `governance.policyPack` | INFERRED | rationale: "risk tolerance low and automation disabled map directly to the conservative reference policy pack" | not independently confirmed by the user in this conversation, but consistent with what they stated |

Completeness at approval: 13 of 15 tracked fields `CONFIRMED`, 1 `UNKNOWN`
(intentionally, pending discovery), 1 `INFERRED` (the policy pack mapping).
`ReadyForReview = true` — none of the outstanding fields were `Blocking`.

## Later revision: what changed by the time of `specs/v1/cloudoptix.yaml`

Roughly two months after this version was approved and the AWS account was
connected, discovery had run repeatedly and cost data had accumulated
against the `checkout` transaction. Real numbers came in tighter than the
initial `$0.025` target — the architecture actually supports `$0.0061` per
checkout — and the team, having seen several months of stable automation
candidates flagged by the rule engine with no incidents, revised their risk
posture. `specs/v1/cloudoptix.yaml` is version 4 of this same tenant's
specification (versions 2 and 3 were smaller edits: connecting the second
AWS account for staging, and adding the `checkout-events` workload once
discovery found it). The structural diff (`spec.Diff`) between version 1
above and version 4 includes, among other changes:

| Path | Version 1 | Version 4 | Impact (as `spec.Diff` would state it) |
|---|---|---|---|
| `objectives.costSlos[0].target` | `0.025` | `0.0061` | "Tightens the checkout cost-per-transaction target by 76%. Recommendations and SLO breach evaluation will now trigger against real observed unit economics rather than an initial estimate." |
| `automation.enabled` | `false` | `true` | "Enables autonomous execution for actions in `automation.autoExecuteActions`, scoped to `automation.environments`. Production remains unaffected unless `production` environment is later added to that list." |
| `optimization.riskTolerance` | `low` | `medium` | "Widens the set of recommendations the rule engine will surface and the policy pack will permit for auto-execution. Does not itself change what auto-executes without a corresponding `automation.autoExecuteActions` entry." |
| `governance.policyPack` | `conservative` | `balanced` | "Switches the deny-biased evaluation baseline. See docs/automation-spec.md for the full effect ordering `balanced.yaml` applies relative to `conservative.yaml`." |
| `application.architecture.deploymentModel` | `""` (UNKNOWN) | `"multi-az-active-active"` | "Resolved by discovery, not by a later conversation turn — this is the `UNKNOWN` field's typical resolution path described in `docs/runbooks/tenant-onboarding-support.md`." |

This progression — a conservative first approval, followed by deliberate,
diffed, human-approved revisions once real data existed — is the intended
shape of the specification lifecycle, not an exception to it.
