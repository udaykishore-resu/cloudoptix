# Architecture Economics specification

Covers `SPEC-ECON-001..005` (`internal/domain/econ`, `internal/application/economics`) and `SPEC-TWIN-001..003` (`internal/application/twin`) — grouped in one document because the twin is, mechanically, the rendering of the same attribution model this document defines.

## SPEC-ECON-001 — The footprint model

A billing tool answers "Amazon RDS cost $24,500 last month." Architecture Economics answers "the checkout capability cost $61,200 last month, of which $24,500 was its database, $8,700 was NAT egress it caused, and $4,100 was its measured share of the shared observability platform — $0.0061 per checkout, up 14% because the p95 basket size grew, not because anything got more expensive per unit." See the root README's [full worked example](../README.md#worked-example-the-checkout-capability), including the demo estate's own verified component costs.

Three ideas make this possible:

1. **Cost is attributed along the architecture graph, not the tag on the invoice line.** Shared components split by measured consumption; egress charges follow the workload that caused the traffic.
2. **Every attributed figure records its provenance and the share of the estate that could not be attributed.** An unattributed remainder is always shown, never hidden by an even split.
3. **Business denominators — transactions, customers, requests — are first-class**, a stored and versioned object (`spec.TransactionSpec`), not a spreadsheet someone maintains outside the platform.

`econ.Scope` spans nine levels with the same algorithm applied at each: `organization, account, environment, application, workload, business_capability, api, transaction, resource`.

## SPEC-ECON-002 — Direct / indirect / shared classification and the Consumers-graph split

`econ.CostClass`:

| Class | Meaning |
|---|---|
| `direct` | Spend on resources the workload exclusively owns — its own instances, its own database, its own volumes |
| `indirect` | Spend the workload demonstrably caused on a resource it does not own, with exactly one consumer — NAT data processing for its own egress, cross-AZ transfer between its own replicas, LCU consumption driven by its own traffic |
| `shared` | Spend on a resource with more than one measured consumer — an observability platform, a shared cache, a shared control plane |

The attribution algorithm's key move is reusing `cloud.Topology.Consumers` as the single source of truth for splitting cost that touches more than one owner, rather than reimplementing that arithmetic. `Consumers` already normalizes a shared component's inbound edges into per-consumer shares summing to one; the economics engine walks every resource with recorded consumers and, for each one that is this scope's own, books that resource's `MonthlyCost` times its measured share as `ClassIndirect` (a single consumer) or `ClassShared` (multiple consumers).

## SPEC-ECON-003 — Unattributed remainder, and why it is never guessed at

A resource this scope structurally depends on (a `depends_on`/`routes_to` edge) but for which no consumer edge was ever recorded is **not** guessed at: its cost is added to the scope's `Unattributed` remainder, fully visible, rather than silently divided evenly across however many scopes happen to touch it. An even split would manufacture false precision — telling a team "your database costs you $340/month" when the true number could be $50 or $900 depending on a traffic pattern nobody measured — and a wrong number that looks confident is worse for a FinOps decision than an honest "we don't know yet, here's what's unclear."

## SPEC-ECON-003 (Cost SLOs) and SPEC-ECON-004 (Economic Error Budget)

`econ.CostSLO` (`internal/domain/econ/slo.go`) applies error-budget discipline — the same discipline reliability engineering applies to availability — to spend:

| `SLOKind` | Example |
|---|---|
| `absolute_spend` | Production infrastructure ≤ $100K/month |
| `cost_per_transaction` | Checkout ≤ $0.02/transaction |
| `cost_per_request` | An API's per-call cost ceiling |
| `cost_per_customer` | Cost-to-serve |
| `waste_ratio` | Waste as a share of spend, ceiling |
| `efficiency_score` | Cloud Efficiency Score, floor |

`EvaluateBudget(slo, actual, now)` (`internal/domain/econ/slo.go:188`) is the pure function at the centre of this spec:

1. Resolve the window (`SLOWindow`: calendar month, rolling 7d/30d, calendar quarter) to a concrete `core.Period`.
2. Compute `elapsed`, the fraction of the window that has passed.
3. Compute a **pro-rated target** (`slo.Target.Scale(elapsed)`) — comparing month-to-date spend against a *full-month* target would report every SLO healthy until the 28th, so consumption is measured against the pro-rated line instead.
4. `Consumed = max(0, actual - proRatedTarget)`; `Remaining = BudgetAmount - Consumed`; `ConsumedRatio = Consumed / BudgetAmount`.
5. **Burn rate** = `ConsumedRatio / elapsed` — `1.0` means the budget lands exactly at zero on the last day of the window; `2.0` means it is burning twice as fast as the window is elapsing.
6. When burn rate `> 1`, project an **exhaustion date** by extrapolating the observed rate forward to where `ConsumedRatio` would hit 1.0, within the window.
7. `BudgetState`: `healthy` (<50% consumed) → `watch` (≥50%) → `at_risk` (≥75%) → `exhausted` (≥100%) → `breached` (actual itself exceeds target, regardless of pacing) → `unknown` (window has not started, or insufficient data).

