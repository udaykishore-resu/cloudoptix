# Onboarding transcript: Ashcroft Custodial Trust

Regulated financial-services ledger platform. This transcript shows the
onboarding agent applying more scrutiny than usual — not because the flow
branches for regulated industries, but because a careful user in a
regulated business asks more precise questions, and the deterministic
validation in `spec.Validate()` catches a real mistake before it becomes an
approved specification. Companion specification:
[`specs/examples/regulated-financial-services.yaml`](../../specs/examples/regulated-financial-services.yaml).

## Transcript

> **CloudOptix:** Hi — I'll help you set up CloudOptix. What's your company called, and what does the business do?
>
> **User:** Ashcroft Custodial Trust. We're a custodial bank — we hold and settle securities for institutional clients. Core banking, heavily regulated: SOX, SEC 17a-4 recordkeeping requirements.
>
> **CloudOptix:** `organization.name = "Ashcroft Custodial Trust"` **[CONFIRMED]**, `organization.industry = "financial-services"` **[CONFIRMED]**. I'll flag SOX and SEC 17a-4 for the security section — `security.complianceFrameworks = ["sox", "sec-17a-4"]` **[CONFIRMED]**, and note that SEC 17a-4 in particular implies long audit retention; I'll set `security.auditRetentionDays = 2555` (seven years) as a starting point **[INFERRED — rationale: "SEC 17a-4 requires broker-dealer records retained a minimum of six years; seven years (2555 days) is a common compliance buffer above the statutory minimum"]** — confirm your actual retention requirement, since this varies by exact regulatory posture.
>
> **User:** Seven years matches our policy, that's correct. And to be clear up front — we do not want any automated changes in this environment. Everything gets reviewed by a human, full stop, even in staging.
>
> **CloudOptix:** `security.auditRetentionDays = 2555` **[CONFIRMED]**, and noted on automation — `automation.enabled = false` **[CONFIRMED]**, with no environments exempted. I want to be precise here since this matters: does "no automated changes" mean CloudOptix shouldn't even *generate* recommendations, or that recommendations are fine but nothing should execute without a human?
>
> **User:** Recommendations are fine, even encouraged — we just want a human approving and executing every single change, no exceptions.
>
> **CloudOptix:** Understood — that's exactly what `automation.enabled = false` does: the rule engine still runs, findings and recommendations are still generated and shown, nothing executes without going through `POST /executions/{id}/approve` first. What's your core platform built on?
>
> **User:** The ledger itself is a monolith on EC2 — deliberately not containerized, it's old and extremely well-tested and nobody wants to touch the deployment model right now. RDS Postgres behind it, multi-AZ. Settlement runs as an overnight batch job.
>
> **CloudOptix:** `application.architecture.style = "monolith"` **[CONFIRMED]**, `computePlatforms = ["ec2"]` **[CONFIRMED]**, `databases = ["rds-postgresql"]` **[CONFIRMED]**, `deploymentModel = "active-passive-multi-az"` **[CONFIRMED]**. Tell me about the transaction volume that matters for cost.
>
> **User:** About 4.2 million settled trades a month. We'd want that tracked but we're honestly not sure yet what a reasonable per-trade cost target even is for us — we've never measured it before.
>
> **CloudOptix:** `business.transactions[0] = {name: "trade-settlement", monthlyVolume: 4200000, critical: true}` **[CONFIRMED]**. I'll leave the cost-per-trade target `UNKNOWN` for now rather than propose a number with no basis — once discovery and a billing cycle or two of data exist, we can set a defensible target together instead of guessing. Given everything so far, I'd suggest the `regulated` policy pack rather than `conservative` — it adds compliance-specific approval and freeze-window behavior on top of conservative's baseline. Want that, or something else?
>
> **User:** Regulated is right, yes. And risk tolerance should be low — we exclude a lot of action types outright, not just gate them behind approval. Snapshots, terminations, stops, security group changes — none of that should even be *recommended* for the ledger database.
>
> **CloudOptix:** `optimization.riskTolerance = "low"` **[CONFIRMED]**, `governance.policyPack = "regulated"` **[CONFIRMED]**, and I've noted the exclusion list — `optimization.excludedActions = ["delete_snapshot", "terminate_instance", "stop_instance", "modify_security_group"]` **[CONFIRMED]**. One thing I need to flag: you mentioned the ledger database directly — can you give me its resource name or tag so I can add it to `optimization.excludedResources` specifically, on top of the action-level exclusions? Action exclusions apply platform-wide; a resource-level exclusion is a stronger, more specific guarantee for this one database.
>
> **User:** Good catch — it's tagged `ledger-db-primary`, and there's a standby too, `ledger-db-standby`.
>
> **CloudOptix:** `optimization.excludedResources = ["ledger-db-primary", "ledger-db-standby"]` **[CONFIRMED]**. That's a solid draft. Completeness: 71% — the main gap is the cost-per-trade target, intentionally left open, plus AWS account details pending connection. Ready to review?

