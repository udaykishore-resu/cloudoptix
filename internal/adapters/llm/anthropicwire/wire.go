// Package anthropicwire is the Anthropic Messages API request/response shape,
// shared verbatim between internal/adapters/llm/anthropic (the direct API)
// and internal/adapters/llm/bedrock (the same model family fronted by
// Bedrock's InvokeModel). Amazon ships Anthropic's Claude models on Bedrock
// with the identical message JSON shape as the direct API — only the
// transport (HTTPS + x-api-key versus SigV4-signed InvokeModel), the
// envelope (a top-level "model" field versus the model in the URL path plus
// an "anthropic_version" field) and authentication differ. Sharing this
// package is what keeps that duplication from spreading into two divergent
// copies of tool-call and structured-output handling.
package anthropicwire

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// StructuredToolName is the synthetic tool CloudOptix asks the model to call
// when a CompletionRequest carries a ResponseSchema. The Anthropic Messages
// API has no native "response_format" the way some other providers do; the
// documented, supported way to get schema-conformant JSON out of it is to
// describe the schema as a tool's input_schema and force tool_choice to that
// tool, so the model's only legal move is to emit arguments matching the
// schema. This is not a workaround bolted on top of the API — it is the
// mechanism Anthropic's own documentation recommends for structured output.
const StructuredToolName = "emit_structured_output"

// DefaultMaxTokens is used when a CompletionRequest does not set MaxTokens;
// the Anthropic API requires the field and rejects a request without it.
const DefaultMaxTokens = 1536

// Message is one turn in the wire format.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one piece of a message: text, a tool invocation the model
// is requesting, or the result of one CloudOptix already ran.
type ContentBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | "tool_result"

	Text string `json:"text,omitempty"` // type == text

	ID    string         `json:"id,omitempty"`    // type == tool_use
	Name  string         `json:"name,omitempty"`  // type == tool_use
	Input map[string]any `json:"input,omitempty"` // type == tool_use

	ToolUseID string `json:"tool_use_id,omitempty"` // type == tool_result
	Content   string `json:"content,omitempty"`     // type == tool_result
	IsError   bool   `json:"is_error,omitempty"`    // type == tool_result
}

// Tool is a tool definition in the wire format.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// ToolChoice steers whether and which tool the model must call.
type ToolChoice struct {
	Type string `json:"type"` // "auto" | "any" | "tool" | "none"
	Name string `json:"name,omitempty"`
}

// Request is the Messages API request body. Model and AnthropicVersion are
// mutually exclusive across transports: the direct API sets Model and omits
// AnthropicVersion (it is a URL-less top-level field there too, but Bedrock
// requires AnthropicVersion in the body instead of Model, since the model is
// already named in Bedrock's URL path). Both fields are therefore left to the
// caller to populate.
type Request struct {
	Model            string      `json:"model,omitempty"`
	AnthropicVersion string      `json:"anthropic_version,omitempty"`
	System           string      `json:"system,omitempty"`
	Messages         []Message   `json:"messages"`
	MaxTokens        int         `json:"max_tokens"`
	Temperature      float64     `json:"temperature,omitempty"`
	StopSequences    []string    `json:"stop_sequences,omitempty"`
	Tools            []Tool      `json:"tools,omitempty"`
	ToolChoice       *ToolChoice `json:"tool_choice,omitempty"`
}

