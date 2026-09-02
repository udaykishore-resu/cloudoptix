# Optimization scenario: EC2 rightsizing

A single overprovisioned instance in the `shopfleet-prod` demo estate,
followed end to end through every stage of the six-stage savings ladder
(`docs/adr/0009-six-stage-savings-lifecycle.md`). This is the same instance
cited in the root [`README.md`](../../README.md#example-optimization-workflow-end-to-end)'s
walkthrough; this file expands each of those steps with the full
`optimize.Finding` / `optimize.Recommendation` shape.

Pricing figures are taken directly from `internal/adapters/pricing/pricebook.json`
(`r5.2xlarge` on-demand `$0.252/hr`; `r5.large` on-demand `$0.063/hr`).

## 1. Discovery and utilization

`checkout-api-worker-1` is an EC2 instance in the `shopfleet-prod` demo
estate:

- Instance type: `r5.2xlarge` (8 vCPU, 64 GiB)
- Tags: `Application: checkout-api`, `Team: payments`, `Environment: production`
- CPU utilization, trailing 30-day window: P50 5.8%, P95 14.1%
- Utilization profile: `ProfileIdle` (`internal/application/utilization`'s classification for a resource whose sustained load sits well below any headroom threshold a rightsizing decision needs)

## 2. Rule evaluation → Finding

The `ec2-underutilized-rightsize` rule (`rules/compute.yaml`,
`internal/domain/optimize`'s compute rule pack) fires:

```json
{
  "rule_id": "compute.ec2.underutilized-rightsize",
  "rule_name": "EC2 instance underutilized for its allocated size",
  "category": "waste",
  "resource_name": "checkout-api-worker-1",
  "resource_kind": "ec2_instance",
  "account_id": "shopfleet-prod",
  "region": "us-east-1",
  "environment": "production",
  "severity": "medium",
  "summary": "r5.2xlarge running at 5.8% P50 CPU over a 30-day window",
  "detail": "Sustained CPU utilization is well below the threshold at which this instance family/size is justified. Two rungs down the family ladder (r5.large) leaves headroom above the observed P95.",
  "evidence": [
    {
      "label": "cpu_utilization_p50",
      "value": "5.8%",
      "window": "30d",
      "source": "cloudwatch"
    },
    {
      "label": "cpu_utilization_p95",
      "value": "14.1%",
      "window": "30d",
      "source": "cloudwatch"
    }
  ],
  "current_monthly_cost": "$183.96",
  "estimated_monthly_saving": "$137.97"
}
```

## 3. Recommendation

```json
{
  "title": "Rightsize checkout-api-worker-1 from r5.2xlarge to r5.large",
  "action": "resize_instance",
  "rationale": "P50 CPU of 5.8% over 30 days indicates the instance is allocated roughly 4x its sustained load. r5.large provides headroom above the observed P95 (14.1%) while halving cost twice over.",
  "current_state": { "instance_type": "r5.2xlarge", "vcpu": 8, "memory_gib": 64, "monthly_cost": "$183.96" },
  "proposed_state": { "instance_type": "r5.large", "vcpu": 2, "memory_gib": 16, "monthly_cost": "$45.99" },
  "estimated_monthly_saving": "$137.97",
  "estimated_annual_saving": "$1655.64",
  "confidence": 0.86,
  "confidence_basis": [
    { "name": "utilization_sample_size", "value": 0.95, "weight": 0.4, "explanation": "30 days of continuous CloudWatch samples, no gaps" },
    { "name": "utilization_stability", "value": 0.82, "weight": 0.35, "explanation": "P50/P95 spread is narrow; no evidence of a bursty workload the P50 alone would mask" },
    { "name": "tag_completeness", "value": 0.90, "weight": 0.25, "explanation": "resource fully tagged (Application, Team, Environment), supporting confident blast-radius computation" }
  ],
  "risk": {
    "score": 0.22,
    "level": "low",
    "availability_risk": "low",
    "performance_risk": "low",
    "data_loss_risk": "none",
    "factors": [
      { "name": "production_environment", "contribution": 0.15, "explanation": "resource is in a production environment" },
      { "name": "headroom_above_p95", "contribution": -0.10, "explanation": "proposed size still clears observed P95 by a wide margin" }
    ],
    "mitigations": ["scheduled during a declared maintenance window", "validation window observes P99 latency post-change before the saving is realized"]
  },
  "blast_radius": {
    "resources_affected": 1,
    "services_affected": 1,
    "critical_services": 1,
    "workloads_affected": ["checkout-api"],
    "transactions_affected": ["checkout"],
    "estimated_users": 340000,
    "environments_affected": ["production"],
    "cross_account": false,
    "score": 0.31,
    "level": "low",
    "completeness": 1.0
  },
  "reversibility": "fast",
  "complexity": "low",
  "requires_approval": true,
  "auto_executable": false
}
```

`requires_approval: true` here despite a low risk score because the
resource is in `production` — `docs/security-spec.md`'s platform
invariants make production approval non-negotiable regardless of what any
policy pack's rule fold would otherwise permit. See
`docs/adr/0008-policy-deny-bias.md`.

## 4. Governance evaluation

Against the `balanced` policy pack (this tenant's `governance.policyPack`),
`govern.Evaluate` returns `effect: require_approval`, `deciding_rule:
balanced.production.require-approval` — production changes require a human
regardless of confidence or risk score, per the platform invariant.

## 5. Approval and execution plan

A `tenant_admin` approves the recommendation. `execute.Plan` is built with
three steps:

1. **Precondition** — verify current instance type is still `r5.2xlarge` (guards against a race with a manual change made after the recommendation was generated).
2. **Snapshot** — capture the instance's current configuration attributes (not an AWS snapshot; the `Snapshot.Attributes` record `execute.Plan` needs to build the rollback).
3. **Mutate** — `ec2:ModifyInstanceAttribute` / stop-modify-start sequence to `r5.large`, carrying an `IdempotencyKey`.

The rollback plan is built *before* step 3 runs (`execute.Plan.Executable`
refuses a plan with no preceding snapshot) and is trivially feasible: revert
to `r5.2xlarge`, `Reversibility: fast`.

## 6. Validation window

60-minute validation window observing `checkout-api`'s P99 latency and error
rate. Both remain within their `WorkloadSLO` bounds (`latencyP99Ms: 600`,
`errorRateMax: 0.001`) throughout the window — `MinSamples` cleared well
before the window closes. `Verdict: Success`.

## 7. Realization

The following month's ingested Cost & Usage Report line item for this
instance bills at the `r5.large` rate. `SavingsRecord.Advance(StageRealized,
...)` fires only at this point — not at execution, not at validation — the
number that eventually reaches a CFO-facing report.

## Savings ladder summary

| Stage | Amount | When |
|---|---|---|
| Potential | $137.97/mo | at recommendation creation |
| Approved | $137.97/mo | tenant_admin approval |
| Planned | $137.97/mo | `execute.Plan` built |
| Executed | $137.97/mo | mutation landed |
| Validated | $137.97/mo | validation window closed with `Success` |
| Realized | $137.97/mo | confirmed against next month's actual CUR line item |

No value changed across stages in this scenario — worth noting precisely
because it's the unremarkable case. See
[`failed-optimization-rollback.md`](failed-optimization-rollback.md) for
what happens when validation does *not* confirm the prediction.
