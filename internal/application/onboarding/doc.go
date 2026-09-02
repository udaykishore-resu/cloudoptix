// Package onboarding implements ports.OnboardingService: the conversational
// agent that turns a plain-language description of a company and its AWS
// estate into a reviewable, versioned spec.Spec.
//
// The agent moves through eight stages — organization, application, aws,
// workloads, business, objectives, governance, review — but the stage order
// only governs which questions it asks next, not what it is willing to
// record: every turn re-extracts structured fields from the FULL
// conversation so far, so an answer volunteered ahead of schedule ("we're a
// Series C fintech doing 40k payments a month, and we need SOC2") is
// captured immediately rather than being dropped until the agent happens to
// reach that stage.
//
// KEY DESIGN DECISION: extraction is never prose parsing. Every turn builds
// a JSON Schema from the fields the agent still cares about (schema.go) and
// calls ports.LLMProvider.Complete with ResponseSchema set, so the answer is
// always a structured object, never text the agent has to interpret itself.
// Against the deterministic provider that structured object comes from
// regex slot-filling (internal/adapters/llm/deterministic); against
// Anthropic or Bedrock it comes from the model's own forced tool-use
// structured output. Either way, application.go applies the SAME
// interpreter to the result, so the onboarding flow behaves identically
// whichever provider is behind it — which is what lets the whole test suite,
// and the demo tenant, run onboarding with no API key at all.
//
// Every value the agent records carries a core.Provenance: CONFIRMED when
// the user stated it, INFERRED (with a one-sentence rationale) when the
// agent derived it from industry, size or other stated facts, UNKNOWN when
// the user was asked and said they didn't know, and
// REQUIRES_USER_CONFIRMATION when the agent proposes a default the user has
// not yet accepted or overridden. This is the mechanical form of
// "AI-assisted, not AI-controlled" at the very first point a tenant meets
// the platform: the agent may ask, extract, infer and summarize, but the
// specification it produces stays a DRAFT — spec.Status stays StatusDraft —
// until Approve is called by a human, and nothing about the draft can
// itself create a tenant, grant AWS access, or configure automation. Only
// Approve does that, and Approve runs spec.Validate() first and refuses to
// proceed over any blocking issue.
//
// Traceability: REQ-ONB-001..012, REQ-AI-001..003, SPEC-ONB-001..006.
package onboarding
