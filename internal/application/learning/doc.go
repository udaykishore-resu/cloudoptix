// Package learning implements the self-learning loop behind
// AutomationService.Learn: it turns execute.Outcome records — what a
// recommendation predicted versus what actually happened once it was
// validated — into execute.RuleCalibration multipliers that future
// recommendations from the same rule are scaled by, and it feeds the same
// outcomes back into the RAG corpus as searchable tenant documents so the
// copilot can answer "how has this kind of change gone for us before" with
// this tenant's own history instead of a generic answer.
//
// # What this loop is not allowed to touch
//
// This has to be said plainly, in one place, because it is the property
// that makes it safe to run unattended: the loop reads execute.Outcome and
// writes execute.RuleCalibration and RAG documents. It does not read or
// write a govern.Policy, an optimize.Rule, a spec.Spec, or any
// ValidationCheck. A calibration multiplier changes how confident a future
// recommendation from a rule claims to be and how large its predicted
// saving claims to be — nothing about whether that recommendation is
// allowed to auto-execute, what approvals it needs, or what a rollback plan
// looks like. Those are governed by govern.Evaluate against a policy a human
// authored and activated, and by the fixed, code-reviewed safety checks in
// internal/application/automation; neither is influenced by this package's
// output.
//
// The reason is not caution for its own sake. A system whose own outcomes
// can rewrite the rules that decide what it is allowed to do next has
// closed a loop that a human is supposed to be standing in: a rule that
// looks good on paper because its first ten executions happened to land in
// a quiet week could, if it were also the thing deciding its own future
// permissions, promote itself into auto-executing in production before
// anyone reviewed why. Keeping calibration strictly downstream of
// governance — an input to how a recommendation is described, never to
// what it is allowed to do — is what makes it defensible to let this loop
// run without a human approving each calibration pass. Confidence
// multipliers computed by this package feed into how confident a rule's
// *next* recommendation claims to be; approval policy always looks at that
// resulting recommendation, exactly as it would look at any other.
//
// # Why a minimum-sample guard
//
// execute.Calibrate (the pure domain function this package's Recalibrate
// wraps) refuses to move a rule's multiplier away from neutral until it has
// seen a minimum number of outcomes. Two rollbacks out of two attempts is
// not evidence a rule is bad; it might be. Adjusting on that little
// evidence is not learning, it is amplifying noise into a decision that
// then compounds — a shrunk confidence changes what a human sees on the
// recommendation, which changes whether they approve it, which changes
// whether a rule ever gets the sample size to be judged fairly. The guard
// is what keeps a rule's calibration a slow-moving, defensible number
// instead of a jumpy one.
//
// Traceability: REQ-LRN-001..006, SPEC-OPT-008.
package learning
