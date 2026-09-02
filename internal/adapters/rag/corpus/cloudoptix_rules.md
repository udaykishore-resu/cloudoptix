---
title: CloudOptix Optimization Rules Reference
source: cloudoptix_rules
---

# CloudOptix Optimization Rules Reference

CloudOptix's rule engine evaluates deterministic, evidence-based rules over
the discovered resource model, never over prose. Every rule belongs to one
of ten categories and produces a Finding — a statement of fact with
evidence — that the recommendation builder then costs, risk-assesses and
ranks. This document explains what each rule family means and why it exists,
for the copilot to draw on when explaining a recommendation.

## Rightsizing rules

- **ec2-underutilized-rightsize**: an EC2 instance whose P95 and P99 CPU and
  memory both show sustained headroom (CPU P95 < 40%, P99 < 55%; memory P95
  < 55%, P99 < 70%, minimum 50% telemetry coverage over at least a 7-day
  window) is a candidate to step down one rung on its instance-family
  ladder. Percentiles, never the mean, so a nightly-batch spike is never
  missed.
- **ec2-oversized-declared-workload**: flags an instance far larger than its
  *declared* workload type typically needs, using the onboarding
  specification rather than telemetry — useful before an instance has
  accumulated enough metric history to be judged on utilisation alone.
- **ec2-burstable-credit-exhaustion**: a t-family instance running on
  "unlimited" burst mode whose surplus credit charges exceed what a
  fixed-size instance would have cost outright.
- **rds-oversized-instance**: the RDS equivalent of EC2 rightsizing, but
  weighted more conservatively because RDS storage changes are only
  one-directional in the AWS API (increase-only), which lowers
  reversibility.
- **eks-nodegroup-overprovisioned** / **k8s-pod-requests-oversized**: node
  groups sized for a pod-packing density the actual pod resource *requests*
  never approach — the reclaimable node count is computed from aggregate
  requested-vs-allocatable capacity, not from node-level CPU alone, because
  Kubernetes bin-packs on requests.
- **lambda-memory-cost-curve**: because Lambda's billed GB-seconds move with
  configured memory *and* CPU is allocated proportionally to memory, there
  is a genuine cost-optimal memory setting that is not always the smallest
  one — this rule walks the cost curve rather than simply recommending the
  minimum.

## Waste rules

- **ec2-never-used-instance** / **ec2-stopped-still-billing-storage**:
  resources with zero or near-zero observed utilisation, or storage that
  keeps billing after its compute was stopped.
- **ebs-unattached-volume**, **elastic-ip-unattached**,
  **ebs-orphaned-snapshot**, **ebs-unused-ami**, **load-balancer-idle**,
  **kms-secrets-unused**, **lambda-unused-provisioned-concurrency**: the
  classic "still billing, nothing using it" family. These are the
  highest-confidence, lowest-risk recommendations CloudOptix produces and
  are usually the first candidates for policy auto-execution.

## Storage rules

- **ebs-gp2-to-gp3** / **rds-gp2-to-gp3**: gp3 is priced independently of
  IOPS and throughput up to a generous free baseline and is strictly cheaper
  than gp2 for the overwhelming majority of workloads at equivalent
  performance — a near risk-free migration.
- **s3-wrong-storage-class** / **s3-no-lifecycle-policy** /
  **s3-intelligent-tiering-candidacy**: match S3 storage class to the
  object's actual access pattern; Intelligent-Tiering candidacy is evaluated
  against its own monitoring-and-automation charge, because for
  small-object-heavy buckets that charge can exceed the storage saving.
- **s3-noncurrent-versions** / **s3-incomplete-multipart-uploads**: cost
  accumulating silently from bucket features (versioning, multipart
  uploads) with no expiry policy attached.

## Commitment rules

- **ec2-commitment-coverage-gap**: steady-state on-demand spend with no
  Savings Plan or RI coverage — the highest-value, lowest-complexity
  optimization available once a baseline is stable, evaluated against the
  *trailing minimum*, never the average or peak.
- **ec2-spot-candidacy**: workloads matching the interruption-tolerant
  profile (stateless, part of an ASG or job queue, no single point of
  failure) that are still running entirely on-demand.
- **dynamodb-billing-mode-mismatch**: on-demand DynamoDB tables with
  predictable, steady traffic, where provisioned capacity (with
  auto-scaling) would be materially cheaper.

## Network rules

- **nat-gateway-vpc-endpoint-opportunity**: NAT Gateway data-processing
  charges dominated by traffic to S3 or DynamoDB, which a free Gateway
  Endpoint would eliminate entirely.
- **nat-gateway-redundant** / **cross-az-chatter**: redundant per-AZ NAT
  spend, and inter-component chatter that crosses availability zones for no
  architectural reason, both billed as cross-AZ data transfer.
- **cloudfront-vs-direct-egress**: cases where a CDN's own cost, plus its
  discounted internet egress rate, would undercut serving traffic directly.

## Scheduling, observability, licensing and data-lifecycle rules

- **ec2-schedule-off-hours**: non-production resources idle outside
  business hours are candidates for a start/stop schedule rather than
  running 24/7 — advisory in production, commonly auto-executable in
  development and staging.
- **cloudwatch-log-retention-unbounded**: log groups with infinite retention
  accumulating storage cost with no compliance reason cited.
- **rds-excessive-backup-retention**, **ebs-snapshot-retention**: retention
  windows kept far longer than any declared policy requires.

## How rules become recommendations

A rule firing produces a **Finding** — evidence plus a plain statement of
fact. The recommendation builder then attaches a costed action, a
confidence score (from metric stability, coverage and the rule's own
historical accuracy), a risk assessment, and a blast radius computed by
walking the architecture graph — never estimated. Only after all of that is
a recommendation ranked and, if policy permits and automation is enabled,
made eligible for execution.
