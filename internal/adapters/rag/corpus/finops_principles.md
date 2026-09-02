---
title: FinOps Foundation Principles
source: finops
---

# FinOps Foundation Principles

The FinOps Foundation defines FinOps as an operating model for cloud
financial management, built on six principles and three iterative phases.
CloudOptix's product structure follows them directly.

## The six principles

1. **Teams need to collaborate.** Finance, engineering and business
   stakeholders share ownership of cloud cost decisions; cost is not solely
   a finance concern bolted on after the fact. This is why CloudOptix
   attributes cost to applications and workloads that engineering owns, not
   only to the account structure finance sees.
2. **Everyone takes ownership of their cloud usage.** Decentralised
   accountability, not a central team policing spend. The economic footprint
   and cost-per-transaction metrics exist so an engineering team can see the
   cost consequence of its own decisions directly, without waiting for a
   monthly finance report.
3. **A centralized team drives FinOps.** Decentralised ownership still needs
   a central function that sets standards, negotiates commitments, and
   maintains the tooling. In CloudOptix terms: the tenant's policy and
   governance configuration is the centralized team's lever.
4. **Reports should be accessible and timely.** Decisions require near
   real-time data, not a report that arrives four weeks after the spend
   happened. This is the reason CloudOptix separates "last ingested" and
   "freshness" from every cost figure it shows.
5. **A centralized team drives FinOps decisions via business value.**
   Optimization decisions are judged against business value delivered per
   dollar, not spend minimisation in isolation — cutting cost that also cuts
   revenue capacity is not a win. This is the reasoning behind unit
   economics (cost per transaction) as a first-class metric alongside
   absolute spend.
6. **Take advantage of the variable cost model of the cloud.** The cloud's
   elasticity is the opportunity FinOps exists to capture: pay only for what
   is used, scale down as well as up, and treat commitment as a deliberate
   trade of flexibility for discount rather than a default.

## Crawl, Walk, Run

FinOps maturity is iterative, not a project with an end date. A "Crawl"
organisation has basic visibility and tagging; "Walk" adds automated
anomaly detection, showback/chargeback and rightsizing; "Run" has
automated optimization within policy, unit economics tracked as a KPI, and
cost SLOs with error budgets treated the same way reliability SLOs are.
CloudOptix's onboarding conversation is deliberately structured so a Crawl
customer gets useful advisory output on day one, while the platform's
governance and automation features let the same customer grow into Run
without switching tools.

## Inform, Optimize, Operate

The three FinOps phases repeat continuously:

- **Inform**: visibility, allocation, benchmarking — you cannot optimize
  what you cannot see or attribute.
- **Optimize**: rate optimization (commitments, pricing) and usage
  optimization (rightsizing, waste elimination, architecture).
- **Operate**: continuous improvement, governance, and automation —
  making the optimized state the durable state rather than a one-time
  clean-up.

## Waste versus commitment versus architecture

A mature FinOps practice separates three distinct levers, because they have
different owners, different risk profiles and different time horizons:

- **Waste elimination** (unattached volumes, idle resources, orphaned
  snapshots) is low-risk, fast, and should be continuously automated once
  trust is established.
- **Rate optimization** (commitments, pricing tier choice) is a finance-led
  decision made on a stable, understood baseline.
- **Architecture optimization** (moving to serverless, adding a cache,
  changing a database engine) is the highest-leverage but highest-effort
  lever, usually requiring engineering time and a project, not a policy
  toggle.
