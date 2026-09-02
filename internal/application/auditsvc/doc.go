// Package auditsvc implements ports.AuditService: the read and write surface
// over the tamper-evident audit trail in internal/domain/audit.
//
// The package does almost no reasoning of its own — audit.Record.Seal,
// audit.VerifyChain and the hash-chain invariant they encode live in the
// domain package and in the repository that appends against it (see
// ports.AuditRepository's doc comment). What this package owns is the
// translation between the transport-facing ports.AuditEntry/ports.AuditQuery
// shapes and the domain's audit.Record/audit.Query, and Timeline: pulling
// together every record that tells the story of one recommendation — its
// creation, the policy decision, the approval, the plan, each execution
// step, validation, any rollback, and the savings realized — in the order
// they actually happened. That ordering is not a heuristic: audit.Record's
// Sequence field is a strictly monotonic per-tenant counter assigned at
// Append time, so sorting by it is sorting by causal order exactly, not an
// approximation of it.
//
// Traceability: REQ-AUD-001..009, SPEC-SEC-005.
package auditsvc
