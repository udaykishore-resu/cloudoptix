---
title: AWS Pricing Models and Cost Mechanics
source: aws_pricing
---

# AWS Pricing Models and Cost Mechanics

AWS bills for almost everything on one of a small number of underlying
mechanics. Recognising which one applies to a resource is the first step in
any optimization: you cannot cut a cost you have mis-modelled.

## On-Demand

Pay per second (most compute) or per hour (some managed services) with no
commitment. This is the default and the most expensive steady-state option.
EC2, RDS, ElastiCache and Redshift all bill on-demand by the hour with
per-second granularity for Linux instances. A stopped EC2 instance stops
billing for compute but its attached EBS volumes keep billing — a very common
source of "why is this account still costing money" confusion.

## Reserved Instances (RIs) and Savings Plans

Both trade a 1- or 3-year commitment for a discount (typically 30-72% versus
on-demand). The distinction that matters for optimization:

- **Standard RIs** are tied to instance family, region and (optionally)
  availability zone. They are the least flexible but cheapest.
- **Convertible RIs** may be exchanged for a different instance family during
  the term, at a lower discount than Standard.
- **Compute Savings Plans** commit to an hourly *dollar* spend, not an
  instance type, and apply automatically across EC2, Fargate and Lambda
  regardless of family, size or region. They are the right default
  recommendation for any estate whose instance mix is expected to change.
- **EC2 Instance Savings Plans** commit to a dollar spend within one instance
  family in one region, in exchange for a deeper discount than Compute
  Savings Plans.

Coverage below roughly 70% of steady-state baseline usually means real
uncommitted spend sitting on the table; coverage pushed to 100% removes all
elasticity and starts paying for capacity that isn't running.

## Spot Instances

Spare EC2 capacity at up to 90% off on-demand, reclaimable by AWS with a two
minute warning. Appropriate for stateless, fault-tolerant, checkpointable or
queue-driven workloads — batch processing, CI runners, stateless web tiers
behind an ASG with mixed instance policies, and Spark/EMR executors. Never
appropriate for anything that cannot tolerate interruption without a
correctness or availability impact: a single-instance production database, a
stateful in-memory cache with no replication, or anything inside a
change-freeze window.

## Serverless and Consumption-Based Pricing

Lambda, DynamoDB on-demand, S3, SQS, API Gateway and Aurora Serverless bill
per invocation, per request, per GB-second, or per read/write-capacity-unit
rather than per provisioned hour. There is no "idle" cost in the traditional
sense, but there are two traps: (1) over-provisioned memory on Lambda
increases the GB-second rate even when CPU is the real bottleneck, because
Lambda allocates CPU proportionally to configured memory; (2) DynamoDB
on-demand is usually more expensive than well-provisioned capacity mode once
traffic is predictable, because on-demand carries roughly a 5-7x per-request
premium in exchange for not having to plan capacity.

## Storage Pricing Mechanics

EBS bills for provisioned size regardless of used space (gp3 also separately
for provisioned IOPS/throughput above the free baseline), so an
over-provisioned but empty volume costs the same as a full one. S3 has seven
storage classes with materially different per-GB prices and, critically,
different **retrieval costs and minimum storage durations**: moving
infrequently-read data to Standard-IA or Glacier only saves money if the
access pattern genuinely matches the class's assumptions, and early deletion
before the minimum duration triggers a penalty charge that can exceed the
saving.

## Data Transfer

Data transfer *into* AWS is free. Transfer *out* to the internet is billed
per GB and is one of the largest, least-visible line items in a bill.
Transfer *between* AWS regions is billed both ways. Transfer *between*
availability zones in the same region is billed both ways at a lower rate —
this is the charge behind "cross-AZ chatter," which shows up when a
service's replicas or dependencies are spread across AZs for availability but
talk to each other constantly. A NAT Gateway bills an hourly charge *plus* a
per-GB data processing charge for every byte that passes through it, which is
why high-egress workloads behind a NAT Gateway are a recurring, expensive
architecture smell, and why a VPC Gateway Endpoint for S3/DynamoDB (free) or
Interface Endpoint for other services (hourly + per-GB, but usually far
cheaper than NAT) is one of the highest-leverage architecture changes
available.

## Amortization

A committed cost (an RI or Savings Plan) is billed up front or monthly but
economically belongs to every hour of the commitment term. CloudOptix
amortizes commitments across the hours they cover so that a resource's
attributed cost reflects its true effective rate, not a spike on the day the
commitment was purchased or a cliff on the day it expires.
