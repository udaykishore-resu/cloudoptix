# ADR-0002: Spec-driven onboarding, not direct provisioning from a conversation

## Status

Accepted, implemented.

## Context

Onboarding a tenant means gathering a large amount of structured information — company context, AWS accounts, architecture, business transactions, cost objectives, risk tolerance, governance posture — most naturally through a conversation. The tempting shortcut is to let that conversation directly create a tenant, connect AWS accounts, and configure automation as it goes.

## Decision

Every conversational turn writes to a mutable, versioned `spec.Spec` draft (`spec.StatusDraft`). Nothing about a draft can create a tenant, grant AWS access, or configure automation — only a human-initiated `Approve` call does that, and `Approve` runs `spec.Validate()` first and refuses to proceed over any blocking issue. The specification is designed to round-trip exactly through `cloudoptix.yaml`, intended to be committed to a customer's git repository like any other infrastructure artifact.

## Consequences

**Positive:**
- The conversational agent can be "pleasant and forgiving" — inferring, proposing, guessing at industry-typical defaults — because none of that is consequential until a human reviews and approves it. See [ADR-0005](0005-deterministic-rules-ai-narration.md) for why this matters for AI-safety reasons specifically, not only product-UX ones.
- Every value carries `core.Provenance`, so a reviewer can distinguish "the user said this" from "CloudOptix guessed this" at the field level, not just at the document level.
- A specification version is diffable (`spec.Diff`) and auditable — a change to `automation.enabled` from `false` to `true` is a reviewable event with a stated `Impact`, not a silent config mutation.
- The spec becomes the single configuration source every downstream engine reads from — discovery scope, policy defaults, automation posture, cost SLOs are all derived from one object, not scattered across separate onboarding steps each with their own persistence.

**Negative:**
- Onboarding is slower to "first value" than a hypothetical flow that starts discovering resources the moment a user mentions an AWS account, since nothing runs until a spec is approved.
- The specification format itself becomes a versioned contract (`spec.CurrentAPIVersion = "cloudoptix.io/v1"`) that has to be maintained and migrated over time, an ongoing cost a direct-provisioning approach would not carry.

## Alternatives considered

**Direct provisioning from conversation state.** Rejected: it collapses the review step that is the entire point of the design — a tenant would have live AWS access and automation configured before anyone had looked at what the agent inferred. This is also the specific alternative [ADR-0005](0005-deterministic-rules-ai-narration.md) generalizes: nothing AI-touched should be one step away from a real-world consequence.

**A wizard-style form instead of a conversation.** Rejected as the *only* interface (not rejected as a complement — `spec.Spec` supports `POST /specs/import` for exactly this): a form does not naturally support "the user volunteers information ahead of schedule" (`REQ-ONB-002`'s full-conversation re-extraction) or a mid-conversation revision the way a conversational agent does, and a form still needs the same draft/approve/validate machinery underneath it, so the conversation is additive value on top of the same spec-driven core, not a replacement for it.
