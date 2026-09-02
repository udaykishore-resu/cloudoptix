package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
)

// The Cost SLOs tag: methods on economicsHandler, since CostSLOs are served
// by the same EconomicsService as the Economics tag (see handlers_economics.go).

func (h *economicsHandler) UpsertCostSLO(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSLOWrite)
	if !ok {
		return
	}
	s, ok := decodeBody[econ.CostSLO](w, r)
	if !ok {
		return
	}
	s.TenantID = p.TenantID
	v, err := h.svc.UpsertCostSLO(r.Context(), s)
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) ListCostSLOs(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	v, err := h.svc.ListCostSLOs(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) DeleteCostSLO(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSLOWrite)
	if !ok {
		return
	}
	err := h.svc.DeleteCostSLO(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusNoContent, nil, err)
}

func (h *economicsHandler) EvaluateSLOs(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	v, err := h.svc.EvaluateSLOs(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) BudgetStates(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	v, err := h.svc.BudgetStates(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}
