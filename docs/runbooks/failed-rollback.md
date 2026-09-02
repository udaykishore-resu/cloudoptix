# Runbook: a failed rollback (the hard one)

This is the worst operational outcome CloudOptix can produce: a change was made to a customer's AWS account, something went wrong, and the platform's own attempt to undo it did not succeed. Per [`docs/slos.md`](../slos.md), rollback success carries a 100% target with no error budget — treat every occurrence of this as a P0 incident, not a statistic.

## Symptom

`execute.Plan.State == PlanRollbackFailed` — the plan reached `PlanRollingBack` and did not reach `PlanRolledBack`.

## Why this can happen at all

The four-phase discipline (SPEC-AUTO-001) requires a rollback plan to exist before any forward mutation runs, and `execute.Plan.Executable` refuses a plan that mutates without a preceding snapshot. This makes an *unplanned* rollback failure (no rollback plan existed) structurally impossible — every plan that reached execution had one. What remains possible, and is exactly what this runbook is for, is a **planned** rollback that itself fails to execute cleanly: the rollback step's own AWS call errors, times out, or succeeds partially (e.g., a multi-step rollback where step 2 of 3 fails after step 1 already landed).

A second, distinct cause: the original forward change's rollback was marked `InfeasibleReason`-carrying at plan time (a deletion, a released Elastic IP — some savings are only available by doing something irreversible) and was approved anyway, with that risk visible on the approval screen. If that plan's forward execution now needs to be undone, "rollback failed" here does not mean a bug — it means the platform is honestly reporting that this specific change was never undoable, exactly as disclosed at approval time.

## Diagnosis — first, distinguish the two causes above

1. **Check the original plan's `DataLossRisk`/`InfeasibleReason` fields**, set at plan-creation time. If the rollback was marked infeasible and approved with that risk explicit, this is not a platform defect — skip to "Infeasible-rollback recovery" below.
2. **If the rollback was feasible and planned normally**, inspect the rollback plan's own step-by-step status exactly as you would a forward execution (see [`stuck-execution-plan.md`](stuck-execution-plan.md) for the general technique) — which specific rollback step failed, and what was the AWS error.
3. **Determine the actual current state of the affected resource** by querying AWS directly (via a fresh discovery run or a targeted describe call), not by trusting the plan's own bookkeeping — a rollback that failed partway through a multi-step sequence means the platform's *recorded* state and the resource's *actual* state have diverged, and the actual state is what matters for recovery.

## Resolution — feasible rollback that failed to execute

1. **Do not retry the rollback blindly.** Because every executor's `Apply` is required to be idempotent on `IdempotencyKey` (SPEC-AUTO-003), a straightforward retry of the *same* rollback plan is usually safe and is the first thing to attempt — but only after step 3 above confirms what actually changed, since a rollback step that failed to *report* success may have actually landed (the same "network cut after the API call succeeded" ambiguity the forward-execution retry story is built to tolerate).
2. **If the retry also fails**, manually reconcile the specific AWS resource to its pre-change state using the AWS console or CLI directly, using the rollback plan's own recorded target state as the specification for what "reconciled" means — the rollback plan already states exactly what the resource should look like; a human is now performing that specific, scoped action instead of the executor.
3. **Once manually reconciled, verify** by running a fresh discovery scan scoped to the affected resource and confirming its state matches the rollback plan's target — do not mark the plan `PlanRolledBack` in the platform's own records until this independent verification confirms it, since the platform's own state must not diverge from the AWS account's real state.
4. **File a defect against the specific executor** (`internal/adapters/aws/executor/*.go`) that failed to apply the rollback — a rollback that requires manual reconciliation is, definitionally, a gap in that executor's idempotency or error-handling that should not recur.

## Resolution — infeasible-rollback recovery (the change was never undoable)

1. Confirm this from the original approval record — `DataLossRisk`/`InfeasibleReason` should be visible in the audit trail (`GET /audit/recommendations/{id}/timeline`) exactly as it was shown to the approver.
2. There is no platform action that recovers this — the resource is gone or changed permanently, as disclosed. Recovery is whatever the customer's own backup/DR posture allows (a snapshot taken before the change, if the customer's own retention policy kept one — CloudOptix's own pre-change snapshot step, if the executor's snapshot phase captured restorable state rather than only metadata, may also be usable — check the specific executor's snapshot-step output for what it actually captured).
3. Mark the `SavingsRecord` `Lost` if this was a savings-motivated change, with `LostReason` stating explicitly that the rollback was infeasible and approved as such — this is not a case to hide from the savings funnel (ADR-0009); an honestly-lost saving from a disclosed-irreversible change is exactly the kind of entry the funnel exists to surface.

## What NOT to do

- Do not mark a plan `PlanRolledBack` based on the rollback API call returning success alone, without independently verifying the resource's actual state — the entire point of idempotent, verifiable execution is that "the API call succeeded" and "the resource is now in the intended state" are checked separately, not assumed to be the same fact.
- Do not attempt a *different*, ad hoc corrective action outside the recorded rollback plan's own target state without updating the audit trail to reflect what was actually done — the audit record must describe reality, not the platform's original intended action, once a human has intervened.

## Escalation

Every `PlanRollbackFailed` occurrence should be treated as a P0 regardless of whether the cause turns out to be the "infeasible, disclosed" case or the "executor defect" case — the triage in this runbook exists to route it correctly and quickly, not to downgrade its severity. Escalate to the on-call for `internal/application/automation` and `internal/adapters/aws/executor` immediately; do not wait for root-cause diagnosis to page.
