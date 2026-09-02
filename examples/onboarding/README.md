# Onboarding transcripts

Four full onboarding conversations, each producing a specification and
ending with a provenance breakdown of every field the conversation set.
Each corresponds to one of the four worked specifications in
[`specs/examples/`](../../specs/examples/), showing the conversation that
would plausibly produce an estate of that shape — though, as with those
files, the specification each transcript below produces is a smaller
first-draft than the fully-populated `specs/examples/` file, since a real
onboarding conversation does not extract every field in one sitting (see
`docs/onboarding-spec.md`, SPEC-ONB-006, on resumability).

| Transcript | Scenario | Companion spec in `specs/examples/` |
|---|---|---|
| [`meridian-retail.md`](meridian-retail.md) | Mid-market e-commerce, single AWS account, conservative posture at first approval | The root README's own worked example; a later, discovery-informed revision of this same tenant is [`specs/v1/cloudoptix.yaml`](../../specs/v1/cloudoptix.yaml) |
| [`northfield-commerce.md`](northfield-commerce.md) | Larger multi-service retail platform, aggressive optimization appetite | [`specs/examples/production-ecommerce.yaml`](../../specs/examples/production-ecommerce.yaml) |
| [`ashcroft-custodial.md`](ashcroft-custodial.md) | Regulated financial-services ledger platform | [`specs/examples/regulated-financial-services.yaml`](../../specs/examples/regulated-financial-services.yaml) |
| [`loopwave.md`](loopwave.md) | Five-person serverless startup | [`specs/examples/serverless-startup.yaml`](../../specs/examples/serverless-startup.yaml) |

Every transcript below is against `internal/adapters/llm/deterministic` — the
scripted, seeded provider described in `docs/ai-spec.md` — not a live model
call. That provider is not a mock: it runs the same regex/keyword extraction
battery and the same `internal/application/onboarding` interpreter a real
Anthropic or Bedrock call would go through, which is why these transcripts
read as plausible, structured extraction rather than canned dialogue. See
`docs/onboarding-spec.md`, SPEC-ONB-003, for why the interpreter is
guaranteed identical regardless of which provider produced the structured
turn.
