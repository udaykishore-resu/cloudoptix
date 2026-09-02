// Package copilot implements ports.CopilotService: the AI Cost Copilot that
// answers questions about a tenant's cloud spend, waste, unit economics and
// optimization opportunities by calling a fixed set of read-only tools and
// composing the answer from what they returned.
//
// KEY DESIGN DECISION — every tool is read-only, and the registry enforces
// it structurally. Every ports.Tool this package registers declares
// ToolDefinition.ReadOnly: true; Register refuses — at registration time,
// not at call time — anything that claims otherwise. There is no tool here
// that writes a resource, approves a recommendation, or changes a policy:
// the copilot can look at a tenant's data and explain it, but the only path
// from "the copilot suggested it" to "it happened" runs back through the
// same specification, policy and approval machinery any other change does.
// This is the mechanical form of "AI-assisted, not AI-controlled" for the
// copilot surface specifically.
//
// The agentic loop (service.go) is provider-agnostic: it calls
// ports.LLMProvider.Complete with the registry's tool definitions and
// executes whatever ToolCall the provider returns, bounded to a fixed
// number of rounds. Against the deterministic provider this produces
// keyword-routed tool calls and a template-composed answer with no API key
// required (see internal/adapters/llm/deterministic); against Anthropic or
// Bedrock the same loop drives real function-calling. Either way, every
// tool result is appended to the conversation as a
// Role==RoleTool/Name=<tool>/Content=<JSON with a "summary" field> message —
// the one convention both the deterministic provider's answer composer and
// this package's own final-answer assembly rely on.
//
// Before an answer is returned it passes through a GroundingVerifier
// (grounding.go): every resource id, account id and dollar figure in the
// text is checked against what the tools actually returned this
// conversation. An answer that references something ungrounded is
// regenerated once; if the retry is still ungrounded, the answer is
// returned with an explicit caveat rather than presented as fact.
//
// Traceability: REQ-AI-006..010, REQ-COP-001..008, SPEC-AI-002, SPEC-AI-003.
package copilot
