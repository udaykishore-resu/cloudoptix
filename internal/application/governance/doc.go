// Package governance implements ports.GovernanceService: the application
// layer around the pure, deterministic policy engine in internal/domain/govern.
//
// govern.Evaluate is intentionally a pure function of (Policy, Input) — no
// I/O, no clock reads, no repository calls — because a decision has to be
// reproducible months later during an audit. Everything this package does is
// in service of that purity: it is the one place responsible for assembling
// a complete, honest govern.Input from the recommendation, the resource, the
// tenant's approved specification and the current economic error budgets,
// and for persisting the Decision that comes back. The input assembly is
// the security-critical half of governance, not the rule evaluation itself —
// a rule can only be as strict as the facts it is given, so a missing or
// zero-valued field here (an empty AccountID, a Region that silently
// defaulted) would silently weaken every guard downstream without the policy
// document itself ever looking wrong. buildInput therefore validates that
// every field the domain package's own Match guards read is actually
// populated, and fails closed — returns an error, produces no Decision —
// rather than evaluate a policy against a partially-blank Input.
//
// Two governance constraints live here rather than in a govern.Policy rule
// because they are not authored by a tenant editing policy: they come from
// the tenant's approved specification (excluded actions/resources/tags,
// change-freeze windows), a different artefact with its own review and
// approval flow. govern.Match has no vocabulary for "was this specific
// resource ID excluded" or "is today inside a declared freeze window" — and
// it should not grow one, because a specification-level exclusion must not
// be expressible or overridable from inside a policy rule. This package
// applies both as a post-processing tightening pass over the Decision
// govern.Evaluate already returned, using the same "most restrictive wins"
// rule the domain package itself uses for its own platform invariants: an
// exclusion or freeze can only make an outcome stricter, never looser.
//
// Traceability: REQ-GOV-001..011, SPEC-GOV-001, SPEC-AI-004.
package governance
