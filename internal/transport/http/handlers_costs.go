package http

import (
	"net/http"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type costsHandler struct{ svc ports.CostService }

func costFilterFromRequest(r *http.Request, period core.Period) ports.CostFilter {
	q := r.URL.Query()
	return ports.CostFilter{
		Period:        period,
		Granularity:   cost.Granularity(orDefault(q.Get("granularity"), string(cost.GranularityDaily))),
		AccountIDs:    QueryAccountIDs(r, "account_id"),
		Regions:       QueryRegions(r, "region"),
		Services:      queryList(r, "service"),
		Environments:  QueryEnvironments(r, "environment"),
		ResourceIDs:   QueryIDs(r, "resource_id"),
		ApplicationID: core.ID(q.Get("application_id")),
		Basis:         cost.AmortizationBasis(orDefault(q.Get("basis"), string(cost.BasisAmortized))),
		TagKey:        q.Get("tag_key"),
		TagValue:      q.Get("tag_value"),
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

type ingestCostRequest struct {
	AccountID string `json:"account_id"`
}

func (h *costsHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	req, ok := decodeBody[ingestCostRequest](w, r)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.Ingest(r.Context(), p.TenantID, core.ID(req.AccountID), period)
	respond(w, r, http.StatusAccepted, v, err)
}

func (h *costsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.Summary(r.Context(), p.TenantID, period)
	respond(w, r, http.StatusOK, v, err)
}

func (h *costsHandler) Series(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.Series(r.Context(), p.TenantID, costFilterFromRequest(r, period))
	respond(w, r, http.StatusOK, v, err)
}

func (h *costsHandler) Breakdown(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	dimension := orDefault(r.URL.Query().Get("dimension"), "service")
	v, err := h.svc.Breakdown(r.Context(), p.TenantID, costFilterFromRequest(r, period), dimension)
	respond(w, r, http.StatusOK, v, err)
}

func (h *costsHandler) Forecast(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	horizonDays := QueryInt(r, "horizon_days", 30)
	horizon := core.PeriodOfDays(time.Now(), horizonDays)
	v, err := h.svc.Forecast(r.Context(), p.TenantID, costFilterFromRequest(r, period), horizon)
	respond(w, r, http.StatusOK, v, err)
}

func (h *costsHandler) DetectAnomalies(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	lookback := core.PeriodOfDays(time.Now(), QueryInt(r, "lookback_days", 14))
	v, err := h.svc.DetectAnomalies(r.Context(), p.TenantID, lookback)
	respond(w, r, http.StatusOK, v, err)
}

func (h *costsHandler) ListAnomalies(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	period, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	v, err := h.svc.ListAnomalies(r.Context(), p.TenantID, period.Start, period.End)
	respond(w, r, http.StatusOK, v, err)
}

func (h *costsHandler) Explain(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	current, err := ParsePeriod(r)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	baseline := core.Period{
		Start: current.Start.AddDate(0, 0, -daysBetween(current)),
		End:   current.Start,
	}
	v, err := h.svc.Explain(r.Context(), p.TenantID, current, baseline)
	respond(w, r, http.StatusOK, v, err)
}

func daysBetween(p core.Period) int {
	d := int(p.End.Sub(p.Start).Hours() / 24)
	if d < 1 {
		d = 1
	}
	return d
}