// Usage is the token accounting the API returns.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the Messages API response body.
type Response struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// ErrorBody is the shape of an API error response.
type ErrorBody struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// BuildRequest converts a ports.CompletionRequest into the wire shape,
// merging the request's System field with any RoleSystem messages, coalescing
// consecutive same-role turns (the API requires strict user/assistant
// alternation, which a tool-call/tool-result exchange would otherwise
// violate), and — when ResponseSchema is set — forcing the model to answer
// through StructuredToolName instead of prose.
func BuildRequest(req ports.CompletionRequest) Request {
	out := Request{
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		StopSequences: req.StopSequences,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = DefaultMaxTokens
	}

	var systemParts []string
	if strings.TrimSpace(req.System) != "" {
		systemParts = append(systemParts, req.System)
	}

	var flat []Message
	for _, m := range req.Messages {
		switch m.Role {
		case ports.RoleSystem:
			if strings.TrimSpace(m.Content) != "" {
				systemParts = append(systemParts, m.Content)
			}
		case ports.RoleUser:
			flat = append(flat, Message{Role: "user", Content: []ContentBlock{{Type: "text", Text: m.Content}}})
		case ports.RoleAssistant:
			var blocks []ContentBlock
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, ContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, ContentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: tc.Arguments})
			}
			if len(blocks) == 0 {
				blocks = []ContentBlock{{Type: "text", Text: ""}}
			}
			flat = append(flat, Message{Role: "assistant", Content: blocks})
		case ports.RoleTool:
			content := m.Content
			flat = append(flat, Message{Role: "user", Content: []ContentBlock{{
				Type: "tool_result", ToolUseID: m.ToolCallID, Content: content,
			}}})
		}
	}
	out.Messages = coalesce(flat)
	out.System = strings.Join(systemParts, "\n\n")

	for _, td := range req.Tools {
		out.Tools = append(out.Tools, Tool{Name: td.Name, Description: td.Description, InputSchema: td.Parameters})
	}

	if req.ResponseSchema != nil {
		out.Tools = []Tool{{
			Name:        StructuredToolName,
			Description: "Emit the final answer as structured data conforming exactly to the provided schema.",
			InputSchema: req.ResponseSchema,
		}}
		out.ToolChoice = &ToolChoice{Type: "tool", Name: StructuredToolName}
	}
	return out
}

// coalesce merges consecutive same-role messages into one, concatenating
// their content blocks in order. Anthropic requires strict user/assistant
// alternation; a tool round produces assistant(tool_use) followed by
// user(tool_result), which already alternates, but two consecutive tool
// results (from a multi-tool round) would otherwise arrive as two separate
// user messages back to back, which the API rejects.
func coalesce(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	out = append(out, msgs[0])
	for _, m := range msgs[1:] {
		last := &out[len(out)-1]
		if last.Role == m.Role {
			last.Content = append(last.Content, m.Content...)
			continue
		}
		out = append(out, m)
	}
	return out
}

// ParseResponse converts a wire Response into a ports.CompletionResponse.
// When the model answered via StructuredToolName, its input becomes
// Structured and is not also surfaced as an ordinary ToolCall — the caller
// asked for structured output, not for a tool round.
func ParseResponse(resp Response, model string, latencyMS int64) ports.CompletionResponse {
	out := ports.CompletionResponse{
		StopReason:   resp.StopReason,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		Model:        firstNonEmpty(resp.Model, model),
		LatencyMS:    latencyMS,
	}
	var text strings.Builder
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			if block.Name == StructuredToolName {
				out.Structured = block.Input
				continue
			}
			out.ToolCalls = append(out.ToolCalls, ports.ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
		}
	}
	out.Content = text.String()
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ParseErrorBody attempts to decode an API error response into a readable
// message, falling back to the raw body when it is not the expected shape.
func ParseErrorBody(body []byte) string {
	var eb ErrorBody
	if err := json.Unmarshal(body, &eb); err == nil && eb.Error.Message != "" {
		return fmt.Sprintf("%s: %s", eb.Error.Type, eb.Error.Message)
	}
	return string(body)
}

// Backoff returns an exponential backoff delay with a fixed jitter-free
// doubling, capped at max. attempt is zero-based (the first retry passes 0).
// Jitter is intentionally omitted: both providers already serialise retries
// behind the middleware chain's circuit breaker and per-tenant rate limiter,
// which spread real traffic far more than a jittered sleep would, and a
// deterministic backoff keeps retry timing reproducible in tests.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}
