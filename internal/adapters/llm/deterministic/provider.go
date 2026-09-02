package deterministic

import (
	"context"
	"encoding/json"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/rag"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// MaxToolRounds caps how many tools the agentic path (toolmatch.go,
// answer.go) will call before it composes a final answer regardless of what
// else might match. A real model under an agentic-loop caller is bounded the
// same way (see internal/application/copilot's round budget); this provider
// enforces its own so a caller that forgets its own cap still terminates.
const MaxToolRounds = 3

// Provider is the platform default ports.LLMProvider: no network call, fully
// deterministic, seeded to drive onboarding extraction and the copilot's
// agentic loop end to end. See the package doc for why this is not a mock.
type Provider struct {
	// ModelName is reported by Name() and CompletionResponse.Model, purely
	// for logging and metrics parity with the network-backed providers.
	ModelName string
	embedder  rag.HashEmbedder
}

var _ ports.LLMProvider = (*Provider)(nil)

// New builds the deterministic provider.
func New() *Provider {
	return &Provider{ModelName: "cloudoptix-deterministic-v1", embedder: rag.NewHashEmbedder()}
}

// Name satisfies ports.LLMProvider.
func (p *Provider) Name() string { return p.ModelName }

// Healthy always reports true: this provider makes no network call, so it
// has nothing to be unhealthy about. It is precisely this property that lets
// internal/adapters/llm/fallback treat it as the guaranteed-available floor.
func (p *Provider) Healthy(ctx context.Context) bool { return true }

// Embed delegates to the same deterministic hashing embedder
// internal/adapters/rag ships as its offline fallback, so the demo tenant's
// retrieval quality is identical whether or not this provider is also
// serving completions.
func (p *Provider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return p.embedder.Embed(ctx, texts)
}

// Complete satisfies ports.LLMProvider. See the package doc for the
// three-way dispatch: structured extraction, agentic tool routing, or a
// fixed honest fallback.
func (p *Provider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	start := time.Now()
	var resp ports.CompletionResponse

	switch {
	case req.ResponseSchema != nil:
		resp = p.completeExtraction(req)
	case len(req.Tools) > 0:
		resp = p.completeAgentic(req)
	default:
		resp = p.completeGeneric(req)
	}

	resp.Model = p.ModelName
	resp.LatencyMS = time.Since(start).Milliseconds()
	resp.InputTokens = approxTokens(req)
	resp.OutputTokens = approxOutputTokens(resp)
	return resp, nil
}

// completeExtraction runs the rule-based slot-filler against the schema and
// returns its findings as Structured, exactly as a real model's forced
// tool-use structured-output call would.
func (p *Provider) completeExtraction(req ports.CompletionRequest) ports.CompletionResponse {
	structured := Extract(req.ResponseSchema, req.Messages)
	return ports.CompletionResponse{
		StopReason: "tool_use",
		Structured: structured,
	}
}

// completeAgentic drives the copilot's tool loop: call the next unasked,
// relevant tool, or compose a final answer once enough evidence has been
// gathered. See toolmatch.go and answer.go.
func (p *Provider) completeAgentic(req ports.CompletionRequest) ports.CompletionResponse {
	question := latestUserMessage(req.Messages)
	results := collectToolResults(req.Messages)
	called := calledToolNames(results)

	if len(called) < MaxToolRounds {
		matches := matchTools(question, req.Tools)
		for _, m := range matches {
			if called[m.name] {
				continue
			}
			return ports.CompletionResponse{
				StopReason: "tool_use",
				ToolCalls: []ports.ToolCall{{
					ID:        "det_" + m.name,
					Name:      m.name,
					Arguments: buildArgs(m.name, question),
				}},
			}
		}
		// No keyword matched anything at all and nothing has been tried yet:
		// fall back to search_knowledge, the one tool that always has
		// something to say, rather than answering with zero grounding.
		if len(called) == 0 {
			for _, t := range req.Tools {
				if t.Name == "search_knowledge" {
					return ports.CompletionResponse{
						StopReason: "tool_use",
						ToolCalls: []ports.ToolCall{{
							ID:        "det_search_knowledge",
							Name:      "search_knowledge",
							Arguments: buildArgs("search_knowledge", question),
						}},
					}
				}
			}
		}
	}

	return ports.CompletionResponse{
		StopReason: "end_turn",
		Content:    composeAnswer(question, results),
	}
}

// completeGeneric handles a plain completion request with neither a schema
// nor tools — for example a narrative/explanation call. It never fabricates
// a specific fact; it states plainly that a live model is needed for
// free-form generation and, when the caller supplied a system prompt or a
// question, echoes back what it understood so the caller can still show the
// user something coherent in Degraded mode.
func (p *Provider) completeGeneric(req ports.CompletionRequest) ports.CompletionResponse {
	q := latestUserMessage(req.Messages)
	if q == "" {
		return ports.CompletionResponse{StopReason: "end_turn", Content: ""}
	}
	return ports.CompletionResponse{
		StopReason: "end_turn",
		Content:    "Running in deterministic mode: " + q,
	}
}

func latestUserMessage(msgs []ports.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == ports.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// approxTokens and approxOutputTokens give the deterministic provider
// plausible, stable token counts for cost accounting and quota tests, using
// the same rough "4 characters per token" heuristic commonly used to
// estimate token counts without a real tokenizer — accurate enough for rate
// limiting and dashboards, and perfectly reproducible.
func approxTokens(req ports.CompletionRequest) int {
	n := len(req.System)
	for _, m := range req.Messages {
		n += len(m.Content)
	}
	if raw, err := json.Marshal(req.Tools); err == nil {
		n += len(raw)
	}
	return n/4 + 1
}

func approxOutputTokens(resp ports.CompletionResponse) int {
	n := len(resp.Content)
	if raw, err := json.Marshal(resp.Structured); err == nil {
		n += len(raw)
	}
	if raw, err := json.Marshal(resp.ToolCalls); err == nil {
		n += len(raw)
	}
	return n/4 + 1
}