**The declared breach response is what makes the budget more than a chart.** Every `CostSLO` names its `BreachActions` in advance: `notify`, `require_approval` (escalate every cost-increasing change to human approval, even ones policy would normally auto-approve), `freeze_cost_increases` (refuse cost-increasing changes outright), `generate_recommendations` (trigger an out-of-band optimization run scoped to the breaching entity), `open_investigation` (create a tracked investigation record with the anomaly decomposition attached). `EconomicErrorBudget.AllowsCostIncrease()` translates the triggered actions into `(allowed bool, requiresApproval bool)`, consumed directly by `govern.Input.BudgetFreeze`/`BudgetRequiresApproval` (see [`automation-spec.md`](automation-spec.md)) — a frozen budget is a hard `prohibit` applied as a platform invariant no policy rule can override; a budget under pressure but not exhausted downgrades `auto_execute` to `require_approval`. Both apply to cost-*increasing* changes only, matching what `AllowsCostIncrease()` is asked: a change that lowers run-rate is what an over-budget tenant needs, and is never gated by its own budget breach.

## SPEC-ECON-005 — Cloud Efficiency Score

`econ.EfficiencyScore` is a **weighted composite**, not a single ratio, because a single ratio is gameable and uninformative — an estate can have perfect utilization and still waste a fortune on unattached volumes, unamortized commitments and cross-AZ chatter. `StandardFactorWeights`:

| Factor | Weight |
|---|---|
| resource_utilization | 0.22 |
| waste_elimination | 0.20 |
| commitment_coverage | 0.15 |
| storage_efficiency | 0.10 |
| network_efficiency | 0.10 |
| architecture_efficiency | 0.10 |
| automation_maturity | 0.07 |
| governance_maturity | 0.06 |

`ComputeEfficiencyScore` combines the weighted, 0–100-clamped factors into one score, grades it A–F, and computes `WasteRatio = IdentifiedWaste / TotalSpend`. Every factor is reported alongside the composite so the score is explainable and each point of improvement maps to a specific action — never an opaque single number. Weights are configurable per tenant (`REQ-ECON-011`), because a serverless-first startup and a lift-and-shift estate do not have the same levers.

## SPEC-TWIN-001 — One graph, multiple view projections

`internal/application/twin` renders the estate — resources, relationships, attributed cost, metrics, findings — as a graph a human can actually look at. The graph itself never changes shape: resources and relationships. A **view** is a projection that picks which numeric fields drive node size and colour and which edges matter — the cost view sizes nodes by `MonthlyCost` and colours by spend tier; the reliability view sizes nodes by blast radius and colours by risk. Building six view-specific graph types would let each one drift out of sync with the others; instead every view produces the same `TwinNode` shape with different fields populated, and `TwinGraph.Legend` documents which fields the caller should read for the active view — which is also what makes collapsing safe: a collapsed synthetic node satisfies the identical struct, so nothing downstream needs a special case for it.

## SPEC-TWIN-002 — Cost-flow conservation, provable by construction

`CostFlow`'s accumulation is built so conservation is provable by construction, not merely observed to hold in testing (`internal/application/twin/costflow.go`, tested in `costflow_test.go`). Every resource's own cost is injected exactly once, at its own node; an edge only ever redistributes a fraction of what a node has already received — never more than 100%, and the un-redistributed remainder stays attributed to that node. Summing every node's displayed amount, at every level, is therefore always exactly equal to the sum of the resources' own billed cost — by induction over the graph's topological order — plus the honest `Unattributed` remainder for the true billed total, which can exceed the summed flow.

## SPEC-TWIN-003 — Dependents traversal and blast radius

`Topology.Dependents(id, maxDepth)` (used by both the twin's reliability view and `optimization`'s blast-radius computation — see [`optimization-spec.md`](optimization-spec.md)) returns the transitive downstream closure along request-path edges to a bounded depth (6), discounted by edge confidence, so a thin or partially-discovered dependency graph produces a lower completeness figure rather than a falsely reassuring small blast radius.

## Current limitations

- The attribution algorithm is implemented and unit-tested (`internal/application/economics/attribution_test.go`) against synthetic topologies; it has not been exercised against the scale or messiness of a real multi-hundred-service architecture graph, where `Consumers` edges are far more likely to be sparse.
- Cost-flow conservation is proved by construction and covered by `costflow_test.go`, but the twin has never been rendered client-side (see [Current limitations](../README.md#current-limitations-and-what-production-hardening-would-still-require) — the frontend is an unmodified scaffold).
- The Cloud Efficiency Score's factor weights are a documented default; no tenant-specific weighting has ever actually been exercised end to end (the override mechanism is a stored config field with no UI or workflow built around it in this codebase).
