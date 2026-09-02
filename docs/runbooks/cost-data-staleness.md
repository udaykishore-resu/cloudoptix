# Runbook: cost-data staleness

## Symptom

`costs/summary`/`costs/breakdown` figures look out of date relative to a customer's own AWS console, an economic footprint doesn't reflect recent spend, or a Cost SLO's `EconomicErrorBudget.EvaluatedAt` is older than expected.

## Diagnosis

1. **Check `IngestResult.Source` and the most recent successful ingestion timestamp** for the affected account. CUR-based ingestion and Cost Explorer-based ingestion have different natural lag: CUR itself typically delivers to the customer's S3 bucket with up to a 24-hour lag from AWS's side before CloudOptix can even read it; Cost Explorer's own data has a shorter but still real lag. Confirm which regime is in effect (`REQ-COST-001`) before concluding CloudOptix itself is behind — some of the staleness may be AWS's own delivery lag, not a CloudOptix ingestion problem.
2. **Check whether ingestion is failing outright versus merely not having run recently.** A failed ingestion job leaves an explicit error; a job that simply hasn't been triggered recently (a scheduling gap) shows no recent attempt at all — these need different fixes.
3. **If CUR-based**, verify the CUR export itself is still configured and delivering in the customer's account (`spec.AWS.CUR.Bucket`/`Prefix`/`ReportName`) — a customer disabling or reconfiguring their own CUR export outside of CloudOptix is a common, entirely customer-side cause that presents identically to a CloudOptix ingestion failure from the dashboard's point of view.

## Resolution

**Ingestion job failing:** Check the specific error from the last failed attempt. A permission error (missing S3 read access to the CUR bucket, or missing Cost Explorer API access) is a variant of [`discovery-iam-gaps.md`](discovery-iam-gaps.md) — the same `ScopeAnalyze` role is what CUR/Cost Explorer access is granted through, so the same "compare the denied action against the role's policy" resolution applies. A throttling error is [`aws-throttling.md`](aws-throttling.md).

**Ingestion job not running on schedule:** This is a scheduling/worker issue, not a data issue — confirm the ingestion trigger (whether cron-driven or event-driven in the deployed configuration) is actually firing; manually trigger `POST /costs/ingest` to unblock the immediate staleness while the scheduling gap is investigated.

**CUR export misconfigured or disabled in the customer's account:** This requires the customer to re-enable/reconfigure CUR delivery on their side; CloudOptix has no ability to configure a customer's CUR export directly (the same "CloudOptix cannot modify what it does not have execute-level access to configure" boundary as any other customer-owned AWS configuration). In the interim, `internal/application/costing`'s CUR-preference-with-fallback design (`REQ-COST-001`) means Cost Explorer-sourced ingestion should still be functioning, coarser but current — confirm `IngestResult.Source` has actually fallen back rather than also failing.

**Idempotent re-ingestion is safe.** `REQ-COST-008` guarantees re-running ingestion for an already-ingested period does not duplicate line items — a re-ingestion triggered to "catch up" after resolving a staleness cause is always safe to run, including re-running for periods already partially ingested.

## What NOT to do

- Do not manually insert or adjust cost records to "fill the gap" — this breaks the property that every `cost.Record` traces to an actual CUR/Cost Explorer line item (SPEC-COST-001), and any manually-inserted figure will be indistinguishable from a real one to every downstream engine, including the economics engine's provenance model.
- Do not silently extend the anomaly-detection baseline window to paper over a staleness gap — a gap in the trailing window changes what the robust z-score baseline actually represents (SPEC-COST-004), and anomaly detection run across a gap should be treated as less reliable until the gap is backfilled, not assumed unaffected.

## Escalation

Sustained staleness (beyond the AWS-side delivery lag alone) with no identifiable ingestion failure or scheduling gap should be escalated to the team owning `internal/application/costing`/`internal/adapters/aws/costing` — this pattern suggests a defect in the ingestion pipeline's own completion/success reporting, not an operational cause this runbook's steps can resolve.
