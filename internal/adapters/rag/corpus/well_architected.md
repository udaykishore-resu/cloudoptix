---
title: Well-Architected Cost Optimization Practice
source: well_architected
---

# AWS Well-Architected Framework — Cost Optimization Pillar

The Well-Architected Framework's Cost Optimization pillar rests on five
design principles that CloudOptix's rule engine and recommendation ranking
are built to operationalise rather than merely cite.

## 1. Implement Cloud Financial Management

Treat cost as a discipline with dedicated capability, not an afterthought.
In practice: cost visibility per team, forecasting, and a feedback loop from
spend back to the teams that generate it. CloudOptix's economic footprint and
efficiency score exist to make this measurable rather than aspirational.

## 2. Adopt a consumption model

Pay only for the computing resources actually required, scaling to meet
business needs and turning off what is not needed. The consumption model is
undermined by three common failures: resources left running after their
purpose ends (waste), resources provisioned for peak but never scaled down
(over-provisioning), and manual scaling processes that lag actual demand
(latency between need and supply).

## 3. Measure overall efficiency

Efficiency is output delivered per dollar spent — business value over cost —
not cost minimisation in isolation. A workload that costs more in absolute
terms but serves proportionally more transactions can be *more* efficient
than a cheaper one serving fewer. This is exactly the reasoning behind
tracking cost-per-transaction as a trend rather than judging cost snapshots
alone.

## 4. Stop spending money on undifferentiated heavy lifting

AWS-managed services (RDS instead of self-managed databases on EC2, Fargate
instead of self-managed cluster capacity, managed Kafka instead of
self-hosted) usually cost more per unit of raw compute but eliminate
operational burden that has a real, if less visible, cost. The trade-off is
legitimate to make either way; the failure mode Well-Architected calls out is
making it unconsciously, by inertia rather than analysis.

## 5. Analyze and attribute expenditure

The framework calls for accurate attribution of cost to the workload,
team or business unit that generated it, with increasing granularity as
maturity increases: account-level, then tagging-based, then resource-level
attribution with a documented model for shared and indirect costs. An
attribution model that silently distributes shared cost evenly across
teams, rather than by measured consumption, systematically misprices every
team's real footprint — this is why CloudOptix's economics engine tracks
Direct/Indirect/Shared cost classes separately with an explicit unattributed
remainder, rather than a single blended number.

## Cost Optimization design principles applied to specific decisions

- **Choose the right resource type and size** based on workload
  characteristics measured over time, not point-in-time snapshots or
  provisioning defaults.
- **Select the right pricing model** — on-demand for unpredictable or
  short-lived usage, Savings Plans/RIs for predictable steady-state, Spot for
  interruption-tolerant workloads.
- **Match supply with demand** using auto-scaling, scheduled scaling for
  known patterns (business-hours-only environments), and serverless where
  the workload's request pattern makes idle capacity mostly avoidable.
- **Optimize over time.** AWS releases new instance types and pricing
  options continuously; a right-sizing decision made a year ago is not
  guaranteed to still be optimal, which is why continuous re-evaluation
  rather than one-time review is a design principle, not an operational
  nicety.

## Relationship to reliability and security pillars

Cost Optimization is explicitly not pursued in isolation: the framework
requires trade-offs against the Reliability, Performance Efficiency and
Security pillars to be conscious and documented. CloudOptix encodes this
structurally — an optimization that improves cost but degrades an
availability target the tenant declared is filtered from auto-execution and
flagged for review, and every recommendation records the reliability and
security risk it introduces alongside its saving.
