# terraform/demo — the intentionally wasteful estate

> ## ⚠️ COST WARNING — READ BEFORE APPLYING
>
> This configuration provisions **real, billable AWS resources that are
> deliberately misconfigured to waste money.** It exists so a CloudOptix
> demo/trial account has something real for discovery, cost analysis, and
> the recommendation engine to find — mirroring the in-memory estate
> `internal/adapters/awssim` simulates, but as actual infrastructure a real
> onboarding role can scan.
>
> At the default sizes (every resource is set to the *smallest instance
> type that still exhibits its pathology* — see each resource's comment),
> this estate costs roughly **$180–260/month** if left running, dominated
> by the EKS control plane (~$73/mo, fixed regardless of node size), the
> three NAT gateways (~$96/mo before any data processing), and the RDS
> pair. Nothing here scales with usage in a way that would surprise you —
> there is no production traffic hitting any of it — but every hour it
> runs is an hour of pure waste by design.
>
> **Run `terraform destroy` when you are done with the demo.** This
> directory is not meant to be a long-lived environment; treat it the way
> you'd treat a rented conference-room projector, not a production system.
> `enable_*` variables let you turn off the most expensive pieces
> (EKS in particular) if you only need a subset of pathologies for a
> shorter demo.

## What CloudOptix should find here

| Resource | Pathology | What CloudOptix should recommend |
|---|---|---|
| `aws_instance.oversized_idle` | An instance sized for a workload that never materializes — CPU baseline pinned near-zero by design (see its `user_data`) | `resize_instance` — down two rungs, per `internal/adapters/awssim/waste.go`'s own `rightsizedCost` heuristic |
| `aws_instance.old_generation` | A previous-generation instance type still running | The old-generation successor-type comparison `oldGenerationWaste()` models |
| `aws_ebs_volume.unattached` | Provisioned, never attached to anything | `delete_volume` |
| `aws_ebs_volume.stopped_instance_volume` | Attached only to a permanently-stopped instance | Still billing for storage nobody is reading |
| `aws_ebs_snapshot.stale` | Older than any reasonable retention window, nothing references it | `delete_snapshot` |
| `aws_eip.unattached` | Allocated, never associated | `release_elastic_ip` |
| `aws_s3_bucket.no_lifecycle` | No versioning, no lifecycle rule, no multipart-upload cleanup | `apply_s3_lifecycle` / `abort_multipart_uploads` |
| `aws_db_instance.oversized_primary` + `aws_db_instance.idle_replica` | An oversized primary and a read replica with zero read traffic by construction | `resize_rds` / `remove_rds_replica` |
| `module.network` (`single_nat_gateway=false`, `enable_vpc_endpoints=false`) | Three NAT gateways, S3/DynamoDB traffic paying NAT data-processing rates it doesn't need to | `create_vpc_endpoint`, and — once traffic is small enough — `remove_nat_gateway` |
| `aws_instance.chatty_a` / `chatty_b` | Two instances placed in **different AZs on purpose**, talking to each other constantly | Cross-AZ data transfer that a same-AZ placement (or a Multi-AZ-aware architecture) would avoid |
| `aws_eks_node_group.demo` + `manifests/oversized-pod-requests.yaml` | A Deployment requesting far more CPU/memory than it uses, forcing the node group larger than the cluster's real workload needs | `adjust_pod_resources` / node-group rightsizing |
| `aws_cloudwatch_log_group.infinite_retention` | `retention_in_days` deliberately omitted — CloudWatch's default is **never expire** | `set_log_retention` (see that action's current status in the onboarding-role module's README — it is IAM-ready but not yet executor-implemented) |

## Toggles

```hcl
enable_eks           = true   # ~$73/mo control plane alone — turn off for a shorter/cheaper demo
enable_cross_az_pair = true   # two t3.micro instances, cheap, but real cross-AZ transfer charges
```

## Applying

```sh
cd terraform/demo
terraform init          # local state by default — see versions.tf; this is a disposable environment
terraform apply
```

No `backend` block is configured here on purpose — unlike
`terraform/environments/*`, this is meant to be stood up and torn down
casually, by whoever is running a demo, without needing the shared remote
state bucket from `terraform/bootstrap`. If you do want to track it in
remote state (e.g. a long-running shared demo account), add your own
backend configuration.

## Tearing down

```sh
terraform destroy
```

The RDS instances have `skip_final_snapshot = true` and
`deletion_protection = false` specifically so `destroy` is a single command
with no manual override step — this is disposable infrastructure by
design, unlike `terraform/environments/production`.
