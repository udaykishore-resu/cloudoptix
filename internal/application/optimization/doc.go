// Package optimization is the deterministic rule engine at the heart of
// CloudOptix: the code that decides, from a tenant's discovered estate, its
// utilisation telemetry and the AWS price book, exactly which changes would
// save money and how much confidence CloudOptix has in each one.
//
// The key design decision is the separation between three things that a
// naive cost tool conflates into one opaque score:
//
//  1. A Rule fires deterministically on evidence and produces a Finding — a
//     statement of fact ("this instance's P99 CPU is 11% over 14 days with
//     92% coverage"), never a recommendation. Two runs against the same
//     inputs always produce the same findings in the same order, which is
//     what lets a recommendation survive a change-review meeting: nothing
//     about it depends on model sampling, wall-clock time, or map iteration
//     order.
//  2. Confidence, blast radius and risk are computed by dedicated,
//     independently testable functions (confidence.go, blast.go, risk.go)
//     from structured facts — metric stability, dependency-graph
//     completeness, business criticality, historical calibration — never
//     from an LLM's self-assessment. An LLM may narrate a recommendation
//     afterwards; it never produces the number a policy decision is made on.
//  3. The Registry (engine.go) owns which rules run, with what thresholds,
//     for which tenant, loaded from the versioned YAML rule pack in
//     rules/. A threshold change is a config diff, not a code deploy.
//
// Every rule in this package guards against three failure modes that make
// naive cost-optimization tools untrustworthy: acting on insufficient
// telemetry (an idle-looking resource with 6% metric coverage is a data
// problem, not a finding), rightsizing on the mean instead of the tail (the
// exact failure mode that causes an outage the night a batch job runs), and
// recommending an action the tenant's own risk tolerance, exclusions or SLOs
// rule out.
//
// Traceability: REQ-OPT-001..014, SPEC-OPT-001..008.
package optimization
