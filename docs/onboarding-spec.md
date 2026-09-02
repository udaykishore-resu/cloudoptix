# Onboarding specification

Covers `SPEC-ONB-001..006`, implemented by `internal/application/onboarding` and `internal/domain/spec`.

## SPEC-ONB-001 — The specification artefact and its lifecycle

`spec.Spec` (`internal/domain/spec/spec.go`) is the pivot of the whole product: a conversation cannot change infrastructure, only produce a draft specification, which a human reads, edits and approves. `spec.Version` wraps a `Spec` with `Status` (`draft → validating → pending_review → approved | rejected | superseded`), a `Checksum`, a `Diff` against its parent, a `Validation` result and a `Completeness` snapshot. Only `StatusDraft` is mutable (`Status.Mutable()`); every other status is terminal. `Version.Approve` is the only transition that makes a specification authoritative — it requires `StatusPendingReview`, calls `spec.Validate()` and refuses over any blocking issue, and records who took responsibility (`ApprovedBy`, `ApprovalID`, `ApprovedAt`).

## SPEC-ONB-002 — Provenance

Every value the agent records carries a `core.Provenance`:

| Provenance | Meaning | Consequence |
|---|---|---|
| `CONFIRMED` | A human stated it, or an authoritative API returned it | Safe to act on |
| `INFERRED` | CloudOptix derived it from other evidence, with a one-sentence `Rationale` | Usable for analysis; never sufficient alone for a production mutation |
| `UNKNOWN` | Asked and the user did not know, or discovery could not determine it | Every engine reading it must degrade gracefully, never substitute a silent default |
| `REQUIRES_USER_CONFIRMATION` | CloudOptix proposes a default whose consequences are large enough that a human must sign off | Blocks the field from being treated as `CONFIRMED` until accepted or overridden |

`spec.Field[T]` wraps every value with its `Provenance`, `Source` (`"user"`, `"aws_discovery"`, `"inference:tag_convention"`, `"default"`), and `Rationale`. This is deliberately a wrapper, not a bare value with provenance tracked elsewhere: it is what lets the review screen show "we inferred this" next to every inferred field, and lets the agent know what it still needs to ask about. `Spec.Provenance` additionally keeps a flattened per-path provenance map alongside the typed fields, so the `cloudoptix.yaml` a customer commits to git stays clean (no provenance noise in the file itself) while the review UI stays fully informative.

## SPEC-ONB-003 — The eight-stage conversational agent

The agent moves through **organization → application → aws → workloads → business → objectives → governance → review**, but the stage order only governs which question is asked next, not what may be recorded — every turn re-extracts structured fields from the **full** conversation so far (`REQ-ONB-002`), so an answer volunteered ahead of schedule ("we're a Series C fintech doing 40k payments a month, and we need SOC2") is captured immediately rather than dropped until the agent happens to reach that stage.

**Extraction is never prose parsing.** Every turn builds a JSON Schema from the fields the agent still cares about (`internal/application/onboarding/schema.go`) and calls `ports.LLMProvider.Complete` with `ResponseSchema` set, so the answer is always a structured object. Against the deterministic provider that structured object comes from a battery of independent regex/keyword extractors keyed to the schema's property names, returning only fields with positive evidence — exactly like a real model's structured output would omit fields it found no support for. Against Anthropic or Bedrock it comes from the model's own forced tool-use structured output. Either way, `application.go` applies the **same interpreter** to the result, so onboarding behaves identically regardless of which provider is behind it — the property that lets the whole test suite and the public demo tenant run with no API key.

## SPEC-ONB-004 — Deterministic pre-approval validation

`spec.Validate()` (`internal/domain/spec/validate.go`) is where the conversational, forgiving half of onboarding meets the exact half. It performs deterministic, no-model checks: account ID format (`^\d{12}$`), region format, IAM role ARN format, email format, maintenance-window time format, valid environment/risk-tolerance enum membership, presence of an organization name, presence of an external ID when an AWS account is declared. Every check exists because a mistake there would cost the customer money or safety — a malformed account ID means discovery finds nothing; a missing external ID is a confused-deputy exposure; automation enabled with no maintenance window means changes at peak traffic. Each issue carries a `core.Severity`; `ValidationResult.HasBlocking()` is what `Version.Approve` checks before allowing the transition.

## SPEC-ONB-005 — Structural diffing

`spec.Diff(before, after)` flattens both specification versions into dotted paths and compares values, so a reordered YAML block produces no diff and a changed field always does. Each `Change` is annotated with `Impact` — plain language explaining the downstream consequence, because `availabilityTarget: 0.999 -> 0.9999` means nothing to a reviewer unless told it will suppress an entire class of single-AZ recommendations. `SortChanges` orders a diff by severity then path, so a reviewer approving version 4 sees the consequential edits first.

## SPEC-ONB-006 — Resumability

`Spec.OpenQuestions` (`[]OpenQuestion`, each with `Path`, `Question`, `Why`, `Required`, `Blocking`, `Options`, `AskedCount`) is persisted as part of the spec, not held in ephemeral conversation state — so an onboarding interrupted mid-conversation resumes days later, on another device, from exactly where it left off, with the agent able to reconstruct what it still needs to ask without re-deriving it from the transcript.

## What the agent cannot do

Nothing about a draft specification can itself create a tenant, grant AWS access, or configure automation. Only `Approve` does that, and `Approve` runs `spec.Validate()` first and refuses to proceed over any blocking issue. This is the mechanical form of "AI-assisted, not AI-controlled" at the very first point a tenant meets the platform.

## Worked example

See the root README's [example onboarding conversation](../README.md#example-onboarding-conversation) and [example specification](../README.md#example-specification), and the fuller transcripts in [`examples/onboarding/`](../examples/onboarding/).

## Current limitations

- The eight-stage flow, extraction schema, and deterministic provider's regex slot-filling are all implemented and tested (`internal/application/onboarding/{service,extraction}_test.go`), but have never been exercised against a real user in a live session — only in test harnesses and the deterministic-provider demo path.
- `internal/domain/spec` has no test file of its own (see [`docs/traceability.md`](traceability.md), Flagged IDs); its correctness is verified only indirectly through onboarding's own tests.
- Provider parity between the deterministic extractor and a real model's structured output is asserted by design (same interpreter, same schema) but not verified by a side-by-side comparison against a live Anthropic/Bedrock call in this environment.
