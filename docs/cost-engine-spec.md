# Cost engine specification

Covers `SPEC-COST-001..004`, implemented by `internal/domain/cost`, `internal/application/costing`, `internal/adapters/aws/costing`, and `internal/adapters/pricing`.

## SPEC-COST-001 — Monetary precision and the billed/attributed distinction

Two decisions carry this specification.

**Money is exact.** `core.Money` (`internal/domain/core/money.go`) is an exact monetary amount held as integer micro-units of currency (1 USD == 1,000,000 micros), never a float. Cloud economics multiplies tiny unit prices ($0.0000166667 per GB-second) by very large usage quantities; float accumulation over millions of line items drifts enough to move a cost-per-transaction figure in the third decimal — exactly the digit CloudOptix reports on. Money is integer everywhere and only converted to float at a presentation boundary. See [ADR-0003](adr/0003-integer-micros-money.md).

**Billed cost is not attributed cost.** Package `cost` models what AWS asserts (a fact); package `econ` models CloudOptix's inference of who caused it (a model). Mixing the two is exactly how FinOps tools end up reporting numbers that do not reconcile with the invoice — every figure in `cost` traces to a specific CUR/Cost Explorer line item, and every figure in `econ` is explicit about how much of a scope's cost is `Unattributed`.

## SPEC-COST-002 — Ingestion

`CostIngestor` (`ports.CostIngestor`) prefers the Cost & Usage Report over Cost Explorer, and says which regime is in effect rather than silently picking one: CUR gives hourly, resource-level line items; Cost Explorer gives daily, service-level aggregates. A tenant who has not enabled CUR still gets cost intelligence — just coarser, without per-resource attribution — and `CostFilter.Basis`/`IngestResult.Source` record which is in effect so nothing downstream mistakes one for the other.

`cost.AmortizationBasis` selects which of AWS's several cost figures a record carries; `BasisAmortized` is the primary stored basis, because unblended cost makes a Savings Plan purchase look like a one-off spike and the following eleven months look artificially cheap — producing nonsense downstream recommendations. `cost.ChargeType` distinguishes usage from the accounting entries around it (`Tax`, `Credit`, `Refund`, `Fee`, `SavingsPlanRecurringFee`, `RIFee`, `Discount`) — credits and refunds are never spread across workloads as if they were consumption.

## SPEC-COST-003 — Pricing catalog

`internal/adapters/pricing` (backed by `pricebook.json`) supplies on-demand, Reserved Instance, Savings Plan, and Spot pricing per instance type/region/platform, plus EBS, RDS, S3, ElastiCache, and per-service (NAT gateway, Lambda, DynamoDB, ...) rates, and region multipliers. `InstanceSpec`/`InstanceFamily`/`SmallerCandidates` support the rightsizing rules' "two rungs down the family ladder" heuristic (see [`optimization-spec.md`](optimization-spec.md)). Every price carries a `pricing_date`; a `CompilationResult` (Cost Compiler) or a rule's finding is only as current as that catalog snapshot — there is no live pricing-API call in the reference implementation.

## SPEC-COST-004 — Anomaly detection

Anomaly detection uses a **robust z-score** — median and median absolute deviation (MAD), not mean and standard deviation. Cost data is dominated by its own anomalies: a Savings Plan purchase, a one-off migration, a genuine spend spike. The mean and stddev of a trailing window are themselves dragged by those events, so a mean-based z-score systematically under-reacts to the very thing it exists to catch — by the third day of a real spike, the "baseline" has already absorbed it. Median and MAD are robust to a minority of outlying points, so the baseline stays representative of normal spend even while an anomaly sits inside the trailing window.

`costs/explain` (`REQ-COST-007`) decomposes a period-over-period change into named drivers (volume, rate, new resource, discount) rather than leaving a reviewer to infer cause from a bare delta.

## Roll-up granularities

`cost.Granularity` — hourly, daily, monthly — are all derivable from the same ingested line items; nothing downstream needs to know which granularity a report was originally requested at to re-aggregate.

## Current limitations

- The real ingestion adapters (`internal/adapters/aws/costing/{costexplorer,cur}.go`) are unit-tested against recorded/mocked API responses; ingestion has never run against a real payer account's actual CUR export.
- `pricebook.json` is a snapshot (`pricing_date` field), manually maintained rather than pulled from AWS's live Price List API — a real deployment would need a refresh job this codebase does not implement.
- Anomaly detection's median/MAD approach is implemented and unit-tested (`internal/application/costing/anomaly_test.go`) against synthetic series; it has not been validated against the anomaly patterns a real multi-year cost history would present (seasonal effects longer than the trailing window, for instance).
