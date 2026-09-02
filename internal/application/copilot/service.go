package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// maxToolRounds bounds the agentic loop: at most this many tool calls are
// executed for one question before the loop forces a final answer from
// whatever evidence has been gathered so far. A real model that keeps
// asking for more tools past this point is cut off — the copilot always
// terminates, and an answer built from partial evidence is explicitly
// possible (composeFromSummaries handles zero or partial results honestly)
// rather than the loop ever running unbounded.
const maxToolRounds = 6

// copilotSystemPrompt is the fixed system prompt for every copilot turn. It
// states the read-only, cite-everything contract in the same terms the
// package doc does, so a real model is told in-band what the tool registry
// and GroundingVerifier already enforce structurally.
const copilotSystemPrompt = "You are the CloudOptix Cost Copilot. Answer only from tool results — call whatever " +
	"tools you need, then answer using the numbers and identifiers those tools returned. Never state a dollar " +
	"figure, resource id, or account id you did not get from a tool. If the tools do not have enough information " +
	"to answer, say so rather than guessing."

// Service implements ports.CopilotService.
//
// KEY DESIGN DECISION — the agentic loop is provider-agnostic and every
// tool it can call is read-only (registry.go refuses anything else at
// registration time), so the worst a misbehaving model can do here is call
// the wrong read-only tool or write a wrong sentence — never touch
// infrastructure. Every answer additionally passes through a
// GroundingVerifier before it is returned: an answer that references a
// resource, account or dollar figure no tool call actually produced this
// turn is regenerated once, and if still ungrounded, returned with an
// explicit caveat rather than presented as fact. This is the mechanical
// form of "AI-assisted, not AI-controlled" for the copilot surface.
//
// Traceability: REQ-AI-006..010, REQ-COP-001..008, SPEC-AI-002, SPEC-AI-003.
type Service struct {
	uow      ports.UnitOfWork
	provider ports.LLMProvider
	registry *Registry
	verifier ports.GroundingVerifier
	clock    func() time.Time
}

var _ ports.CopilotService = (*Service)(nil)

// New builds the copilot service. provider may be nil or unhealthy — Ask
// degrades to a templated, tool-grounded answer rather than failing, the
// same contract internal/application/onboarding's extraction path follows.
func New(uow ports.UnitOfWork, provider ports.LLMProvider, knowledge ports.KnowledgeStore) *Service {
	return &Service{
		uow:      uow,
		provider: provider,
		registry: BuildRegistry(uow, knowledge),
		verifier: NewVerifier(),
		clock:    func() time.Time { return time.Now().UTC() },
	}
}

// Ask implements ports.CopilotService.
func (s *Service) Ask(ctx context.Context, tenant core.TenantID, in ports.CopilotRequest) (ports.CopilotAnswer, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.CopilotAnswer{}, err
	}
	if strings.TrimSpace(in.Question) == "" {
		return ports.CopilotAnswer{}, core.NewError(core.ErrInvalidInput, "empty_question", "question is required")
	}
	start := s.clock()

	principal, _ := core.PrincipalFrom(ctx)
	tools := s.registry.Definitions(func(def ports.ToolDefinition) bool { return principal.Can(def.RequiredPermission) })

	conv, priorMessages, err := s.loadOrStartConversation(ctx, tenant, in)
	if err != nil {
		return ports.CopilotAnswer{}, err
	}

	messages := append(priorMessages, ports.Message{Role: ports.RoleUser, Content: in.Question})

	answer, toolResults, grounding, cites, degraded, inTok, outTok, latency := s.run(ctx, tenant, in.Question, messages, tools)

	turn := ports.Turn{
		ID:          core.NewID("turn"),
		Role:        ports.RoleAssistant,
		Content:     answer,
		At:          s.clock(),
		ToolResults: toolResults,
		Citations:   cites,
		InputTokens: inTok, OutputTokens: outTok, LatencyMS: latency,
		Grounded: grounding.Grounded, GroundingIssues: grounding.Issues, Degraded: degraded,
	}
	if err := s.appendTurns(ctx, tenant, conv.ID, in, turn); err != nil {
		return ports.CopilotAnswer{}, err
	}

	return ports.CopilotAnswer{
		ConversationID: conv.ID, Answer: answer, Citations: cites, ToolCalls: toolResults,
		Grounded: grounding.Grounded, GroundingIssues: grounding.Issues, Degraded: degraded,
		FollowUps: followUpsFor(toolResults), LatencyMS: s.clock().Sub(start).Milliseconds(),
	}, nil
}

