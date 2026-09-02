# Onboarding transcript: Loopwave

A five-person serverless startup — the shortest, thinnest conversation of
the four, deliberately. It shows the agent not over-asking a small team for
structure they don't have yet, and approving a genuinely minimal
specification rather than padding it with defaults. Companion specification:
[`specs/examples/serverless-startup.yaml`](../../specs/examples/serverless-startup.yaml).

## Transcript

> **CloudOptix:** Hi — I'll help you set up CloudOptix. What's your company called, and what does the business do?
>
> **User:** Loopwave. We're a tiny SaaS product, webhook relay and event transformation for other developers. Five of us total, all engineers including the two founders.
>
> **CloudOptix:** `organization.name = "Loopwave"` **[CONFIRMED]**, `organization.industry = "saas"` **[CONFIRMED]**, `organization.size = "5 employees"` **[CONFIRMED]**. What's your architecture look like?
>
> **User:** Fully serverless — Lambda, DynamoDB, API Gateway, SQS and EventBridge for the async stuff. One AWS account, we haven't needed more than that.
>
> **CloudOptix:** `application.architecture.style = "serverless"` **[CONFIRMED]**, `computePlatforms = ["lambda"]` **[CONFIRMED]**, `databases = ["dynamodb"]` **[CONFIRMED]**, `messaging = ["sqs", "eventbridge"]` **[CONFIRMED]**. How do you deploy — any particular framework, and do you have a settled deployment model, like blue/green or canary?
>
> **User:** We use SAM. Deployment model — honestly no, someone just runs `sam deploy` from their laptop when it's ready. There's no formal process.
>
> **CloudOptix:** That's a completely normal answer at this stage — I'll leave `application.architecture.deploymentModel` **[UNKNOWN]** rather than force a label onto something informal. `iac = ["sam"]` **[CONFIRMED]**. What's the core thing you'd want cost tracked against?
>
> **User:** Webhook relay volume, basically — we do around 42 million webhooks a month right now. We're pre-Series-A, every dollar actually matters, we're trying to extend runway.
>
> **CloudOptix:** `business.transactions[0] = {name: "webhook-relay", monthlyVolume: 42000000, critical: true}` **[CONFIRMED]**. Given runway matters directly, I'd suggest a Cost SLO on cost-per-webhook rather than only tracking absolute spend — do you have a rough target, or should we set one after seeing a real bill?
>
> **User:** We don't have a real number yet. Set it loose for now, we'll tighten once we've actually looked at a full month of Cost Explorer data.
>
> **CloudOptix:** I'll set `objectives.costSlos[0] = {name: "relay-cost-per-webhook", kind: "cost_per_transaction", transaction: "webhook-relay", errorBudgetPct: 0.25}` with a wide error budget and no fixed target yet **[CONFIRMED — target intentionally deferred]**. For automation — with a team this small, do you want CloudOptix executing safe changes automatically, or reviewing everything yourselves?
>
> **User:** Actually let's keep automation off for now. We're small enough that we'd rather just look at the recommendations ourselves for a while and build trust in what it's suggesting before letting anything auto-execute.
>
> **CloudOptix:** `automation.enabled = false` **[CONFIRMED]**. Reasonable at any size, and especially sensible as a first step. Given the small team and low current spend, I'd suggest `governance.productionChangesRequireApproval = false` — with five people who are all engineers, a formal approval gate may be more process than you need right now. Want that, or would you rather keep the approval gate anyway?
>
> **User:** No approval gate is fine, we'll just review things as we go. We're not going to accidentally run a production incident past ourselves.
>
> **CloudOptix:** `governance.productionChangesRequireApproval = false` **[CONFIRMED]**. That's a complete, if intentionally minimal, specification. Completeness: 82% of the fields that apply to an estate this size are set (several sections — like a formal change-management system, or dedicated approver roles — simply don't apply yet and are left empty rather than filled with placeholders). Ready to review?

## Resulting specification (excerpt, version 1)

```yaml
apiVersion: cloudoptix.io/v1
kind: CloudOptixSpec

organization:
  name: Loopwave
  industry: saas
  size: "5 employees"

application:
  name: loopwave-api
  architecture:
    style: serverless
    computePlatforms: [lambda]
    databases: [dynamodb]
    messaging: [sqs, eventbridge]
    deploymentModel: ""   # UNKNOWN — no formal deployment process exists
    iac: [sam]

business:
  transactions:
    - name: webhook-relay
      monthlyVolume: 42000000
      critical: true
      # targetCostPerUnit intentionally omitted at this version

objectives:
  costSlos:
    - name: relay-cost-per-webhook
      kind: cost_per_transaction
      transaction: webhook-relay
      errorBudgetPct: 0.25
      # target intentionally omitted — wide error budget set instead of a guessed target

automation:
  enabled: false

governance:
  productionChangesRequireApproval: false
```

## Provenance breakdown

| Path | Provenance | Source | Note |
|---|---|---|---|
| `organization.*` | CONFIRMED | user | — |
| `application.architecture.style` / `computePlatforms` / `databases` / `messaging` / `iac` | CONFIRMED | user | — |
| `application.architecture.deploymentModel` | UNKNOWN | user explicitly described an informal, ad hoc process | recorded honestly as unknown rather than mislabeled as some named deployment strategy it isn't |
| `business.transactions[0].monthlyVolume` | CONFIRMED | user | — |
| `business.transactions[0].targetCostPerUnit` | UNKNOWN | user deferred pending real billing data | — |
| `objectives.costSlos[0].errorBudgetPct` | CONFIRMED | user (agent proposed a wide value given no target existed yet, user accepted) | wider than the `0.05`–`0.10` typical of the other three transcripts, appropriately reflecting genuine uncertainty rather than false precision |
| `automation.enabled` | CONFIRMED | user | — |
| `governance.productionChangesRequireApproval` | CONFIRMED | user | notable as the one field across all four transcripts where the agent proactively suggested *relaxing* a default (`false` instead of the platform's usual `true` starting suggestion) based on team size and composition, and the user agreed — a case worth citing because it shows the agent adapting its own suggestions to context rather than always defaulting toward stricter settings |

At 82% completeness with no `Blocking: true` open questions, this
specification was approvable in its very first version — the smallest
tenant of the four onboarding examples in this directory produced the
simplest first-approval path, which is the expected relationship between
estate complexity and onboarding conversation length, not a coincidence of
this particular script.
