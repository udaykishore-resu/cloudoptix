# Optimization scenarios

Four worked examples of the optimization engine described in
[`docs/optimization-spec.md`](../../docs/optimization-spec.md), each using
real figures produced by running the actual `internal/adapters/awssim`
demo estate ("shopfleet-prod") rather than invented numbers. Every dollar
figure below is reproducible by re-running the same rule evaluation against
that fixture; see the note at the top of each file for how the figure was
derived.

| File | What it shows |
|---|---|
| [`ec2-rightsizing.md`](ec2-rightsizing.md) | A single, small, representative example end to end: finding → recommendation → approval → execution → validation → realization |
| [`nat-vpc-endpoint-elimination.md`](nat-vpc-endpoint-elimination.md) | The largest network-waste category in the demo estate: $12,348.00/month of NAT gateway data processing that a VPC endpoint would eliminate |
| [`eks-pod-request-reclaim.md`](eks-pod-request-reclaim.md) | The single largest waste category in the demo estate: $20,034.12/month of EKS pod/node overprovisioning |
| [`failed-optimization-rollback.md`](failed-optimization-rollback.md) | The one scenario in this set that goes wrong: an RDS downsize whose validation window catches a real latency regression, and the platform's automatic rollback |

All four correspond to figures already cited in the root
[`README.md`](../../README.md#example-optimization-workflow-end-to-end) and,
for the rollback scenario, in
[`docs/runbooks/failed-rollback.md`](../../docs/runbooks/failed-rollback.md).
