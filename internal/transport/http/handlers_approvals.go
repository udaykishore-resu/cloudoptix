package http

import (
	"net/http"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
)

// The Approvals tag: methods on governanceHandler, since approvals are
// served by the same GovernanceService as the Policies tag.

func (h *governanceHandler) ListApprovals(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermApprovalRead)
	if !ok {
		return
	}
	v, err := h.svc.ListApprovals(r.Context(), p.TenantID, ParseListOptions(r))
	respond(w, r, http.StatusOK, v, err)
}

func (h *governanceHandler) GetApproval(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermApprovalRead)
	if !ok {
		return
	}
	v, err := h.svc.GetApproval(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

type decideApprovalRequest struct {
	Approved bool   `json:"approved"`
	Comment  string `json:"comment"`
}

func (h *governanceHandler) Decide(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermApprovalDecide)
	if !ok {
		return
	}
	req, ok := decodeBody[decideApprovalRequest](w, r)
	if !ok {
		return
	}
	// Principal, role and provenance come from the authenticated context, not
	// the request body — an approval vote must record who actually decided,
	// not whatever the client claims.
	role := core.Role("")
	if len(p.Roles) > 0 {
		role = p.Roles[0]
	}
	resp := govern.Response{
		Principal: p.Describe(),
		Role:      role,
		Approved:  req.Approved,
		Comment:   req.Comment,
		At:        time.Now().UTC(),
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	}
	v, err := h.svc.Decide(r.Context(), p.TenantID, PathID(r, "id"), resp)
	respond(w, r, http.StatusOK, v, err)
}

func (h *governanceHandler) RequestApproval(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermApprovalRead)
	if !ok {
		return
	}
	req, ok := decodeBody[govern.Request](w, r)
	if !ok {
		return
	}
	req.TenantID = p.TenantID
	req.RequestedBy = p.Describe()
	v, err := h.svc.RequestApproval(r.Context(), req)
	respond(w, r, http.StatusCreated, v, err)
}
