# Runbook: tenant onboarding support

For support staff helping a prospective or new tenant through onboarding — not for the platform-team incident responses the other runbooks in this directory cover.

## Common situation: a conversation is stuck / not progressing

1. `GET /onboarding/{conversationID}` returns the full state, including `Completeness` (confirmed/inferred/unknown/needs-confirmation counts, `ReadyForReview`) and `OpenQuestions` — start here rather than reading the raw transcript; it tells you exactly what the agent still needs and whether any of it is `Blocking`.
2. If `OpenQuestions` includes a `Blocking: true` entry the user hasn't addressed, that is why the conversation cannot reach `ReadyForReview` — walk the user through that specific question directly (referencing `Question`/`Why` from the open-question record) rather than asking them to "just keep talking to the agent," since the agent will keep circling back to the same blocking gap.
3. Onboarding is resumable by design (`SPEC-ONB-006`) — a conversation abandoned mid-flow and resumed days later, even from a different device, picks up exactly where it left off via `Spec.OpenQuestions`. If a user reports "it forgot everything," check that they are resuming the same `conversationID`, not starting a new one.

## Common situation: the user disagrees with an inferred value

Every inferred field carries a one-sentence `Rationale` (`core.Provenance = INFERRED`) — surface that rationale directly to the user rather than re-explaining the inference yourself; it is the platform's own stated reasoning and is usually the fastest way to either confirm the inference was reasonable or identify exactly what fact the user needs to correct. The user can override any inferred value at any point in the conversation — a later statement supersedes an earlier one (`REQ-ONB-008`), so simply having them state the correct value resolves it; no separate "edit" flow is required mid-conversation (though `PATCH /onboarding/{conversationID}` exists for direct edits outside the chat flow too).

## Common situation: "I don't know" answers

This is an expected, first-class path, not an error state — see the root README's [example onboarding conversation](../../README.md#example-onboarding-conversation) for a worked instance. The field is recorded `UNKNOWN`, and discovery is often able to resolve it once an AWS account is connected (the example conversation's `deploymentModel` field is a direct instance of this: left `UNKNOWN` in conversation, expected to be inferred later from what discovery actually finds running). Reassure the user that "I don't know" is a valid, complete answer for most fields — only a `Blocking: true` open question actually needs resolution before approval.

## Common situation: the user wants to approve before discovery has run

This is allowed and by design — `Version.Approve` requires `spec.Validate()` to pass with no blocking issues, which is independent of whether an AWS account has been connected or discovered yet. A tenant can be created, and the specification frozen, purely from conversational input; AWS account connection and discovery are separate, later steps (`POST /aws-accounts`, `POST /discovery/runs`) a tenant admin performs after tenant creation. Do not tell a user they need to connect AWS before approving — walk them through account connection as the natural next step *after* approval instead.

## Common situation: validation is blocking approval and the reason isn't obvious

`Version.Validation.Issues` (surfaced via `GET /onboarding/{conversationID}/summary` and `POST /specs/validate`) lists every issue with a `core.Severity` and a specific field path. The most common blocking causes, in practice:

- A malformed AWS account ID (must match `^\d{12}$` — a common user error is pasting an account alias or an ARN instead of the bare 12-digit ID).
- A declared AWS account with no `external_id` — required before any role can be verified (SPEC-ONB-004's confused-deputy check runs at validation time, not only at verification time, so this is caught early rather than failing later at `verify`).
- `automation.enabled: true` with no `maintenanceWindows` declared — the platform will not accept an automation-enabled spec with no window to constrain it to (this reasoning is stated directly in `spec.Validate()`'s own doc comment: "automation enabled with no maintenance window means changes at peak traffic").

Walk the user to the specific field named in the issue; do not guess.

## Common situation: the user wants to change risk posture or policy pack mid-conversation

Fully supported — see the root README's example conversation, which shows exactly this (revising a Cost SLO target mid-conversation). A later statement in the same conversation supersedes an earlier one for the same field; there is no need to restart. If the change happens *after* approval (a new requirement, not a support scenario covered by this runbook), that requires a new specification version — `POST /specs/revisions` — which will produce a structural diff (`spec.Diff`) with stated impact for the tenant admin to review and re-approve, not a silent in-place edit of the approved version.

## Escalation

If `spec.Validate()` reports a blocking issue that does not correspond to any of the documented checks in [`onboarding-spec.md`](../onboarding-spec.md)'s SPEC-ONB-004 section, or if the deterministic extraction visibly fails to capture information the user stated clearly and unambiguously, escalate to the team owning `internal/application/onboarding` with the `conversationID` and the specific turn where extraction diverged from what the user said.
