package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// tenantsHandler serves both the Tenants and Users tags — both are backed by
// the single TenantService.
type tenantsHandler struct{ svc ports.TenantService }

func (h *tenantsHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermTenantAdmin)
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *tenantsHandler) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermTenantAdmin)
	if !ok {
		return
	}
	t, ok := decodeBody[tenancy.Tenant](w, r)
	if !ok {
		return
	}
	t.ID = p.TenantID // the path's tenant scope always wins over anything the body claims
	v, err := h.svc.Update(r.Context(), t, p.Describe())
	respond(w, r, http.StatusOK, v, err)
}

func (h *tenantsHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermTenantAdmin)
	if !ok {
		return
	}
	v, err := h.svc.ListUsers(r.Context(), p.TenantID, ParseListOptions(r))
	respond(w, r, http.StatusOK, v, err)
}

type inviteUserRequest struct {
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

func (h *tenantsHandler) InviteUser(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermTenantAdmin)
	if !ok {
		return
	}
	req, ok := decodeBody[inviteUserRequest](w, r)
	if !ok {
		return
	}
	v, err := h.svc.InviteUser(r.Context(), p.TenantID, req.Email, rolesOf(req.Roles), p.Describe())
	respond(w, r, http.StatusCreated, v, err)
}

type updateRolesRequest struct {
	Roles []string `json:"roles"`
}

func (h *tenantsHandler) UpdateRoles(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermTenantAdmin)
	if !ok {
		return
	}
	req, ok := decodeBody[updateRolesRequest](w, r)
	if !ok {
		return
	}
	err := h.svc.UpdateRoles(r.Context(), p.TenantID, PathID(r, "userID"), rolesOf(req.Roles), p.Describe())
	respond(w, r, http.StatusNoContent, nil, err)
}

func (h *tenantsHandler) RemoveUser(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermTenantAdmin)
	if !ok {
		return
	}
	err := h.svc.RemoveUser(r.Context(), p.TenantID, PathID(r, "userID"), p.Describe())
	respond(w, r, http.StatusNoContent, nil, err)
}
