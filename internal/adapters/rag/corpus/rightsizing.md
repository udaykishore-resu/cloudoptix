---
title: Right-Sizing and Commitment Guidance
source: rightsizing
---

# Right-Sizing and Commitment Guidance

## Right-size on percentiles, never on averages

An instance averaging 8% CPU that spikes to 95% at P99 during a nightly batch
job is not a downsizing candidate — a naive tool that reasons on the mean
would recommend shrinking it and cause an outage on the next batch run.
CloudOptix reasons on P95/P99 utilisation over a full business cycle
(minimum 14 days, ideally 30, so weekly patterns are captured), and requires
a minimum data coverage threshold before proposing a change at all. A
resource with 12% telemetry coverage over the window is a monitoring gap, not
evidence of low utilisation.

## The right-sizing decision sequence

1. **Coverage check** — is there enough utilisation history to trust a
   percentile at all?
2. **Stability check** — is the distribution steady, spiky, seasonal, or
   trending? A steady-state resource can be resized with high confidence; a
   seasonal one needs its peak season included in the window; a trending one
   (usage climbing week over week) should not be downsized based on
   yesterday's headroom.
3. **Headroom calculation** — how much capacity above the observed P99 would
   the *proposed* size still provide? A good default is to leave at least
   25-40% headroom above P99 for burst absorption, wider for anything on the
   customer-facing request path.
4. **Family and generation** — moving within a family (e.g. m5.4xlarge to
   m5.2xlarge) is a pure right-size. Moving to a newer generation of the same
   family (m5 to m6i/m7i, or x86 to Graviton/arm64) typically adds a further
   10-20% price-performance improvement independent of sizing, but requires
   the workload to support the target architecture — Graviton needs an
   arm64-compatible build, which is a code and pipeline change, not a
   one-click resize.

## Commitment sizing

Commit against the **trailing minimum steady-state baseline**, not the
average and never the peak. The correct order of operations for a maturing
estate is: right-size first, *then* commit — committing to 3 years of an
oversized instance family locks in the waste for the life of the commitment.
A blended approach that most FinOps practices converge on: cover the
predictable floor with 1-year Compute Savings Plans (flexible, moderate
discount), and only commit to Standard RIs or 3-year terms for capacity that
has been stable for a long observed period and is unlikely to change
architecture.

## Common rightsizing anti-patterns

- Downsizing a database primary based on average CPU while ignoring
  connection count, IOPS or memory pressure, any one of which can be the
  real constraint.
- Resizing an Auto Scaling Group's launch template without checking whether
  the *minimum* count, not just the instance size, is oversized for the
  observed floor.
- Treating a burstable instance family (t3/t4g) as "low utilisation" without
  checking CPU credit balance — a t-family instance intentionally runs low
  average CPU and bursts using accumulated credits; the metric that matters
  is credit balance trending toward zero, not average CPU.
- Rightsizing Lambda memory down without checking duration: because Lambda
  allocates CPU proportional to memory, a memory decrease can increase
  duration enough that the GB-second cost goes *up* even though the
  per-request memory allocation went down.

## Reversibility as a first-class input

Every right-sizing action carries a reversibility rating. Resizing an EC2
instance is fast (stop, modify, start — a few minutes of downtime, instantly
undoable). Resizing RDS storage is one-directional in the AWS API — storage
can be increased but not decreased without a full migration, so it is
classified as low reversibility and weighted accordingly in the priority
formula even when the confidence is high.
