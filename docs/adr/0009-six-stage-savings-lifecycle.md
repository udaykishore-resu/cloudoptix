# ADR-0009: Six-stage savings lifecycle

## Status

Accepted, implemented.

## Context

Nearly every cost-optimization tool reports one number: "potential savings." That figure assumes every recommendation gets approved, every approval gets executed exactly as planned, every execution holds up under real traffic, and every predicted dollar amount is exactly what shows up on the next invoice. None of those four assumptions is reliably true, and a tool that reports only the top-line number gives a FinOps lead no way to see where value is actually leaking out of the funnel — or to know whether "we found $2M in potential savings this quarter" bears any relationship to what the company actually spent less on.

## Decision

`execute.SavingsStage` (`internal/domain/execute/savings.go`) defines six explicit rungs — `potential → approved → planned → executed → validated → realized` — and `internal/application/automation` advances a `SavingsRecord` through them only as each phase's evidence actually lands: `Planned` when `PlanExecution` succeeds (a rollback-inclusive plan exists), `Executed` when the AWS mutation lands, `Validated` when the post-change observation window closes without a critical regression, and `Realized` only when *the next invoice*, not the original prediction restated, confirms the reduction. A rollback at any stage — whether from a mid-plan execution failure or a post-validation critical regression — marks the record `Lost` with a stated `LostReason`, rather than quietly removing it from the funnel or leaving it stuck at whatever stage it last reached.

## Consequences

**Positive:**
- The funnel's conversion rate is an operational number a FinOps lead can act on: "we generate $61K/month in potential savings but only realize $32K" is a materially different, more useful statement than "we identified $61K/month in savings," and it points directly at *where* to intervene (approval friction? execution failures? validation regressions?).
- `Realized` is the only figure the platform will put in front of a CFO, and it is defined so that it cannot be gamed by restating a prediction — it requires an actual second measurement, after the fact, from billing data.
- Lost savings are visible, not hidden — a rollback is exactly as loud in the funnel as a success, which is what keeps the conversion rate honest rather than survivorship-biased.
- `execute.Outcome` (predicted vs. actual, at `Realized`) is the exact input the learning loop's confidence calibration reads from — the ladder is not only a reporting device, it is the platform's own feedback signal.

**Negative:**
- The full ladder requires the platform to keep watching a change well past the moment it executes — through a validation window and into the next billing cycle — which is materially more bookkeeping than a fire-and-forget "recommendation executed" event, and means a "realized" figure always lags a "potential" one by at least a billing cycle.
- A recommendation stuck at an intermediate stage for a long time (approved but never planned; executed but the validation window never closes because of missing telemetry) needs its own monitoring to avoid quietly rotting in the funnel unnoticed — the ladder makes this visible in principle but does not, on its own, alert on it (see [`docs/slos.md`](../slos.md) for where that alerting would live).
- Six stages is more states to reason about, test, and persist than a boolean "done/not done" — a deliberate complexity trade the design accepts in exchange for the honesty the funnel provides.

## Alternatives considered

**Report "potential savings" only, at generation time.** Rejected — this is the exact status-quo failure mode the ADR's Context section describes, and the one the root README explicitly contrasts CloudOptix against as a category difference from traditional FinOps tooling.

**A simpler two-stage model: "recommended" and "done."** Rejected: collapses exactly the distinctions that make the funnel diagnostic. "Done" conflates "AWS accepted the API call" with "the change held up" with "the bill actually went down" — three different failure points that a FinOps lead needs to distinguish to know what to fix.

**Advance `Realized` from the plan's predicted saving at execution time, rather than waiting for the next invoice.** Rejected: this is precisely the "restating the prediction" shortcut the decision explicitly refuses. The whole point of a `Realized` stage distinct from `Executed`/`Validated` is that it is backed by an independent measurement, not a restated estimate — collapsing it into execution time would make `Realized` mean nothing more than `Executed` already means.
