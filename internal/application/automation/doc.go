// Package automation implements ports.AutomationService: the machinery that
// turns a governed recommendation into a real, reversible change in a
// customer's AWS account, watches what actually happened, and undoes it when
// it did not go well.
//
// # The four-phase discipline
//
// Every change goes through the same four phases, and each phase is a
// separate, independently auditable call: Plan (build the forward steps, the
// snapshot steps and the rollback plan — no AWS call, nothing mutates),
// Execute (re-check governance one more time, then run the plan step by
// step against AWS with idempotency and retries), Validate (watch the
// change for its declared observation window and render a verdict against
// baseline), Rollback (reverse it). Nothing skips a phase: PlanExecution's
// output is not executable on its own, Execute refuses to run a plan whose
// rollback was not built, and Validate never runs against a plan that has
// not finished executing. The obvious alternative — one long "just do it"
// method — would make every one of those refusals implicit and untestable;
// keeping the phases as separate calls with separate persisted state is what
// lets an operator (or a test) stop the story at any point and see exactly
// what has and has not happened yet.
//
// # Why the rollback plan is built before anything runs
//
// execute.Plan.Executable requires a non-nil Rollback and refuses a plan that
// mutates without a preceding snapshot. This package enforces both checks a
// second time at the orchestration layer, on top of what the domain type and
// each ports.Executor implementation already enforce, because the safety
// property — "CloudOptix never makes a change it cannot undo, or at minimum
// cannot honestly describe as undoable" — is exactly the kind of invariant
// that must not depend on every future executor implementation remembering
// to preserve it. A rollback plan that is infeasible (a deletion, a released
// IP) is not blocked — some savings are only available by doing something
// irreversible — but it is never silently treated as feasible: DataLossRisk
// and InfeasibleReason travel with the plan into the approval screen.
//
// # Re-checking governance immediately before AWS
//
// A plan can sit approved for hours: a maintenance window can close, an
// economic error budget can freeze, a change freeze can start, an approval
// can expire. Execute calls governance.Evaluate again immediately before the
// first AWS call, not only trusting whatever state the plan carried from
// PlanExecution time, for the same reason a bank re-checks a balance
// immediately before honoring a transfer instead of trusting a balance it
// read five minutes ago. If the fresh decision no longer permits the
// change, Execute refuses — even for a plan that was perfectly valid when
// it was approved.
//
// # Idempotency is the retry story
//
// Every mutating step carries an IdempotencyKey the executor's Apply method
// is contractually required to honor (ports.Executor's doc comment says so;
// awssim's genericExecutor implements it by checking whether the target
// already matches the desired state before mutating). Execute leans on that
// contract for its own retry loop: a step that fails on a transient AWS
// error (throttling, a timeout) is retried with exponential backoff up to a
// bounded attempt count, and a retry that actually landed on the first
// attempt but failed to report success (a network cut after the API call
// succeeded) is indistinguishable from one that never landed — both are safe
// to retry because Apply is idempotent on the key, not because CloudOptix
// trusts the network to behave.
//
// # The autonomous loop's own blast radius
//
// ProcessAutonomous is the one entry point that acts without a human in the
// loop for this call, so it caps itself independently of any per-plan
// safety check: a per-cycle count of how many changes it will plan and
// execute, the tenant's declared maintenance windows, a running total against
// spec.Automation.MaxMonthlyImpact, and per-tenant concurrency all bound what
// one invocation can do before the next governance re-check happens on the
// next recommendation. A bug that caused every recommendation in a large
// estate to look auto-executable should degrade to "capped and reported",
// never to "every resource changed in one pass".
//
// # Savings, honestly measured
//
// Package execute's SavingsStage ladder is advanced by this package as each
// phase completes: Planned when PlanExecution succeeds, Executed when the
// AWS mutation lands, Validated when the observation window closes without a
// critical regression, and Realized when that same measurement is what
// produced the number (not the original prediction restated). A rollback —
// whether from a mid-plan execution failure or a post-validation critical
// regression — marks the record lost with the reason rather than continuing
// to advance it: hiding a lost saving would make the funnel's conversion
// rate a marketing number instead of an operational one, which is the exact
// failure mode execute.Funnel exists to prevent.
//
// Traceability: REQ-EXE-001..014, REQ-VAL-001..008, REQ-AUTO-001..009,
// SPEC-AUTO-001..006.
package automation
