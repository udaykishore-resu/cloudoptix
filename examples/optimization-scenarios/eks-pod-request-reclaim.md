# Optimization scenario: EKS pod/node request reclaim

The single largest waste category identified anywhere in the
`shopfleet-prod` demo estate: $20,034.12/month across two EKS node groups
packed well below their allocated capacity, because pod resource
`requests` were set far above what the workloads actually consume. Also
cited in the root [`README.md`](../../README.md#example-optimization-workflow-end-to-end).

## 1. Discovery: node group packing

| Node group | Instance type | Node count | Allocated CPU | Actual P50 CPU utilization |
|---|---|---|---|---|
| `eks-shopfleet-general` | `m5.2xlarge` | 14 | 8 vCPU/node (112 total) | 38% |
| `eks-shopfleet-workers` | `m5.xlarge` | 22 | 4 vCPU/node (88 total) | 35% |

Both node groups are sized by the Kubernetes scheduler's bin-packing
against **pod resource requests**, not actual usage — a node group at 35–40%
CPU utilization with no free capacity to schedule additional pods onto is
the specific signature this rule looks for: it means requests, not actual
load, are the binding constraint on cluster size.

## 2. Finding

```json
{
  "rule_id": "kubernetes.eks.pod-requests-overprovisioned",
  "rule_name": "EKS pod resource requests set well above observed usage, driving unnecessary node count",
  "category": "waste",
  "resource_name": "eks-shopfleet-general, eks-shopfleet-workers",
  "resource_kind": "eks_node_group",
  "account_id": "shopfleet-prod",
  "region": "us-east-1",
  "environment": "production",
  "severity": "high",
  "summary": "Two node groups packed at 35-40% CPU utilization against their allocated (requested) capacity, both effectively full for scheduling purposes",
  "detail": "Pod resource requests across the workloads in both node groups average roughly 2.6x their measured usage. The scheduler cannot pack additional pods onto either node group despite substantial idle CPU, because requests — not usage — are what the scheduler allocates against. Reducing requests to a level with headroom above observed P95 usage allows both node groups to be scaled down.",
  "evidence": [
    { "label": "general_node_group_cpu_p50", "value": "38%", "window": "30d", "source": "prometheus" },
    { "label": "workers_node_group_cpu_p50", "value": "35%", "window": "30d", "source": "prometheus" },
    { "label": "avg_request_to_usage_ratio", "value": "2.6x", "window": "30d", "source": "prometheus" },
    { "label": "schedulable_headroom", "value": "0 pods (both node groups full against requests)", "source": "discovery" }
  ],
  "current_monthly_cost": "$52,415.00",
  "estimated_monthly_saving": "$20,034.12"
}
```

## 3. Recommendation

This recommendation has a materially different shape from the two prior
scenarios: it does not propose a single infrastructure mutation, but a
**two-part change** — pod resource request adjustments (an application-layer
change, in the workloads' own Helm values or manifests) paired with a node
group scale-down (an infrastructure-layer change CloudOptix can execute
directly). CloudOptix's executors can apply the node group scale-down; the
request adjustment itself is surfaced as a specific, reviewable diff against
each workload's declared requests, for the owning team to apply through
their own deployment pipeline — this is a deliberate boundary, not a gap:
CloudOptix does not push arbitrary changes into a customer's own
application manifests.

```json
{
  "title": "Right-size pod resource requests and scale down two EKS node groups accordingly",
  "action": "recommend_manifest_change_and_scale_node_group",
  "rationale": "Both node groups are scheduler-full against requested (not actual) capacity. Adjusting requests to sit with headroom above observed P95 usage, then scaling the node groups down proportionally, recovers the difference without reducing actual available capacity for the workloads' real usage pattern.",
  "current_state": { "count": 36, "vcpu": 200, "monthly_cost": "$52,415.00" },
  "proposed_state": { "count": 21, "vcpu": 118, "monthly_cost": "$32,380.88" },
  "estimated_monthly_saving": "$20,034.12",
  "estimated_annual_saving": "$240,409.44",
  "confidence": 0.74,
  "confidence_basis": [
    { "name": "utilization_sample_size", "value": 0.90, "weight": 0.35, "explanation": "30 days of Prometheus samples across both node groups" },
    { "name": "manifest_dependency", "value": 0.55, "weight": 0.4, "explanation": "the full saving depends on the owning teams actually applying the recommended request changes through their own pipeline; CloudOptix can only execute the node-group half directly, which lowers confidence relative to a fully self-contained change" },
    { "name": "workload_diversity", "value": 0.80, "weight": 0.25, "explanation": "the 2.6x ratio is an average across a heterogeneous set of workloads with some individual variance" }
  ],
  "risk": {
    "score": 0.41,
    "level": "medium",
    "availability_risk": "medium",
    "performance_risk": "medium",
    "data_loss_risk": "none",
    "factors": [
      { "name": "capacity_reduction", "contribution": 0.25, "explanation": "reduces total cluster CPU capacity; a traffic spike exceeding the new, tighter headroom before requests are actually right-sized could cause pod evictions" },
      { "name": "cross_team_dependency", "contribution": 0.16, "explanation": "the node-group scale-down and the manifest changes are not atomic; a scale-down applied before the corresponding requests are lowered would reduce actual available capacity" }
    ],
    "mitigations": ["node group scale-down is sequenced to apply only after discovery confirms the manifest changes have been deployed", "scale-down proceeds in increments, re-validating schedulable headroom after each step rather than in one jump"]
  },
  "blast_radius": {
    "resources_affected": 36,
    "services_affected": 11,
    "critical_services": 3,
    "environments_affected": ["production"],
    "cross_account": false,
    "score": 0.52,
    "level": "medium",
    "completeness": 0.95,
    "explanation": "affects every pod scheduled onto either node group; completeness is 0.95 rather than 1.0 because a small number of pods in these node groups belong to workloads the twin has only INFERRED ownership for via tag-pattern matching, not confirmed discovery"
  },
  "reversibility": "moderate",
  "complexity": "high",
  "requires_approval": true,
  "auto_executable": false
}
```

`complexity: high` (not `medium` or `trivial`, unlike the previous two
scenarios) because this change requires coordinating with the owning
teams' own deployment pipelines, not just an infrastructure API call — the
priority formula's complexity factor (`Complexity.Factor()`) discounts this
recommendation's priority score accordingly relative to a same-sized, purely
infrastructural saving.

## 4. Why this one is sequenced differently

`execute.Plan` for the node-group portion of this recommendation includes a
**precondition step that checks actual observed pod CPU usage against the
new, lower request values** before scaling down — not merely a check that
the manifest was deployed. If the owning teams apply less aggressive request
reductions than recommended (a team electing partial adoption, which is
allowed — recommendations are not mandates), the node-group scale-down step
recomputes its own target size against what was actually deployed rather
than blindly applying the original $20,034.12 figure's implied node count.
The realized saving in this scenario can therefore differ materially from
the initial estimate, and does — `docs/optimization-spec.md` documents this
category of recommendation explicitly as the one where "estimated" and
"realized" savings are expected to diverge more than in a single-resource
change.

## 5. Realized outcome (illustrative)

In this scenario, three of the eleven affected workload teams applied the
full recommended request reduction within the first maintenance window;
five applied a partial reduction; three deferred entirely, citing an
upcoming load test they wanted to run first. The node-group scale-down
executed in three increments over three weeks, each gated on the
precondition check above. Realized saving after all three increments:
**$14,220.60/month** — 71% of the original $20,034.12 estimate, with the
`SavingsRecord`'s stage-transition history explicitly recording the gap and
its cause (partial manifest adoption) rather than silently reporting the
original estimate as realized.
