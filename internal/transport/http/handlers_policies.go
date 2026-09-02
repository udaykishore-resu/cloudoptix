package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// governanceHandler serves both the Policies and Approvals tags — both are
// backed by the single GovernanceService.
type governanceHandler struct{ svc ports.GovernanceService }

func (h *governanceHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermPolicyRead)
	if !ok {
		return
	}
	v, err := h.svc.GetPolicy(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *governanceHandler) ListPolicyVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermPolicyRead)
	if !ok {
		return
	}
	v, err := h.svc.ListPolicyVersions(r.Context(), p.TenantID, r.URL.Query().Get("name"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *governanceHandler) SavePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermPolicyWrite)
	if !ok {
		return
	}
	pol, ok := decodeBody[govern.Policy](w, r)
	if !ok {
		return
	}
	pol.TenantID = p.TenantID
	v, err := h.svc.SavePolicy(r.Context(), p.TenantID, pol, p.Describe())
	respond(w, r, http.StatusOK, v, err)
}

func (h *governanceHandler) ValidatePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := authorize(w, r, core.PermPolicyRead); !ok {
		return
	}
	pol, ok := decodeBody[govern.Policy](w, r)
	if !ok {
		return
	}
	v := h.svc.ValidatePolicy(r.Context(), pol)
	WriteJSON(w, http.StatusOK, v)
}

func (h *governanceHandler) ActivatePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermPolicyWrite)
	if !ok {
		return
	}
	err := h.svc.ActivatePolicy(r.Context(), p.TenantID, PathID(r, "id"), p.Describe())
	respond(w, r, http.StatusNoContent, nil, err)
}

func (h *governanceHandler) Evaluate(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermPolicyRead)
	if !ok {
		return
	}
	v, err := h.svc.Evaluate(r.Context(), p.TenantID, PathID(r, "recommendationID"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *governanceHandler) Simulate(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermPolicyRead)
	if !ok {
		return
	}
	pol, ok := decodeBody[govern.Policy](w, r)
	if !ok {
		return
	}
	v, err := h.svc.Simulate(r.Context(), p.TenantID, pol)
	respond(w, r, http.StatusOK, v, err)
}
