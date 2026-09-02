// Package llm is the namespace for CloudOptix's ports.LLMProvider
// implementations and the middleware that wraps them. It holds no code of its
// own — each concern lives in its own subpackage so that a caller pulls in
// only the transport it needs:
//
//   - anthropic    — ports.LLMProvider over the Anthropic Messages API.
//   - bedrock      — ports.LLMProvider over Amazon Bedrock's InvokeModel API,
//     SigV4-signed, serving the same Anthropic model family through AWS.
//   - anthropicwire — the request/response wire format shared by anthropic
//     and bedrock, since Bedrock's Anthropic models speak the same message
//     JSON shape as the direct API with a thin wrapper.
//   - deterministic — a scripted, seeded provider that is the platform
//     default: no API key, fully reproducible, and capable enough to drive
//     onboarding and the copilot end to end. See deterministic's package doc
//     for why this is not a mock.
//   - middleware   — a provider decorator chain: tracing, metrics, per-tenant
//     rate limiting and quota, a circuit breaker, a response cache, audit
//     logging, and prompt-injection defence on tool results.
//   - fallback     — a provider that degrades to the deterministic provider
//     when a primary provider is unhealthy, so a model outage never takes
//     the platform down.
//
// # Governing principle
//
// Every provider in this tree implements exactly ports.LLMProvider: Complete,
// Embed, Healthy. None of them can execute infrastructure, approve anything,
// or write to a repository — the interface has no such method, so the
// constraint is structural rather than a convention providers are trusted to
// honour. A provider may draft, extract, summarize, rank and explain; the
// consequential paths (spec approval, policy evaluation, execution) live
// entirely outside this tree and read only validated, structured data that
// happens to have been proposed by a model. This is the mechanical form of
// "AI-assisted, not AI-controlled" at the boundary where the platform
// actually talks to a model.
//
// Traceability: REQ-AI-001..009, SPEC-AI-001..004.
package llm
