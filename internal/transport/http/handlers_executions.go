package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// The Executions tag: methods on automationHandler, since executions are
// served by the same AutomationService as the Automation tag.

func (h *automationHandler) Execute(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermExecutionStart)
	if !ok {
		return
	}
	v, err := h.svc.Execute(r.Context(), p.TenantID, PathID(r, "id"), p.Describe())
	respond(w, r, http.StatusAccepted, v, err)
}

type cancelExecutionRequest struct {
	Reason string `json:"reason"`
}

func (h *automationHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermExecutionCancel)
	if !ok {
		return
	}
	req, ok := decodeBody[cancelExecutionRequest](w, r)
	if !ok {
		return
	}
	err := h.svc.Cancel(r.Context(), p.TenantID, PathID(r, "id"), req.Reason, p.Describe())
	respond(w, r, http.StatusNoContent, nil, err)
}

func (h *automationHandler) Validate(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermExecutionRead)
	if !ok {
		return
	}
	v, err := h.svc.Validate(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

type rollbackRequest struct {
	Reason string `json:"reason"`
}

func (h *automationHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRollbackStart)
	if !ok {
		return
	}
	req, ok := decodeBody[rollbackRequest](w, r)
	if !ok {
		return
	}
	v, err := h.svc.Rollback(r.Context(), p.TenantID, PathID(r, "id"), req.Reason, p.Describe())
	respond(w, r, http.StatusAccepted, v, err)
}

// ExecutionStream streams an execution plan's progress as SSE by polling
// GetPlan — the same honest-polling approach used for discovery progress
// (see sse.go), since AutomationService exposes GetPlan as a snapshot, not a
// subscription.
func (h *automationHandler) ExecutionStream(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermExecutionRead)
	if !ok {
		return
	}
	sse, err := NewSSEWriter(w)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	pollJobProgress(r, sse, func() (any, bool, error) {
		plan, err := h.svc.GetPlan(r.Context(), p.TenantID, PathID(r, "id"))
		if err != nil {
			return nil, true, err
		}
		return plan, plan.State.Terminal(), nil
	})
}
