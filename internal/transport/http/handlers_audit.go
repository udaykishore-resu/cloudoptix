package http

import (
	"net/http"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type auditHandler struct{ svc ports.AuditService }

func auditQueryFromRequest(r *http.Request) ports.AuditQuery {
	q := r.URL.Query()
	period, err := ParsePeriod(r)
	from, to := time.Time{}, time.Time{}
	if err == nil {
		from, to = period.Start, period.End
	}
	return ports.AuditQuery{
		Actions:   queryList(r, "action"),
		Actors:    queryList(r, "actor"),
		Outcomes:  queryList(r, "outcome"),
		SubjectID: core.ID(q.Get("subject_id")),
		From:      from,
		To:        to,
		Limit:     QueryInt(r, "limit", 50),
		Cursor:    q.Get("cursor"),
	}
}

func (h *auditHandler) Query(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAuditRead)
	if !ok {
		return
	}
	v, err := h.svc.Query(r.Context(), p.TenantID, auditQueryFromRequest(r))
	respond(w, r, http.StatusOK, v, err)
}

func (h *auditHandler) Verify(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAuditRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.Verify(r.Context(), p.TenantID, period.Start, period.End)
	respond(w, r, http.StatusOK, v, err)
}

func (h *auditHandler) Timeline(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermAuditRead)
	if !ok {
		return
	}
	v, err := h.svc.Timeline(r.Context(), p.TenantID, PathID(r, "recommendationID"))
	respond(w, r, http.StatusOK, v, err)
}
