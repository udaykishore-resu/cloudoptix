package http

import (
	"encoding/json"
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// onboardingHandler serves the Onboarding tag. Start is unauthenticated (a
// prospective customer has no CloudOptix identity yet); every other
// operation requires a conversation id that Start handed back, which is
// itself the access control for the remainder of one conversation until
// Approve creates the tenant.
type onboardingHandler struct{ svc ports.OnboardingService }

type startOnboardingRequest struct {
	Actor          string        `json:"actor"`
	ActorEmail     string        `json:"actor_email"`
	InitialMessage string        `json:"initial_message"`
	ExistingTenant core.TenantID `json:"existing_tenant,omitempty"`
}

// Start has no permission requirement — see the type doc comment — and is
// mounted outside the authentication middleware entirely (router.go).
func (h *onboardingHandler) Start(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[startOnboardingRequest](w, r)
	if !ok {
		return
	}
	state, err := h.svc.Start(r.Context(), ports.StartOnboardingInput{
		Actor: req.Actor, ActorEmail: req.ActorEmail,
		InitialMessage: req.InitialMessage, ExistingTenant: req.ExistingTenant,
	})
	respond(w, r, http.StatusCreated, state, err)
}

type sendMessageRequest struct {
	Message string `json:"message"`
}

func (h *onboardingHandler) Send(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[sendMessageRequest](w, r)
	if !ok {
		return
	}
	state, err := h.svc.Send(r.Context(), PathID(r, "conversationID"), req.Message)
	respond(w, r, http.StatusOK, state, err)
}

// SendStream is the SSE surface for onboarding chat turns: the reply streams
// as it is produced instead of the client waiting for the whole turn.
// Because OnboardingService.Send is not itself a streaming API (it returns
// the completed OnboardingState), this handler calls it exactly once and
// then emits its result as a sequence of framed SSE events — a "thinking"
// event immediately, then the completed turn — so the same conversation flow
// endpoint gives a client that wants to render progressive output somewhere
// to hang a UI on, without requiring the underlying service to be
// restructured around streaming to get an event-stream response shape.
func (h *onboardingHandler) SendStream(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[sendMessageRequest](w, r)
	if !ok {
		return
	}
	sse, err := NewSSEWriter(w)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	_ = sse.Send(Event{Event: "thinking", Data: map[string]any{"stage": "processing"}})
	state, err := h.svc.Send(r.Context(), PathID(r, "conversationID"), req.Message)
	if err != nil {
		_ = sse.SendError(err)
		return
	}
	_ = sse.Send(Event{Event: "turn", Data: state})
	_ = sse.Send(Event{Event: "done", Data: map[string]any{"stage": state.Stage}})
}

func (h *onboardingHandler) State(w http.ResponseWriter, r *http.Request) {
	state, err := h.svc.State(r.Context(), PathID(r, "conversationID"))
	respond(w, r, http.StatusOK, state, err)
}

func (h *onboardingHandler) Summarize(w http.ResponseWriter, r *http.Request) {
	summary, err := h.svc.Summarize(r.Context(), PathID(r, "conversationID"))
	respond(w, r, http.StatusOK, summary, err)
}

type applyEditRequest struct {
	Patch json.RawMessage `json:"patch"`
	Actor string          `json:"actor"`
}

func (h *onboardingHandler) ApplyEdit(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[applyEditRequest](w, r)
	if !ok {
		return
	}
	var patch map[string]any
	if len(req.Patch) > 0 {
		if err := json.Unmarshal(req.Patch, &patch); err != nil {
			WriteProblem(w, r, core.Invalid("patch: %s", err.Error()))
			return
		}
	}
	state, err := h.svc.ApplyEdit(r.Context(), PathID(r, "conversationID"), patch, req.Actor)
	respond(w, r, http.StatusOK, state, err)
}

type approveOnboardingRequest struct {
	Actor      string `json:"actor"`
	ActorEmail string `json:"actor_email"`
	TenantName string `json:"tenant_name"`
	TenantSlug string `json:"tenant_slug"`
	Plan       string `json:"plan"`
}

func (h *onboardingHandler) Approve(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[approveOnboardingRequest](w, r)
	if !ok {
		return
	}
	result, err := h.svc.Approve(r.Context(), ports.ApproveOnboardingInput{
		ConversationID: PathID(r, "conversationID"),
		Actor:          req.Actor, ActorEmail: req.ActorEmail,
		TenantName: req.TenantName, TenantSlug: req.TenantSlug,
		Plan:      planOf(req.Plan),
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	respond(w, r, http.StatusCreated, result, err)
}

type cancelOnboardingRequest struct {
	Reason string `json:"reason"`
}

func (h *onboardingHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[cancelOnboardingRequest](w, r)
	if !ok {
		return
	}
	err := h.svc.Cancel(r.Context(), PathID(r, "conversationID"), req.Reason)
	respond(w, r, http.StatusNoContent, nil, err)
}