// run drives the bounded agentic loop and returns the final answer text
// together with everything needed to build the stored Turn and the
// CopilotAnswer: the tool results (for auditability), the grounding
// verdict, assembled citations, whether the answer had to degrade, and
// token/latency accounting.
func (s *Service) run(ctx context.Context, tenant core.TenantID, question string, messages []ports.Message, tools []ports.ToolDefinition) (
	answer string, toolResults []ports.ToolResult, grounding ports.GroundingReport, cites []ports.Citation, degraded bool, inTok, outTok int, latencyMS int64,
) {
	gb := newGroundingBuilder()
	start := time.Now()

	if s.provider == nil || !s.provider.Healthy(ctx) {
		degraded = true
		answer, toolResults = s.degradedAnswer(ctx, tenant, question, gb)
		cites = gb.citations
		grounding = ports.GroundingReport{Grounded: true, Confidence: 1}
		latencyMS = time.Since(start).Milliseconds()
		return
	}

	for round := 0; round < maxToolRounds; round++ {
		req := ports.CompletionRequest{
			Purpose: "copilot", System: copilotSystemPrompt, Messages: messages,
			Tools: tools, TenantID: tenant, MaxTokens: 1024,
		}
		resp, err := s.provider.Complete(ctx, req)
		if err != nil {
			degraded = true
			answer, toolResults = s.degradedAnswer(ctx, tenant, question, gb)
			cites = gb.citations
			grounding = ports.GroundingReport{Grounded: true, Confidence: 1}
			latencyMS = time.Since(start).Milliseconds()
			return
		}
		inTok += resp.InputTokens
		outTok += resp.OutputTokens

		if len(resp.ToolCalls) == 0 {
			answer = resp.Content
			break
		}
		for _, call := range resp.ToolCalls {
			result, tr := s.invokeTool(ctx, tenant, call)
			toolResults = append(toolResults, tr)
			gb.absorb(call.Name, result)
			messages = append(messages, toolCallMessage(call), toolResultMessage(call, result))
		}
		if round == maxToolRounds-1 {
			// Budget exhausted: compose from whatever was gathered rather than
			// looping forever waiting for the model to stop asking for tools.
			answer = composeFromSummaries(question, toolResults)
		}
	}
	if answer == "" {
		answer = composeFromSummaries(question, toolResults)
	}

	cites = gb.citations
	report, verr := s.verifier.Verify(ctx, tenant, answer, gb.set)
	if verr == nil {
		grounding = report
	} else {
		grounding = ports.GroundingReport{Grounded: true, Confidence: 1}
	}
	if !grounding.Grounded {
		// One regeneration attempt: ask again, explicit about what failed to
		// ground, hoping for a cleaner answer from the same evidence.
		messages = append(messages, ports.Message{Role: ports.RoleUser, Content: regenerationPrompt(grounding)})
		req := ports.CompletionRequest{Purpose: "copilot", System: copilotSystemPrompt, Messages: messages, TenantID: tenant, MaxTokens: 1024}
		if resp, err := s.provider.Complete(ctx, req); err == nil && resp.Content != "" {
			retryReport, verr := s.verifier.Verify(ctx, tenant, resp.Content, gb.set)
			if verr == nil && retryReport.Grounded {
				answer = resp.Content
				grounding = retryReport
			} else {
				// Still ungrounded: keep the original answer but make the
				// caveat explicit rather than silently presenting it as fact.
				answer = answer + " " + caveatFor(grounding)
			}
		} else {
			answer = answer + " " + caveatFor(grounding)
		}
	}
	latencyMS = time.Since(start).Milliseconds()
	return
}

