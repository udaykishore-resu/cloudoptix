package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// economicsHandler serves the Economics tag (footprints, unit economics,
// efficiency, the executive summary) and, in handlers_cost_slos.go, the Cost
// SLOs tag — both are backed by the single EconomicsService.
type economicsHandler struct{ svc ports.EconomicsService }

func (h *economicsHandler) Compute(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.Compute(r.Context(), p.TenantID, period)
	respond(w, r, http.StatusAccepted, v, err)
}

func (h *economicsHandler) Footprint(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	scope := econ.Scope(r.URL.Query().Get("scope"))
	v, err := h.svc.Footprint(r.Context(), p.TenantID, scope, PathID(r, "id"), period)
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) ListFootprints(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	scope := econ.Scope(orDefault(r.URL.Query().Get("scope"), string(econ.ScopeApplication)))
	v, err := h.svc.ListFootprints(r.Context(), p.TenantID, scope, period)
	respond(w, r, http.StatusOK, v, err)
}

// economicsWritePermission is what UpsertTransaction and every Cost SLO
// mutation in handlers_cost_slos.go check. There is no dedicated
// "economics:write" permission in the platform's permission table — SLOWrite
// is the closest fit and is already granted to exactly the roles (tenant
// admin, architect, FinOps analyst) that should be defining transactions and
// objectives, so reusing it here rather than inventing a parallel permission
// keeps the RBAC table's single source of truth intact.
const economicsWritePermission = core.PermSLOWrite

func (h *economicsHandler) UpsertTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, economicsWritePermission)
	if !ok {
		return
	}
	t, ok := decodeBody[econ.BusinessTransaction](w, r)
	if !ok {
		return
	}
	t.TenantID = p.TenantID // the path/token's tenant scope always wins over anything the body claims
	v, err := h.svc.UpsertTransaction(r.Context(), t)
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	v, err := h.svc.ListTransactions(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) UnitEconomics(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.UnitEconomics(r.Context(), p.TenantID, PathID(r, "id"), period)
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) UnitEconomicsHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.UnitEconomicsHistory(r.Context(), p.TenantID, PathID(r, "id"), period.Start, period.End)
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) EfficiencyScore(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	scope := econ.Scope(orDefault(r.URL.Query().Get("scope"), string(econ.ScopeOrganization)))
	v, err := h.svc.EfficiencyScore(r.Context(), p.TenantID, scope, core.ID(r.URL.Query().Get("scope_id")))
	respond(w, r, http.StatusOK, v, err)
}

func (h *economicsHandler) ExecutiveSummary(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermEconomicsRead)
	if !ok {
		return
	}
	v, err := h.svc.ExecutiveSummary(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}
