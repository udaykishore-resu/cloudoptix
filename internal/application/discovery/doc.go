// Package discovery implements ports.DiscoveryService: the orchestrator that
// turns "scan this AWS account" into a normalized, attributed resource
// inventory. Every other engine in this platform — the twin, cost
// attribution, economics, optimization — is only as trustworthy as what this
// package writes to the resource store, so its design choices are all in
// service of one property: a discovery run must never make the model worse
// than it was before the run started, even when AWS throttles it, denies it
// a permission, or the run is cut off halfway through.
//
// Four decisions carry that property:
//
//  1. One (service × region) job per unit of concurrency, run through a
//     bounded worker pool with its own retry loop. A single service
//     throttling or a single missing IAM permission fails that one job —
//     recorded with the exact denied action, not a generic error string — and
//     every other job keeps running. Nothing here treats "scan the estate" as
//     an all-or-nothing operation, because on a real account it never is.
//  2. The tombstone pass (marking resources absent because they were not
//     seen this scan) is scoped by construction to exactly the (kind, region)
//     pairs a *successful* job actually covered this run. A discoverer that
//     failed contributes zero kinds to that region's tombstone set, so its
//     resource kind is left untouched rather than deleted for having gone
//     unobserved — the failure mode of "partial scan wipes half the estate"
//     is not a bug to avoid here, it is a state the code cannot reach: the
//     kind/region pairs MarkAbsent is ever called with are read from the same
//     coverage map that only successful jobs write into.
//  3. Retries use exponential backoff with full jitter, and only for errors
//     core.Retryable classifies as transient (throttling, timeouts,
//     dependency unavailability). A permission error is never retried — no
//     amount of waiting fixes a missing IAM action — so it fails fast and
//     reports the denied action for the onboarding flow to act on
//     immediately instead of after four wasted attempts.
//  4. Attribution (environment, application, workload, owner, criticality) is
//     resolved once per run from three sources in order of trust: a
//     recognised tag on the resource itself, an AttributionRule (evaluated in
//     priority order, first match wins — see cloud.AttributionRule), and
//     finally the onboarded account's own declared environment as the
//     weakest fallback. Every resource records which source won via
//     core.Provenance, so a downstream engine — and a human looking at the
//     twin — can see "this is confirmed" versus "this is an account-level
//     guess" rather than one undifferentiated field.
//
// Traceability: REQ-DSC-001..014, SPEC-DSC-001..003.
package discovery
