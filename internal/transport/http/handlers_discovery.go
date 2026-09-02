package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type discoveryHandler struct{ svc ports.DiscoveryService }

type runDiscoveryRequest struct {
	AccountID      string   `json:"account_id"`
	Regions        []string `json:"regions"`
	Services       []string `json:"services"`
	Trigger        string   `json:"trigger"`
	IncludeMetrics bool     `json:"include_metrics"`
	IncludeCost    bool     `json:"include_cost"`
	Async          bool     `json:"async"`
}

func (h *discoveryHandler) Run(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermDiscoveryRun)
	if !ok {
		return
	}
	req, ok := decodeBody[runDiscoveryRequest](w, r)
	if !ok {
		return
	}
	regions := make([]core.Region, 0, len(req.Regions))
	for _, rg := range req.Regions {
		regions = append(regions, core.Region(rg))
	}
	trigger := req.Trigger
	if trigger == "" {
		trigger = "manual"
	}
	run, err := h.svc.Run(r.Context(), p.TenantID, ports.DiscoveryRequest{
		AccountID: core.ID(req.AccountID), Regions: regions, Services: req.Services,
		Trigger: trigger, IncludeMetrics: req.IncludeMetrics, IncludeCost: req.IncludeCost, Async: req.Async,
	})
	respond(w, r, http.StatusAccepted, run, err)
}

func (h *discoveryHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), p.TenantID, PathID(r, "runID"))
	respond(w, r, http.StatusOK, v, err)
}

// GetStream streams a running discovery job's progress as SSE — it polls
// Get at a short interval and emits a "progress" event whenever the run's
// coverage or state changes, terminating on a final state. Polling rather
// than a native progress-push API is the honest reflection of what
// DiscoveryService exposes: Get returns a snapshot, not a subscription, so
// this is the streaming surface built from the polling primitive that
// exists rather than one this transport package cannot honestly provide.
func (h *discoveryHandler) GetStream(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	sse, err := NewSSEWriter(w)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	pollJobProgress(r, sse, func() (any, bool, error) {
		run, err := h.svc.Get(r.Context(), p.TenantID, PathID(r, "runID"))
		if err != nil {
			return nil, true, err
		}
		done := run.State == "completed" || run.State == "failed" || run.State == "partial"
		return run, done, nil
	})
}

func (h *discoveryHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	v, err := h.svc.ListRuns(r.Context(), p.TenantID, QueryInt(r, "limit", 20))
	respond(w, r, http.StatusOK, v, err)
}

func (h *discoveryHandler) Status(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	v, err := h.svc.Status(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}
