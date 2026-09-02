package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type awsAccountsHandler struct{ svc ports.AWSAccountService }

type registerAccountRequest struct {
	AccountID   string            `json:"account_id"`
	Alias       string            `json:"alias"`
	Environment string            `json:"environment"`
	Regions     []string          `json:"regions"`
	AccessMode  string            `json:"access_mode"`
	RoleARNs    map[string]string `json:"role_arns"`
	IsPayer     bool              `json:"is_payer"`
	CURBucket   string            `json:"cur_bucket"`
	CURPrefix   string            `json:"cur_prefix"`
}

func (h *awsAccountsHandler) Register(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAWSConnect)
	if !ok {
		return
	}
	req, ok := decodeBody[registerAccountRequest](w, r)
	if !ok {
		return
	}
	regions := make([]core.Region, 0, len(req.Regions))
	for _, rg := range req.Regions {
		regions = append(regions, core.Region(rg))
	}
	roleARNs := make(map[cloud.RoleScope]core.ARN, len(req.RoleARNs))
	for scope, arn := range req.RoleARNs {
		roleARNs[roleScopeOf(scope)] = core.ARN(arn)
	}
	account, instructions, err := h.svc.Register(r.Context(), p.TenantID, ports.RegisterAccountInput{
		AccountID: core.AccountID(req.AccountID), Alias: req.Alias,
		Environment: core.NormalizeEnvironment(req.Environment), Regions: regions,
		AccessMode: cloud.AccessMode(req.AccessMode), RoleARNs: roleARNs,
		IsPayer: req.IsPayer, CURBucket: req.CURBucket, CURPrefix: req.CURPrefix,
	})
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"account": account, "next_steps": instructions})
}

func (h *awsAccountsHandler) Verify(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAWSConnect)
	if !ok {
		return
	}
	account, check, err := h.svc.Verify(r.Context(), p.TenantID, PathID(r, "id"))
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"account": account, "connection_check": check})
}

func (h *awsAccountsHandler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	v, err := h.svc.List(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *awsAccountsHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

type suspendAccountRequest struct {
	Reason string `json:"reason"`
}

func (h *awsAccountsHandler) Suspend(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAWSConnect)
	if !ok {
		return
	}
	req, ok := decodeBody[suspendAccountRequest](w, r)
	if !ok {
		return
	}
	err := h.svc.Suspend(r.Context(), p.TenantID, PathID(r, "id"), req.Reason)
	respond(w, r, http.StatusNoContent, nil, err)
}

func (h *awsAccountsHandler) Remove(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAWSConnect)
	if !ok {
		return
	}
	err := h.svc.Remove(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusNoContent, nil, err)
}

func (h *awsAccountsHandler) Instructions(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAWSConnect)
	if !ok {
		return
	}
	v, err := h.svc.Instructions(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}