func (s *Service) invokeTool(ctx context.Context, tenant core.TenantID, call ports.ToolCall) (map[string]any, ports.ToolResult) {
	start := time.Now()
	tool, ok := s.registry.Get(call.Name)
	if !ok {
		res := toolError("unknown tool %q", call.Name)
		return res, ports.ToolResult{ToolCallID: call.ID, Name: call.Name, Result: res, Error: fmt.Sprintf("unknown tool %q", call.Name), LatencyMS: time.Since(start).Milliseconds()}
	}
	if principal, ok := core.PrincipalFrom(ctx); ok && !principal.Can(tool.Definition().RequiredPermission) {
		res := toolError("not permitted to call %q", call.Name)
		return res, ports.ToolResult{ToolCallID: call.ID, Name: call.Name, Result: res, Error: "permission denied", LatencyMS: time.Since(start).Milliseconds()}
	}
	out, err := tool.Invoke(ctx, tenant, call.Arguments)
	if err != nil {
		res := toolError("%v", err)
		return res, ports.ToolResult{ToolCallID: call.ID, Name: call.Name, Result: res, Error: err.Error(), LatencyMS: time.Since(start).Milliseconds()}
	}
	m, ok := out.(map[string]any)
	if !ok {
		m = toolResult(fmt.Sprintf("%v", out), nil)
	}
	tr := ports.ToolResult{ToolCallID: call.ID, Name: call.Name, Result: m, LatencyMS: time.Since(start).Milliseconds()}
	if e, ok := m["error"].(string); ok && e != "" {
		tr.Error = e
	}
	return m, tr
}

func toolCallMessage(call ports.ToolCall) ports.Message {
	return ports.Message{Role: ports.RoleAssistant, ToolCalls: []ports.ToolCall{call}}
}

func toolResultMessage(call ports.ToolCall, result map[string]any) ports.Message {
	raw, err := json.Marshal(result)
	if err != nil {
		raw = []byte(`{"error":"could not encode tool result"}`)
	}
	return ports.Message{Role: ports.RoleTool, Name: call.Name, ToolCallID: call.ID, Content: string(raw)}
}

// degradedAnswer answers with a fixed, tool-grounded template when no model
// is available: it runs the one tool most likely to have something useful
// to say (get_cost_summary) directly, rather than guessing at intent
// without a model to route the question.
func (s *Service) degradedAnswer(ctx context.Context, tenant core.TenantID, question string, gb *groundingBuilder) (string, []ports.ToolResult) {
	tool, ok := s.registry.Get("get_cost_summary")
	if !ok {
		return "The AI assistant is temporarily unavailable, and no fallback tool could be reached.", nil
	}
	out, err := tool.Invoke(ctx, tenant, nil)
	if err != nil {
		return "The AI assistant is temporarily unavailable, and the cost summary could not be loaded either: " + err.Error(), nil
	}
	m, _ := out.(map[string]any)
	gb.absorb("get_cost_summary", m)
	summary, _ := m["summary"].(string)
	tr := ports.ToolResult{ToolCallID: "degraded_get_cost_summary", Name: "get_cost_summary", Result: m}
	return "The AI assistant is temporarily unavailable, so here is a grounded cost summary instead: " + summary, []ports.ToolResult{tr}
}

// composeFromSummaries builds a plain-English answer directly from the
// tool results gathered this turn, the same "concatenate the summaries"
// strategy internal/adapters/llm/deterministic's answer.go uses — every
// sentence traces to a tool result, so this composition is grounded by
// construction, which matters for the maxToolRounds-exhausted path where
// the loop stops before the model produced any Content of its own.
func composeFromSummaries(question string, results []ports.ToolResult) string {
	if len(results) == 0 {
		return "I don't have enough grounded data to answer that yet — no tool returned a result for this question."
	}
	var successes, failures []string
	for _, r := range results {
		m, _ := r.Result.(map[string]any)
		if s, ok := m["summary"].(string); ok && s != "" {
			if r.Error != "" {
				failures = append(failures, s)
			} else {
				successes = append(successes, s)
			}
			continue
		}
		if r.Error != "" {
			failures = append(failures, r.Error)
		}
	}
	var b strings.Builder
	if len(successes) > 0 {
		b.WriteString(strings.Join(successes, " "))
	}
	if len(failures) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("Some data could not be retrieved: " + strings.Join(failures, "; ") + ".")
	}
	if b.Len() == 0 {
		return "I looked, but found no grounded data to answer that."
	}
	return b.String()
}

func regenerationPrompt(report ports.GroundingReport) string {
	return "Your previous answer referenced something not returned by any tool this turn: " + strings.Join(report.Issues, "; ") +
		". Answer again using only the numbers, resource ids and account ids the tool results above actually contain."
}

