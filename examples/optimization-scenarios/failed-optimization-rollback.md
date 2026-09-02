# Optimization scenario: a failed optimization, and the rollback that catches it

The one scenario in this set that does not go as predicted — deliberately
included because a documentation set that only shows successes
misrepresents what "autonomous optimization" actually means in this
platform. This is the same scenario referenced in the root
[`README.md`](../../README.md#example-optimization-workflow-end-to-end) and
worked through operationally in
[`docs/runbooks/failed-rollback.md`](../../docs/runbooks/failed-rollback.md).
This file is the recommendation-and-execution narrative; the runbook is the
operational response to the state this scenario ends in.

Pricing figures from `internal/adapters/pricing/pricebook.json`'s `rds`
section (`on_demand_single_az` base rate `$3.84`, `postgres` engine
multiplier `1.0`, `multi_az` multiplier `2.0`).

## 1. Discovery

`shopfleet-orders-primary`: `db.r5.8xlarge` RDS PostgreSQL, Multi-AZ,
4000 GiB `gp3` storage, tagged `Application: orders-api`,
`Environment: production`, `Criticality: critical`.

- Current cost: compute $5,606.40/mo (`db.r5.8xlarge` Multi-AZ) + storage $460.00/mo = **$6,066.40/mo**
- CPU utilization, trailing 30-day window: P50 22%, P95 61%
- Connection count: stable, no evidence of connection-pool exhaustion risk

## 2. Finding

```json
{
  "rule_id": "database.rds.underutilized-rightsize",
  "rule_name": "RDS instance underutilized for its allocated class",
  "category": "waste",
  "resource_name": "shopfleet-orders-primary",
  "resource_kind": "rds_instance",
  "account_id": "shopfleet-prod",
  "region": "us-east-1",
  "environment": "production",
  "severity": "medium",
  "summary": "db.r5.8xlarge running at 22% P50 CPU, 61% P95, over a 30-day window",
  "detail": "One rung down the family ladder (db.r5.4xlarge) leaves headroom above the observed P95, consistent with the conservative rightsizing heuristic applied to production database instances.",
  "evidence": [
    { "label": "cpu_utilization_p50", "value": "22%", "window": "30d", "source": "cloudwatch" },
    { "label": "cpu_utilization_p95", "value": "61%", "window": "30d", "source": "cloudwatch" }
  ],
  "current_monthly_cost": "$6,066.40",
  "estimated_monthly_saving": "$2,803.20"
}
```

## 3. Recommendation

```json
{
  "title": "Rightsize shopfleet-orders-primary from db.r5.8xlarge to db.r5.4xlarge",
  "action": "resize_rds_instance",
  "rationale": "P95 CPU of 61% still clears the target size's headroom threshold under this platform's conservative one-rung-down heuristic for stateful production databases (contrast with the two-rungs-down heuristic applied to stateless EC2 in examples/optimization-scenarios/ec2-rightsizing.md).",
  "current_state": { "instance_type": "db.r5.8xlarge", "monthly_cost": "$6,066.40" },
  "proposed_state": { "instance_type": "db.r5.4xlarge", "monthly_cost": "$3,263.20" },
  "estimated_monthly_saving": "$2,803.20",
  "estimated_annual_saving": "$33,638.40",
  "confidence": 0.71,
  "confidence_basis": [
    { "name": "utilization_sample_size", "value": 0.95, "weight": 0.4, "explanation": "30 days of continuous CloudWatch samples" },
    { "name": "utilization_stability", "value": 0.58, "weight": 0.4, "explanation": "wider P50/P95 spread than the EC2 scenario (22% to 61%) — some evidence of bursty load the P50 alone understates" },
    { "name": "tag_completeness", "value": 0.95, "weight": 0.2, "explanation": "fully tagged" }
  ],
  "risk": {
    "score": 0.48,
    "level": "medium",
    "availability_risk": "medium",
    "performance_risk": "medium",
    "data_loss_risk": "low",
    "factors": [
      { "name": "production_critical_database", "contribution": 0.30, "explanation": "resource is a production, critical-tagged database — the highest-consequence class of resource this rule category touches" },
      { "name": "p95_proximity_to_headroom_threshold", "contribution": 0.18, "explanation": "61% P95 is closer to the target size's practical ceiling than the EC2 scenario's 14.1%, leaving a narrower safety margin" }
    ],
    "mitigations": ["Multi-AZ failover available during the resize", "validation window observes p99 latency specifically, not just CPU, because CPU alone would not have caught the actual regression in this case"]
  },
  "blast_radius": {
    "resources_affected": 1,
    "services_affected": 4,
    "critical_services": 4,
    "workloads_affected": ["orders-api", "checkout-api", "inventory-sync", "fulfillment-worker"],
    "transactions_affected": ["checkout", "order-fulfillment"],
    "estimated_users": 340000,
    "monthly_revenue_at_risk": "$1,840,000.00",
    "environments_affected": ["production"],
    "cross_account": false,
    "score": 0.67,
    "level": "high",
    "completeness": 1.0,
    "explanation": "a shared production database with four dependent critical services carries a high blast radius regardless of the resize's own risk score — the twin's dependency walk, not the rule's own confidence, drives this number"
  },
  "reversibility": "fast",
  "complexity": "medium",
  "requires_approval": true,
  "auto_executable": false
}
```

Note the contrast with the EC2 scenario: identical rule category
(underutilized rightsizing), but `risk.level: medium` and
`blast_radius.level: high` here versus `low`/`low` there — the difference
is entirely the resource's role in the dependency graph (a shared,
multi-consumer production database) and the narrower utilization margin,
not anything about the rule itself.

