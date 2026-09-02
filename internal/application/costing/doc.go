// Package costing implements ports.CostService: the cost-intelligence engine
// that turns billed AWS spend into ingested records, roll-ups, forecasts,
// anomalies and explanations.
//
// Two design decisions carry the package:
//
//  1. Ingestion prefers the Cost & Usage Report over Cost Explorer, and says
//     so in the result rather than silently picking one. CUR gives
//     hourly, resource-level line items; Cost Explorer gives daily,
//     service-level aggregates. A tenant who has not enabled CUR still gets
//     cost intelligence, just coarser and without per-resource attribution —
//     and CostFilter.Basis / IngestResult.Source record which regime is
//     in effect so nothing downstream mistakes one for the other.
//  2. Anomaly detection uses a robust z-score — median and median absolute
//     deviation (MAD), not mean and standard deviation. Cost data is
//     dominated by its own anomalies: a Savings Plan purchase, a one-off
//     migration, a genuine spend spike. The mean and stddev of a trailing
//     window are themselves dragged by those events, so a mean-based z-score
//     systematically under-reacts to the very thing it exists to catch — by
//     the third day of a real spike the "baseline" has already absorbed it.
//     The median and MAD are robust to a minority of outlying points, so the
//     baseline stays representative of normal spend even while an anomaly is
//     sitting inside the trailing window.
//
// Traceability: REQ-COST-001..008, SPEC-COST-001..004.
package costing
