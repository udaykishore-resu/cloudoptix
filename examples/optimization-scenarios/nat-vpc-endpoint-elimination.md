# Optimization scenario: NAT gateway / VPC endpoint elimination

The largest network-waste category in the `shopfleet-prod` demo estate:
three NAT gateways processing traffic — most of it S3-bound — that a
gateway VPC endpoint would route for free. Figures reproduced from the
demo estate's actual discovered topology and CloudWatch metrics; also cited
in the root [`README.md`](../../README.md#example-optimization-workflow-end-to-end).

## 1. Discovery: the three NAT gateways

| NAT gateway | Region/AZ | Monthly cost |
|---|---|---|
| `nat-us-east-1a` | us-east-1a | $5,657.85 |
| `nat-us-east-1b` | us-east-1b | $5,432.85 |
| `nat-us-east-1c` | us-east-1c | $4,442.85 |
| **Total** | | **$15,533.55** |

## 2. Traffic composition

VPC Flow Logs ingested by discovery classify destination IP ranges against
AWS's published S3 prefix list for the region. Across all three gateways,
80% of processed bytes are destined for S3 endpoints — traffic that a
**gateway VPC endpoint for S3** (a free, route-table-level construct, not a
metered interface endpoint) would route directly, bypassing the NAT
gateway's per-GB data processing charge entirely.

## 3. Finding

```json
{
  "rule_id": "network.nat.no-vpc-endpoint-for-s3-traffic",
  "rule_name": "NAT gateway processing S3-bound traffic with no VPC endpoint present",
  "category": "waste",
  "resource_name": "nat-us-east-1a, nat-us-east-1b, nat-us-east-1c",
  "resource_kind": "nat_gateway",
  "account_id": "shopfleet-prod",
  "region": "us-east-1",
  "environment": "production",
  "severity": "high",
  "summary": "80% of NAT-processed bytes across 3 gateways are S3-bound; no S3 gateway VPC endpoint exists in this VPC",
  "detail": "A gateway VPC endpoint for S3 is a free, route-table-level construct — it eliminates NAT data-processing charges for S3 traffic entirely rather than reducing them. The remaining 20% of traffic (non-S3 destinations) will continue to require the NAT gateways.",
  "evidence": [
    { "label": "s3_traffic_share", "value": "80%", "window": "30d", "source": "discovery" },
    { "label": "nat_data_processing_charge_rate", "value": "$0.045/GB", "source": "cost_explorer" },
    { "label": "vpc_endpoints_present", "value": "none (S3 gateway type)", "source": "discovery" }
  ],
  "current_monthly_cost": "$15,533.55",
  "estimated_monthly_saving": "$12,348.00"
}
```

The $12,348.00 figure is the data-processing-charge component attributable
to the S3-bound 80% share across all three gateways — not 80% of the full
$15,533.55 gateway cost, since the base hourly charge for each NAT gateway
(a fixed cost independent of traffic) is unaffected by adding an endpoint;
only the metered data-processing portion goes away.

## 4. Recommendation

```json
{
  "title": "Add an S3 gateway VPC endpoint to eliminate NAT data-processing charges for S3 traffic",
  "action": "create_vpc_endpoint",
  "rationale": "80% of NAT-processed bytes across all three AZ gateways are S3-bound. A gateway VPC endpoint for S3 costs nothing to operate and removes this traffic from the NAT data-processing meter entirely, without removing the NAT gateways themselves (still required for the remaining 20% of non-S3 outbound traffic).",
  "estimated_monthly_saving": "$12,348.00",
  "estimated_annual_saving": "$148,176.00",
  "confidence": 0.91,
  "confidence_basis": [
    { "name": "flow_log_sample_completeness", "value": 0.97, "weight": 0.5, "explanation": "30-day continuous Flow Logs coverage across all three gateways" },
    { "name": "destination_classification_confidence", "value": 0.93, "weight": 0.3, "explanation": "classified against AWS's published, versioned S3 IP prefix list for the region" },
    { "name": "endpoint_absence_confirmed", "value": 1.0, "weight": 0.2, "explanation": "discovery directly confirms no S3 gateway endpoint exists in the VPC's route tables" }
  ],
  "risk": {
    "score": 0.08,
    "level": "low",
    "availability_risk": "low",
    "performance_risk": "low",
    "data_loss_risk": "none",
    "factors": [
      { "name": "additive_change", "contribution": -0.20, "explanation": "adding a gateway VPC endpoint does not remove or modify any existing resource; it is purely additive to the route table" }
    ],
    "mitigations": ["route table changes are reviewed for unintended route-priority conflicts before application", "no NAT gateway is removed or resized by this change"]
  },
  "blast_radius": {
    "resources_affected": 3,
    "services_affected": 6,
    "critical_services": 2,
    "environments_affected": ["production"],
    "cross_account": false,
    "score": 0.18,
    "level": "low",
    "completeness": 1.0,
    "explanation": "affects every service in the VPC whose traffic is currently NAT-routed to S3, but only by removing metered cost from that specific path — no service loses connectivity or has its route path degraded"
  },
  "reversibility": "fast",
  "complexity": "trivial",
  "requires_approval": true,
  "auto_executable": false
}
```

Despite `complexity: trivial` and `risk.score: 0.08`, this still requires
approval because it touches production networking shared across every
service in the VPC, not because of the rule engine's own risk math alone —
the same production-approval invariant as the EC2 rightsizing scenario.

## 5. Execution and validation

Unlike the EC2 rightsizing example, there is nothing to roll back to in the
usual sense — the rollback plan here is simply "remove the route table
entry," which is itself trivial and fast (`Reversibility: fast`,
`RollbackPlan.Feasible: true`). The validation window observes NAT
data-processing charges in the next Cost Explorer pull rather than a
latency SLO, since this change has no latency-relevant surface — the
validator here checks the *cost claim*, not a performance claim.

## 6. Realization

The following billing cycle's Cost Explorer pull shows NAT data-processing
charges for the affected gateways reduced consistent with the 80% S3-traffic
share. `SavingsRecord` advances to `Realized` once that reduction is
confirmed against real billing data, exactly as in the EC2 scenario — no
optimization category in CloudOptix skips the "wait for the actual bill"
step, regardless of how mechanically certain the saving looks in advance.
