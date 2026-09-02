# Runbook: a stuck execution plan

## Symptom

An `execute.Plan` has not progressed to a terminal state (`Terminal()`: `PlanValidated`, `PlanRolledBack`, `PlanCancelled`, `PlanRollbackFailed`) for far longer than the tenant's typical validation window — visible via `GET /executions/{id}` showing the plan sitting in `PlanExecuting`, `PlanValidating`, or `PlanRollingBack` past its expected duration.

## Diagnosis

1. **Identify exactly which phase it is stuck in.** The four-phase discipline (SPEC-AUTO-001) means the plan's current `PlanState` tells you precisely which phase to investigate — there is no ambiguous "somewhere in execution" state:
   - `PlanExecuting`: stuck mid-step. Check the plan's step-by-step status for which specific step has not completed.
   - `PlanValidating`: the observation window has not closed, or is taking longer than `ValidationWindowMinutes` should allow.
   - `PlanRollingBack`: a rollback is itself in progress and has not completed — see [`failed-rollback.md`](failed-rollback.md) if this state persists, since a stuck rollback is materially more urgent than a stuck forward execution.
2. **For a stuck `PlanExecuting`**: check whether the current step is retrying (transient AWS error, within the bounded retry/backoff budget — expected, will resolve on its own within the retry budget) or has exhausted retries without transitioning the plan to `PlanFailed` (a defect — every step failure should either retry within budget or terminate the plan, never leave it silently stalled with no further activity).
3. **For a stuck `PlanValidating`**: check `MinSamples` against actual observed sample count for the validation window's declared metrics — a validation window that cannot gather `MinSamples` (e.g., a very low-traffic resource, or a metrics-collection gap — see [`aws-throttling.md`](aws-throttling.md)) will not close on its own and needs either a longer window, a lower `MinSamples` threshold for that resource class, or manual intervention.

## Resolution

**Stuck mid-execution, retries exhausted, plan not auto-failed (a defect state):** This should not happen by design — `Execute`'s retry loop is bounded and should transition the plan to `PlanFailed` on exhaustion, which (per SPEC-AUTO-001) should never leave a plan executable-but-abandoned. Manually inspect the last completed step and the specific AWS error on the failed step; if the step's mutation partially landed (check via a fresh discovery/describe call against the actual resource state, not the plan's own optimistic record), treat this as equivalent to a failed rollback scenario — see [`failed-rollback.md`](failed-rollback.md) — since the platform's own bookkeeping and the resource's true state have diverged.

**Stuck in validation, insufficient samples:** `POST /executions/{id}/validate` (`REQ-VAL-008`) allows an operator to force an early validation pass — use this deliberately, understanding that a forced-early validation against too few samples is more likely to return `VerdictInconclusive` than a confident verdict, which is the correct, honest outcome for that situation rather than a false success or failure.

**Stuck in validation, sufficient samples but no verdict rendered:** This indicates the validation-evaluation job itself did not run or did not complete — check the worker responsible for closing validation windows; this is an operational/scheduling issue, not a data issue, and forcing validation manually (above) is the correct immediate mitigation while the underlying scheduling issue is investigated.

## What NOT to do

- Do not manually transition a plan's `PlanState` directly in the database to "unstick" it. Every state transition in this design exists specifically to keep the rollback-availability and re-check-governance-before-AWS-call invariants true; a manual state edit can produce a plan the system believes is further along than the AWS account actually is.
- Do not cancel (`POST /executions/{id}/cancel`) a plan that has already started mutating AWS resources without first confirming what state those resources are actually in — cancellation is designed for pre-execution or early-execution abort, not as a substitute for rollback once a mutation has landed.

## Escalation

A plan stuck in `PlanExecuting` with exhausted retries and no automatic transition to `PlanFailed` is a defect in `internal/application/automation`'s execute loop — escalate to the team owning that package with the plan ID, the last completed step, and the exact AWS error from the final retry attempt.
