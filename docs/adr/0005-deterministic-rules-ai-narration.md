# ADR-0005: Deterministic rules with AI narration, not AI-generated recommendations

## Status

Accepted, implemented.

## Context

An LLM is a natural fit for parsing a conversation, drafting a narrative, or summarizing a large amount of structured data into something a human wants to read. It is also easy to reach for it to *decide* things: "does this recommendation look safe," "how confident should we be," "should this auto-execute." Every one of those decisions has real-world consequences once execution is wired up.

## Decision

Every consequential decision in CloudOptix — a Finding, a confidence score, a risk assessment, a blast radius, a policy `Effect`, an approval requirement — is computed by a deterministic, pure function of structured facts, with no model call anywhere in the computation. An LLM may narrate a recommendation, extract structured fields from a conversation (through a JSON-Schema-constrained call, never free parsing), rank candidate answers to a copilot question, or compose a final answer from tool results — it never produces the number a policy decision, an approval requirement, or an execution plan is built on.

Concretely: `optimize.Rule` fires on evidence to produce a `Finding` (never asks a model "is this worth flagging"); `ComputeConfidence`/`ComputeRiskAssessment`/`ComputeBlastRadius` are each independently testable Go functions over structured inputs (never an LLM self-report); `govern.Evaluate` is a pure function over `govern.Input`, with no field on that struct capable of carrying free text; the copilot's tools are all read-only and its final answer passes a grounding verifier before being returned.

## Consequences

**Positive:**
- Reproducibility: the same inputs produce the same Finding, the same confidence, the same policy decision, months apart — the property an audit actually needs, and the property that makes CloudOptix's own decisions defensible in a way "the model said so" cannot be.
- Testability: `internal/application/optimization/confidence.go`, `blast.go`, `risk.go` and `internal/domain/govern/policy.go` are each unit-testable with plain Go test cases, no model mocking, no flakiness from sampling temperature.
- A calibrated confidence score genuinely reflects evidence quality (metric stability, coverage, dependency-graph completeness) rather than "how confident-sounding was the model's training distribution on similar text" — the specific failure mode `confidence.go`'s own doc comment names.
- The learning loop (`internal/application/learning`) can safely run unattended precisely because it only ever adjusts a *calibration multiplier* on top of this deterministic scoring, never the scoring logic or the policy that consumes it — see [`optimization-spec.md`](../optimization-spec.md)'s `SPEC-OPT-008` section.

**Negative:**
- A genuinely novel pattern an LLM might have flagged from broader "intuition" — one that does not fit any of the 48 rule pack entries — is invisible to the platform until a human writes a new rule. The rule pack is comprehensive but not exhaustive, and nothing in this architecture auto-discovers a new rule category.
- Writing a new rule is a Go code change (a new `rule_*.go` file plus a YAML entry), a materially higher bar than "adjust a prompt" — a deliberate cost, but a real one, for adding a new category of finding.
- Narration quality is decoupled from decision quality: a beautifully-narrated recommendation and a poorly-narrated one carry identical confidence/risk/blast-radius numbers if the underlying facts are identical, which is correct but can feel like a missed opportunity to use the model's own judgement more fully.

## Alternatives considered

**LLM-scored confidence and risk.** Rejected for the reason stated in `confidence.go`'s own doc comment: a model asked "how confident are you" answers from its training distribution of confident-sounding text, uncorrelated with whether the underlying telemetry actually supports the claim.

**An LLM-in-the-loop policy engine** (a model reviewing each recommendation against policy text and deciding). Rejected: this would make `govern.Evaluate` non-reproducible and would reintroduce exactly the prompt-injection surface `internal/adapters/llm/middleware`'s sanitization exists to guard against at the tool-result boundary — a policy decision must not be one adversarially-crafted resource tag away from a different outcome.

**A hybrid where the model's opinion is one input alongside the deterministic score.** Considered, rejected for this codebase: even as "one input among several," a model's opinion would need its own trust calibration, its own failure mode analysis, and its own audit story — the same complexity as making it the sole decision-maker, for a benefit ("catch something the rules missed") better captured by expanding rule coverage, which is auditable, than by blending in an unauditable signal.
