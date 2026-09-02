# eks

The Kubernetes cluster `helm/cloudoptix` deploys onto: control plane, IRSA
trust anchor, two managed node groups, cluster autoscaling (Karpenter or
Cluster Autoscaler), the aws-load-balancer-controller, and the core EKS
addons.

## Two-apply reality for a brand-new cluster

This module installs `helm_release` and `kubernetes_manifest` resources
(the aws-load-balancer-controller, Karpenter and its NodePool/EC2NodeClass,
or Cluster Autoscaler) whose providers must authenticate against a cluster
that does not exist yet on a first `apply`. Terraform can express this
dependency graph, but the `helm`/`kubernetes` **provider configuration**
itself (in the environment composition, not in this module — see below)
needs the cluster's endpoint and CA data, which only exist after
`aws_eks_cluster.this` is created.

In practice: a brand-new environment's first `apply` should either
`-target=module.eks.aws_eks_cluster.this` (and the node groups) first, then
apply again without a target for everything else, or simply accept that the
first full `apply` errors on the helm/kubernetes-provider resources and a
second, identical `apply` succeeds once the cluster exists. This is a
well-known, accepted rough edge of standing up EKS-with-in-cluster-addons
in one Terraform root — not a bug in this module.

## Why this module declares `helm`/`kubernetes` as required providers but never configures them

A reusable child module configuring its own provider blocks is deprecated
Terraform practice (provider passing to modules was removed in 0.13+ for
anything but the root). The **environment composition**
(`terraform/environments/*/providers.tf`) configures both providers using
this module's `cluster_endpoint`, `cluster_certificate_authority_data`
outputs and the `aws_eks_cluster_auth` data source for a short-lived token —
see that directory's own README.

## System vs. Spot node groups

- **system** (On-Demand): anything that must not be interrupted mid-work —
  today, that's the chart's migration Job. Sized small (2–6) because it
  only needs to comfortably run cluster-critical and non-interruptible
  pods.
- **spot** (Spot, tainted `cloudoptix.io/spot=true:NoSchedule`): the
  default home for the chart's stateless, queue-driven workers
  (discovery/optimization/validation/notification — see
  `internal/adapters/events`'s at-least-once, retry-safe delivery model,
  which is exactly what makes a workload Spot-appropriate).
  `spot_instance_types` deliberately lists several similarly-shaped
  instance families rather than one type — Spot capacity pools are keyed
  on (type, AZ), and diversifying is what actually prevents interruption
  storms in practice, not a single "cheap enough" type.

This is CloudOptix eating its own dog food from the other direction:
`terraform/demo` provisions a wastefully over-provisioned, all-On-Demand
node group on purpose, as the pathology the platform's `enable_spot`
recommendation (`internal/adapters/aws/executor`) exists to catch. This
module is what "having taken that recommendation" looks like.

## Karpenter vs. Cluster Autoscaler

`var.autoscaler` picks one (default `karpenter`). See the variable's doc
comment for the trade-off in one paragraph; short version: Karpenter
provisions right-sized EC2 capacity directly and reacts faster, Cluster
Autoscaler only scales the two managed node groups this module already
defines and is the more conservative, easier-to-reason-about choice for a
first production rollout. Both are fully wired (IAM, interruption handling
for Karpenter, autodiscovery tags for Cluster Autoscaler) — switching is a
one-variable change, not a re-architecture.

## aws-load-balancer-controller IAM policy

`policies/aws-load-balancer-controller-iam-policy.json` is vendored
verbatim from AWS's own published policy for this controller rather than
reconstructed action-by-action, because an under-scoped hand-rolled version
fails in a worse way (an ALB silently never reconciles) than a wider
vendored one that matches what upstream actually tests against. Refresh it
periodically from
`https://raw.githubusercontent.com/kubernetes-sigs/aws-load-balancer-controller/main/docs/install/iam_policy.json`
when bumping `aws_load_balancer_controller_version`.
