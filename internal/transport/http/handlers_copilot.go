package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type copilotHandler struct{ svc ports.CopilotService }

type askCopilotRequest struct {
	ConversationID string `json:"conversation_id"`
	Question       string `json:"question"`
	ContextKind    string `json:"context_kind"`
	ContextID      string `json:"context_id"`
}

func askRequestFrom(p core.Principal, req askCopilotRequest) ports.CopilotRequest {
	return ports.CopilotRequest{
		ConversationID: core.ID(req.ConversationID),
		Question:       req.Question,
		Actor:          p.Describe(),
		ContextKind:    req.ContextKind,
		ContextID:      core.ID(req.ContextID),
	}
}

func (h *copilotHandler) Ask(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCopilotUse)
	if !ok {
		return
	}
	req, ok := decodeBody[askCopilotRequest](w, r)
	if !ok {
		return
	}
	v, err := h.svc.Ask(r.Context(), p.TenantID, askRequestFrom(p, req))
	respond(w, r, http.StatusOK, v, err)
}

// AskStream streams a copilot answer as SSE. CopilotService.Ask is a single
// synchronous call, not a token stream — there is no incremental generation
// API in this port — so this emits a "thinking" event, invokes Ask once, and
// emits the finished answer as one "answer" event followed by "done". It is
// deliberately not framed as token-by-token streaming because it is not:
// this is what makes the copilot surface consistent with onboarding's
// SendStream (see handlers_onboarding.go), which is built from the same
// non-streaming primitive for the same reason.
func (h *copilotHandler) AskStream(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCopilotUse)
	if !ok {
		return
	}
	req, ok := decodeBody[askCopilotRequest](w, r)
	if !ok {
		return
	}
	sse, err := NewSSEWriter(w)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	if sendErr := sse.Send(Event{Event: "thinking", Data: map[string]any{"status": "retrieving"}}); sendErr != nil {
		return
	}
	answer, err := h.svc.Ask(r.Context(), p.TenantID, askRequestFrom(p, req))
	if err != nil {
		_ = sse.SendError(err)
		return
	}
	if sendErr := sse.Send(Event{Event: "answer", Data: answer}); sendErr != nil {
		return
	}
	_ = sse.Send(Event{Event: "done", Data: map[string]any{"conversation_id": answer.ConversationID}})
}

func (h *copilotHandler) GetConversation(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCopilotUse)
	if !ok {
		return
	}
	v, err := h.svc.GetConversation(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *copilotHandler) ListConversations(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCopilotUse)
	if !ok {
		return
	}
	v, err := h.svc.ListConversations(r.Context(), p.TenantID, ParseListOptions(r))
	respond(w, r, http.StatusOK, v, err)
}

func (h *copilotHandler) Suggestions(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCopilotUse)
	if !ok {
		return
	}
	v, err := h.svc.Suggestions(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}