## Validation catching a real mistake, before approval

After this conversation, the tenant admin (a different person from the one
who had the conversation) reviewed the draft and, while filling in the AWS
account section by hand in the review UI, pasted the account's **alias**
(`ashcroft-prod`) into the account ID field instead of its 12-digit numeric
ID. `spec.Validate()` rejected the draft:

```
BLOCKING  aws.accounts[0].id  does not match required format ^[0-9]{12}$
          (got "ashcroft-prod")
```

`Version.Approve` refused to transition the version to `approved` until
this was corrected — the deterministic validation checks described in
`docs/onboarding-spec.md` (SPEC-ONB-004) exist for exactly this class of
mistake, and this is the specific failure mode `docs/runbooks/tenant-onboarding-support.md`
calls out as the most common cause of a blocked approval in practice.

## Resulting specification (excerpt, version 1, approved after correction)

```yaml
apiVersion: cloudoptix.io/v1
kind: CloudOptixSpec

organization:
  name: Ashcroft Custodial Trust
  industry: financial-services

application:
  name: ledger-platform
  architecture:
    style: monolith
    computePlatforms: [ec2]
    databases: [rds-postgresql]
    deploymentModel: active-passive-multi-az

business:
  transactions:
    - name: trade-settlement
      monthlyVolume: 4200000
      critical: true
      # targetCostPerUnit intentionally omitted — no historical baseline existed

optimization:
  riskTolerance: low
  excludedActions: [delete_snapshot, terminate_instance, stop_instance, modify_security_group]
  excludedResources: [ledger-db-primary, ledger-db-standby]

automation:
  enabled: false

governance:
  policyPack: regulated

security:
  complianceFrameworks: [sox, sec-17a-4]
  auditRetentionDays: 2555
```

## Provenance breakdown

| Path | Provenance | Source | Note |
|---|---|---|---|
| `organization.name` / `industry` | CONFIRMED | user | — |
| `security.complianceFrameworks` | CONFIRMED | user | — |
| `security.auditRetentionDays` | CONFIRMED | user | overrode the agent's own reasonable `INFERRED` default (a case where the inference and the confirmed value happened to match, but the field is recorded as user-confirmed, not as an accepted inference — an important distinction for audit purposes) |
| `automation.enabled` | CONFIRMED | user | agent asked a clarifying question before recording this, distinguishing "no recommendations" from "no auto-execution" — a case where extraction correctly avoided over-interpreting an ambiguous statement |
| `application.architecture.*` | CONFIRMED | user | — |
| `business.transactions[0].monthlyVolume` | CONFIRMED | user | — |
| `business.transactions[0].targetCostPerUnit` | UNKNOWN | user had no historical baseline | left unset rather than guessed — see `docs/architecture-economics-spec.md` on why a fabricated Cost SLO target is worse than none |
| `optimization.riskTolerance` | CONFIRMED | user | — |
| `governance.policyPack` | CONFIRMED | user | agent proposed `regulated`, user confirmed explicitly rather than the agent inferring it silently |
| `optimization.excludedActions` | CONFIRMED | user | — |
| `optimization.excludedResources` | CONFIRMED | user | agent proactively asked for resource-level detail beyond what the user volunteered, once it recognized the stated intent ("shouldn't even be recommended for the ledger database") was better satisfied by a resource-level exclusion than an action-level one alone |
| `aws.accounts[0].id` | initially malformed, corrected before approval | tenant admin (manual review-UI edit, not the conversation) | the one field in this transcript that did not come from the conversation directly, and the one that `spec.Validate()` caught |
