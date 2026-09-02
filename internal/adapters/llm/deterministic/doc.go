// Package deterministic implements ports.LLMProvider without calling any
// model: a rule-based intent classifier and slot-filler for structured
// extraction, a keyword-driven tool router and a template-driven answer
// composer for the agentic copilot loop, and a fixed, seeded response for
// anything else. It is the platform's default provider.
//
// # Why this is not a mock
//
// A mock LLM provider returns canned strings regardless of input, which is
// enough to test that a caller handles *a* response but tells you nothing
// about whether the caller's actual extraction, question-routing or
// answer-grounding logic works. This provider is judged by a different bar:
// it must independently drive a realistic multi-turn onboarding conversation
// from "we're an e-commerce company called Meridian Retail" to a complete,
// valid specification, and it must answer the copilot's promised questions —
// why did cost increase, what's wasting money, which service is most
// expensive — by reading the *actual* numbers out of the tool results it is
// handed, not by returning a fixed sentence. Every AI-dependent code path in
// CloudOptix — onboarding's stage machine, extraction, inference; the
// copilot's tool selection, grounding verification, degraded mode — runs
// against this provider in CI, with no API key, and produces the same output
// on the same input every time. That is what makes those paths testable at
// all without a live model in the loop, and it is also exactly what runs the
// public demo tenant.
//
// # How Complete decides what to do
//
// Complete inspects the CompletionRequest, not a stored conversation state —
// like a real provider, this one is stateless between calls and reconstructs
// everything it needs from req.Messages on every invocation:
//
//   - ResponseSchema set: extraction mode (extract.go). The last user message
//     is scanned by a battery of independent regex/keyword extractors keyed
//     to the property names of CloudOptix's onboarding extraction schema
//     (internal/application/onboarding/schema.go); only fields with positive
//     evidence are returned, exactly like structured output from a real model
//     would omit fields it found no support for.
//   - Tools set, no ResponseSchema: agentic mode (toolmatch.go, answer.go).
//     The question is matched against a keyword table to the most relevant
//     tool not yet called in this exchange (calls already made are read back
//     from the Role==RoleTool messages already in req.Messages, keyed by
//     Message.Name); once at least one tool has answered, or a small round
//     budget is exhausted, a templated final answer is composed by pulling
//     concrete figures out of the accumulated tool-result JSON.
//   - Neither: a short, honest, fixed reply naming what would need an actual
//     model to answer further — never a fabricated fact.
//
// Traceability: REQ-AI-003, REQ-AI-007, REQ-ONB-002, SPEC-AI-001.
package deterministic
