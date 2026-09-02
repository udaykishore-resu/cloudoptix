package http

import (
	"net/http"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// automationHandler serves the Automation tag (planning, autonomous
// processing, calibration) and, in handlers_executions.go and
// handlers_savings.go, the Executions and Savings tags — all three are
// backed by the single AutomationService. There is no dedicated
// SavingsService port: Funnel is what the Savings tag serves, since a
// realized-savings funnel is exactly what AutomationService already tracks
// as the outcome of the plans it executes.
type automationHandler struct{ svc ports.AutomationService }

type planExecutionRequest struct {
	DryRun       bool       `json:"dry_run"`
	ScheduledFor *time.Time `json:"scheduled_for,omitempty"`
}

func (h *automationHandler) PlanExecution(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermExecutionStart)
	if !ok {
		return
	}
	req, ok := decodeBody[planExecutionRequest](w, r)
	if !ok {
		return
	}
	v, err := h.svc.PlanExecution(r.Context(), p.TenantID, PathID(r, "recommendationID"), ports.PlanOptions{
		DryRun: req.DryRun, ScheduledFor: req.ScheduledFor, RequestedBy: p.Describe(),
	})
	respond(w, r, http.StatusCreated, v, err)
}

func (h *automationHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermExecutionRead)
	if !ok {
		return
	}
	v, err := h.svc.GetPlan(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *automationHandler) ListPlans(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermExecutionRead)
	if !ok {
		return
	}
	var states []execute.PlanState
	for _, s := range queryList(r, "state") {
		states = append(states, execute.PlanState(s))
	}
	v, err := h.svc.ListPlans(r.Context(), p.TenantID, states, ParseListOptions(r))
	respond(w, r, http.StatusOK, v, err)
}

func (h *automationHandler) ProcessAutonomous(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAutomationWrite)
	if !ok {
		return
	}
	v, err := h.svc.ProcessAutonomous(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *automationHandler) Learn(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAutomationWrite)
	if !ok {
		return
	}
	v, err := h.svc.Learn(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}
