// Package specsvc implements ports.SpecService: post-onboarding
// specification management — reading versions, proposing and reviewing
// revisions, approving and rejecting them, and importing or exporting the
// cloudoptix.yaml document a customer keeps in their own repository.
//
// KEY DESIGN DECISION: onboarding (internal/application/onboarding) is the
// only place a specification is drafted turn-by-turn through conversation;
// once approved, a tenant no longer talks to an agent to change its
// configuration — it proposes a patch, reviews the diff and completeness
// that patch would produce, and a human approves or rejects it. That is why
// ProposeRevision both re-validates and computes the diff before anything
// is stored: a reviewer must never be shown a draft that cannot legally
// become the active specification, and must never be asked to approve a
// change without first seeing exactly what it moves. Approval and
// supersession of the version it replaces happen inside one
// ports.UnitOfWork for the same reason spec.SpecRepository.Approve itself is
// documented as needing to be atomic — two simultaneously active versions
// would mean every downstream engine is reading a different configuration
// depending on which one it happened to load first.
//
// Traceability: REQ-SPEC-001..015, SPEC-ONB-001, SPEC-ONB-004, SPEC-ONB-005.
package specsvc
