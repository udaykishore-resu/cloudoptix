package http

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type specsHandler struct{ svc ports.SpecService }

func (h *specsHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecRead)
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *specsHandler) GetActive(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecRead)
	if !ok {
		return
	}
	v, err := h.svc.GetActive(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *specsHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecRead)
	if !ok {
		return
	}
	v, err := h.svc.ListVersions(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *specsHandler) Diff(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecRead)
	if !ok {
		return
	}
	from, err1 := strconv.Atoi(r.URL.Query().Get("from"))
	to, err2 := strconv.Atoi(r.URL.Query().Get("to"))
	if err1 != nil || err2 != nil {
		WriteProblem(w, r, core.Invalid("from and to query parameters must be integer spec versions"))
		return
	}
	v, err := h.svc.Diff(r.Context(), p.TenantID, from, to)
	respond(w, r, http.StatusOK, v, err)
}

type proposeRevisionRequest struct {
	Patch json.RawMessage `json:"patch"`
	Actor string          `json:"actor"`
}

func (h *specsHandler) ProposeRevision(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecWrite)
	if !ok {
		return
	}
	req, ok := decodeBody[proposeRevisionRequest](w, r)
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
	v, err := h.svc.ProposeRevision(r.Context(), p.TenantID, patch, req.Actor)
	respond(w, r, http.StatusCreated, v, err)
}

type approveSpecRequest struct {
	Actor string `json:"actor"`
}

func (h *specsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecApprove)
	if !ok {
		return
	}
	req, ok := decodeBody[approveSpecRequest](w, r)
	if !ok {
		return
	}
	v, err := h.svc.Approve(r.Context(), p.TenantID, PathID(r, "id"), req.Actor)
	respond(w, r, http.StatusOK, v, err)
}

type rejectSpecRequest struct {
	Reason string `json:"reason"`
	Actor  string `json:"actor"`
}

func (h *specsHandler) Reject(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecApprove)
	if !ok {
		return
	}
	req, ok := decodeBody[rejectSpecRequest](w, r)
	if !ok {
		return
	}
	err := h.svc.Reject(r.Context(), p.TenantID, PathID(r, "id"), req.Reason, req.Actor)
	respond(w, r, http.StatusNoContent, nil, err)
}

// Validate lints a candidate specification without persisting it — the
// review UI calls this on every keystroke-settled edit, well before an
// operator commits to ProposeRevision.
func (h *specsHandler) Validate(w http.ResponseWriter, r *http.Request) {
	if _, ok := authorize(w, r, core.PermSpecRead); !ok {
		return
	}
	s, ok := decodeBody[spec.Spec](w, r)
	if !ok {
		return
	}
	result := h.svc.Validate(r.Context(), s)
	respond(w, r, http.StatusOK, result, nil)
}

func (h *specsHandler) ExportYAML(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecRead)
	if !ok {
		return
	}
	data, err := h.svc.ExportYAML(r.Context(), p.TenantID, PathID(r, "id"))
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="spec.yaml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type importYAMLRequest struct {
	YAML  string `json:"yaml"`
	Actor string `json:"actor"`
}

func (h *specsHandler) ImportYAML(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermSpecWrite)
	if !ok {
		return
	}
	req, ok := decodeBody[importYAMLRequest](w, r)
	if !ok {
		return
	}
	v, err := h.svc.ImportYAML(r.Context(), p.TenantID, []byte(req.YAML), req.Actor)
	respond(w, r, http.StatusCreated, v, err)
}