## 4. Approval and execution

A `tenant_admin` approves. `execute.Plan` builds a four-step plan: a
precondition check, an RDS snapshot (a real AWS snapshot here, not just
attribute capture — `Snapshot.BackupRefs` records the snapshot identifier,
because an RDS instance-class change is not attribute-reversible the way an
EC2 stop/modify/start is), the Multi-AZ-aware resize mutation, and a verify
step. `RollbackPlan.Feasible: true`, `Reversibility: fast` — reverting to
`db.r5.8xlarge` is itself just another resize.

## 5. Execution

The resize completes successfully. `Plan.State → PlanExecuted →
PlanValidating`. `SavingsRecord.Advance(StageExecuted, $2,803.20, ...)`.

## 6. Validation — where the prediction breaks

The 90-minute validation window observes `checkout` and
`order-fulfillment` transaction P99 latency, per each affected workload's
`WorkloadSLO`. Forty minutes in, `orders-api`'s P99 latency crosses its
declared `latencyP99Ms` bound and stays elevated — the smaller instance
class's connection-handling headroom, adequate under the 30-day CPU profile
that drove the recommendation, is not adequate under a query-pattern
detail the CPU metric alone did not capture (a burst of longer-running
analytical queries from the fulfillment team's own reporting job,
overlapping the validation window by coincidence of timing rather than
because of the resize itself — but the validator does not need to know the
cause to render a verdict; it only needs to know the SLO was breached).

`ValidationPlan` renders `Verdict: Regression` — not `Inconclusive`, not a
borderline call. `Plan.State → PlanRollingBack`.

## 7. Rollback

Because the rollback was marked `Feasible: true` at plan time, this is the
ordinary case, not the harder one `docs/runbooks/failed-rollback.md`
describes for an infeasible rollback. The rollback plan's steps (revert to
`db.r5.8xlarge`, restoring from the pre-change RDS snapshot's captured
attributes) execute. `Plan.State → PlanRolledBack`. A fresh discovery scan,
run specifically to verify the rollback rather than trust the AWS API
call's own success response, confirms `shopfleet-orders-primary` is back at
`db.r5.8xlarge` and `orders-api` P99 latency has returned within its SLO
bound.

## 8. Savings ladder outcome

```json
{
  "stage": "executed",
  "lost": true,
  "lost_reason": "Validation window detected a p99 latency regression on orders-api exceeding its declared SLO bound; the change was rolled back before validation could complete. The resize itself executed and briefly billed at the lower rate, but the saving is not carried forward as realized.",
  "history": [
    { "from": "potential", "to": "approved", "amount": "$2,803.20" },
    { "from": "approved", "to": "planned", "amount": "$2,803.20" },
    { "from": "planned", "to": "executed", "amount": "$2,803.20" },
    { "from": "executed", "to": "executed", "amount": "$0.00" }
  ]
}
```

`SavingsRecord.MarkLost` is called at the point the rollback is confirmed,
with the reason stated in the record itself — not deleted from the funnel,
not quietly excluded from a report. `docs/adr/0009-six-stage-savings-lifecycle.md`
is explicit that an honestly-lost saving is exactly the kind of entry this
funnel exists to surface, and this scenario is the concrete instance that
ADR's abstract argument is about.

## 9. What this scenario is meant to demonstrate

Three things, together:

1. **The rule engine's confidence score was not wrong to be lower here
   (0.71) than in the EC2 scenario (0.86).** A wider P50/P95 utilization
   spread is exactly the signal that should lower confidence, and it did —
   this recommendation was flagged as less certain than the EC2 one before
   anything executed, not only in hindsight.
2. **Deterministic risk and blast-radius scoring correctly identified this
   as higher-consequence** (`risk.level: medium`, `blast_radius.level:
   high`) than the EC2 case, which is why it required a human approval with
   that context visible, not an automated pass.
3. **The four-phase discipline did its job.** The prediction was wrong —
   CPU utilization alone did not capture the connection-handling behavior
   that mattered — and the platform caught this itself, during the
   validation window it is specifically designed to require before a saving
   is ever counted as real, and reversed the change without a human needing
   to notice the regression first. This is what "the rollback plan exists
   before the forward mutation runs" is actually for.
