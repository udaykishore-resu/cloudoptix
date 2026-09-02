package ports

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// Role is a chat message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in a model conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a model's request to invoke a tool.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolDefinition describes a tool to the model.
//
// Every CloudOptix tool is read-only. There is no tool that mutates AWS, no
// tool that approves anything, and no tool that writes to the database. The
// model can look at the tenant's data and reason about it; the consequential
// paths are reached by structured output flowing through validation, policy
// and approval. This is the mechanical form of "AI-assisted, not
// AI-controlled".
//
// Traceability: REQ-AI-006, SPEC-AI-002.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
	// ReadOnly must be true for every registered tool. The registry rejects a
	// tool declaring otherwise, so the invariant cannot be broken by adding a
	// file.
	ReadOnly bool `json:"read_only"`
	// RequiredPermission is checked against the calling principal before the
	// tool runs, so the copilot can never read data the human could not.
	RequiredPermission core.Permission `json:"required_permission"`
}

// Tool is an executable read-only capability offered to the model.
type Tool interface {
	Definition() ToolDefinition
	Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error)
}

// CompletionRequest is one model invocation.
type CompletionRequest struct {
	// Purpose labels the call for metrics, cost attribution and rate limiting:
	// "onboarding", "copilot", "narrative", "extraction", "summarization".
	Purpose     string           `json:"purpose"`
	System      string           `json:"system"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	Temperature float64          `json:"temperature"`
	MaxTokens   int              `json:"max_tokens"`
	// ResponseSchema requests structured output conforming to a JSON Schema.
	// Extraction paths always set it: free prose is never parsed into a
	// specification.
	ResponseSchema map[string]any `json:"response_schema,omitempty"`
	StopSequences  []string       `json:"stop_sequences,omitempty"`
	TenantID       core.TenantID  `json:"tenant_id"`
}

// CompletionResponse is a model reply.
type CompletionResponse struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	StopReason   string     `json:"stop_reason"`
	InputTokens  int        `json:"input_tokens"`
	OutputTokens int        `json:"output_tokens"`
	Model        string     `json:"model"`
	LatencyMS    int64      `json:"latency_ms"`
	// Structured carries the parsed object when ResponseSchema was supplied.
	Structured map[string]any `json:"structured,omitempty"`
}

// LLMProvider is the model abstraction.
//
// Three implementations ship: Anthropic, Amazon Bedrock, and a deterministic
// scripted provider. The deterministic one is not a stub — it drives the demo
// tenant and the whole test suite, which means every AI-dependent path in
// CloudOptix is exercised in CI with no API key and no non-determinism.
type LLMProvider interface {
	Name() string
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	// Embed returns vectors for RAG indexing and retrieval.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Healthy reports provider availability, so the copilot can degrade to
	// deterministic answers rather than erroring when the model is down.
	Healthy(ctx context.Context) bool
}

// Document is one RAG corpus entry.
type Document struct {
	ID        string            `json:"id"`
	TenantID  core.TenantID     `json:"tenant_id"` // empty for platform-wide knowledge
	Source    string            `json:"source"`    // "aws_docs" | "finops" | "cloudoptix_rules" | "tenant_policy" | "outcomes"
	Title     string            `json:"title"`
	Content   string            `json:"content"`
	URL       string            `json:"url,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Embedding []float32         `json:"-"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// RetrievedDocument is a search hit with its score.
type RetrievedDocument struct {
	Document Document `json:"document"`
	Score    float64  `json:"score"`
	Snippet  string   `json:"snippet"`
}

// KnowledgeStore is the RAG index.
//
// Retrieval is tenant-partitioned: a query returns platform knowledge plus the
// querying tenant's own documents, never another tenant's. The partition is
// enforced in the store rather than in a filter the caller passes, because a
// forgotten filter here would leak one customer's architecture notes into
// another's copilot answer.
type KnowledgeStore interface {
	Index(ctx context.Context, docs []Document) error
	Search(ctx context.Context, tenant core.TenantID, query string, k int, sources []string) ([]RetrievedDocument, error)
	Delete(ctx context.Context, tenant core.TenantID, ids []string) error
	Count(ctx context.Context, tenant core.TenantID) (int, error)
}

// ConversationKind separates the two chat surfaces, which have different
// safety properties: onboarding produces a specification draft, the copilot
// produces only prose and read-only tool calls.
type ConversationKind string

const (
	ConversationOnboarding ConversationKind = "onboarding"
	ConversationCopilot    ConversationKind = "copilot"
)

// Conversation is a stored chat session.
type Conversation struct {
	ID       core.ID          `json:"id"`
	TenantID core.TenantID    `json:"tenant_id"`
	Kind     ConversationKind `json:"kind"`
	Title    string           `json:"title"`
	Actor    string           `json:"actor"`

	Turns []Turn `json:"turns"`

	// SpecID links an onboarding conversation to the draft it is building, so
	// a reviewer of specification v3 can read the conversation that produced
	// it.
	SpecID    core.ID   `json:"spec_id,omitempty"`
	State     string    `json:"state"` // active | awaiting_review | completed | abandoned
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Turn is one exchange, stored with everything needed to explain the answer.
type Turn struct {
	ID      core.ID   `json:"id"`
	Role    Role      `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`

	// ToolCalls and Retrieved record what the assistant looked at, which is
	// what makes a copilot answer auditable. A user can always ask "where did
	// that number come from" and get the tool call and its result.
	ToolCalls   []ToolCall          `json:"tool_calls,omitempty"`
	ToolResults []ToolResult        `json:"tool_results,omitempty"`
	Retrieved   []RetrievedDocument `json:"retrieved,omitempty"`
	// Citations are the concrete data references behind the answer.
	Citations []Citation `json:"citations,omitempty"`

	// SpecPatch records the structured extraction applied to the draft
	// specification on this turn, if any.
	SpecPatch  []spec.Change              `json:"spec_patch,omitempty"`
	Provenance map[string]core.Provenance `json:"provenance,omitempty"`

	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	LatencyMS    int64  `json:"latency_ms,omitempty"`
	Model        string `json:"model,omitempty"`
	// Grounded reports whether every factual claim resolved to tenant data.
	// An ungrounded answer is either rejected or served with an explicit
	// caveat, never presented as fact.
	Grounded        bool     `json:"grounded"`
	GroundingIssues []string `json:"grounding_issues,omitempty"`
	Degraded        bool     `json:"degraded"` // answered without the model
}

// ToolResult is a tool invocation's outcome.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Result     any    `json:"result"`
	Error      string `json:"error,omitempty"`
	LatencyMS  int64  `json:"latency_ms"`
}

// Citation is a reference to a concrete piece of tenant data behind an answer.
type Citation struct {
	Kind  string `json:"kind"` // "resource" | "cost_record" | "recommendation" | "document" | "metric"
	ID    string `json:"id"`
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
	URL   string `json:"url,omitempty"`
}

// ConversationRepository stores chat sessions.
type ConversationRepository interface {
	Create(ctx context.Context, c Conversation) error
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (Conversation, error)
	AppendTurn(ctx context.Context, tenant core.TenantID, id core.ID, t Turn) error
	Update(ctx context.Context, c Conversation) error
	List(ctx context.Context, tenant core.TenantID, kind ConversationKind, opts ListOptions) (Page[Conversation], error)
}

// GroundingVerifier checks that model output references only entities that
// exist in the tenant's data.
//
// It is the tripwire between "the model wrote a plausible sentence" and
// "CloudOptix asserted a fact". Every identifier, resource name and monetary
// figure in an answer is resolved against the tenant's actual model; anything
// unresolvable is reported, and the answer is either rejected or downgraded to
// an explicitly-caveated one.
//
// Traceability: REQ-AI-008, SPEC-AI-003.
type GroundingVerifier interface {
	Verify(ctx context.Context, tenant core.TenantID, answer string, allowed GroundingSet) (GroundingReport, error)
}

// GroundingSet is the universe of entities an answer may reference.
type GroundingSet struct {
	ResourceIDs     map[string]string // native id or CloudOptix id -> display name
	ResourceNames   map[string]bool
	Services        map[string]bool
	Amounts         []core.Money
	Recommendations map[string]bool
	Applications    map[string]bool
	Transactions    map[string]bool
}

// GroundingReport is the verification outcome.
type GroundingReport struct {
	Grounded          bool     `json:"grounded"`
	UnknownResources  []string `json:"unknown_resources,omitempty"`
	UnverifiedAmounts []string `json:"unverified_amounts,omitempty"`
	Issues            []string `json:"issues,omitempty"`
	// Confidence is the share of checkable claims that resolved.
	Confidence float64 `json:"confidence"`
}