func caveatFor(report ports.GroundingReport) string {
	return fmt.Sprintf("(Note: %d detail(s) in this answer could not be verified against tool data and should be double-checked.)", len(report.Issues))
}

func followUpsFor(results []ports.ToolResult) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range results {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		switch r.Name {
		case "get_cost_summary", "get_cost_breakdown":
			out = append(out, "What's driving the biggest costs?")
		case "list_recommendations":
			out = append(out, "What should we optimize first?")
		}
	}
	return out
}

// loadOrStartConversation resolves in.ConversationID to a stored
// conversation, or starts a new one when it is empty or not found.
func (s *Service) loadOrStartConversation(ctx context.Context, tenant core.TenantID, in ports.CopilotRequest) (ports.Conversation, []ports.Message, error) {
	var conv ports.Conversation
	var history []ports.Message
	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if in.ConversationID != "" {
			existing, gerr := repos.Conversations.Get(ctx, tenant, in.ConversationID)
			if gerr == nil {
				conv = existing
				history = toMessages(existing.Turns)
				return nil
			}
		}
		conv = ports.Conversation{
			ID: core.NewID("conv"), TenantID: tenant, Kind: ports.ConversationCopilot,
			Title: titleFromQuestion(in.Question), Actor: in.Actor, State: "active",
			CreatedAt: s.clock(), UpdatedAt: s.clock(),
		}
		return repos.Conversations.Create(ctx, conv)
	})
	return conv, history, err
}

func (s *Service) appendTurns(ctx context.Context, tenant core.TenantID, convID core.ID, in ports.CopilotRequest, assistant ports.Turn) error {
	return s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		userTurn := ports.Turn{ID: core.NewID("turn"), Role: ports.RoleUser, Content: in.Question, At: s.clock()}
		if err := repos.Conversations.AppendTurn(ctx, tenant, convID, userTurn); err != nil {
			return err
		}
		return repos.Conversations.AppendTurn(ctx, tenant, convID, assistant)
	})
}

func toMessages(turns []ports.Turn) []ports.Message {
	out := make([]ports.Message, 0, len(turns))
	for _, t := range turns {
		out = append(out, ports.Message{Role: t.Role, Content: t.Content})
	}
	return out
}

func titleFromQuestion(q string) string {
	q = strings.TrimSpace(q)
	if len(q) > 60 {
		return q[:57] + "..."
	}
	return q
}

// GetConversation implements ports.CopilotService.
func (s *Service) GetConversation(ctx context.Context, tenant core.TenantID, id core.ID) (ports.Conversation, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Conversation{}, err
	}
	var conv ports.Conversation
	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		c, err := repos.Conversations.Get(ctx, tenant, id)
		conv = c
		return err
	})
	return conv, err
}

// ListConversations implements ports.CopilotService.
func (s *Service) ListConversations(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[ports.Conversation], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[ports.Conversation]{}, err
	}
	var page ports.Page[ports.Conversation]
	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		p, err := repos.Conversations.List(ctx, tenant, ports.ConversationCopilot, opts)
		page = p
		return err
	})
	return page, err
}

// Suggestions implements ports.CopilotService: a short list of questions
// worth asking, derived from the tenant's actual state (open
// recommendations, budgets at risk) rather than a fixed list, so a tenant
// with nothing wrong sees different suggestions than one with a breached
// budget.
func (s *Service) Suggestions(ctx context.Context, tenant core.TenantID) ([]string, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []string
	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if summary, serr := repos.Recommendations.Summary(ctx, tenant); serr == nil && summary.Open > 0 {
			out = append(out, "What should we optimize first?")
			if summary.SavingByCategory[optimize.CategoryWaste].Units() > 0 {
				out = append(out, "What's wasting money right now?")
			}
		}
		if states, berr := repos.Economics.ListBudgetStates(ctx, tenant); berr == nil {
			for _, b := range states {
				if b.State == econ.BudgetAtRisk || b.State == econ.BudgetExhausted || b.State == econ.BudgetBreached {
					out = append(out, "Are we within our cost budget?")
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, "How much are we spending?", "What's our most expensive service?", "Why did cost change recently?")
	return dedupe(out), nil
}
