package http

import (
	"net/http"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// recommendationsHandler serves the Recommendations tag, backed by
// OptimizationService.
type recommendationsHandler struct{ svc ports.OptimizationService }

type analyzeRequest struct {
	AccountIDs         []string `json:"account_ids"`
	Environments       []string `json:"environments"`
	Categories         []string `json:"categories"`
	RuleIDs            []string `json:"rule_ids"`
	GenerateNarratives bool     `json:"generate_narratives"`
	Async              bool     `json:"async"`
}

func (h *recommendationsHandler) Analyze(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRecommendRun)
	if !ok {
		return
	}
	req, ok := decodeBody[analyzeRequest](w, r)
	if !ok {
		return
	}
	accountIDs := make([]core.AccountID, 0, len(req.AccountIDs))
	for _, a := range req.AccountIDs {
		accountIDs = append(accountIDs, core.AccountID(a))
	}
	environments := make([]core.Environment, 0, len(req.Environments))
	for _, e := range req.Environments {
		environments = append(environments, core.NormalizeEnvironment(e))
	}
	categories := make([]optimize.Category, 0, len(req.Categories))
	for _, c := range req.Categories {
		categories = append(categories, optimize.Category(c))
	}
	ruleIDs := make([]optimize.RuleID, 0, len(req.RuleIDs))
	for _, rid := range req.RuleIDs {
		ruleIDs = append(ruleIDs, optimize.RuleID(rid))
	}
	v, err := h.svc.Analyze(r.Context(), p.TenantID, ports.AnalyzeRequest{
		AccountIDs: accountIDs, Environments: environments, Categories: categories,
		RuleIDs: ruleIDs, GenerateNarratives: req.GenerateNarratives, Async: req.Async,
	})
	respond(w, r, http.StatusAccepted, v, err)
}

func recommendationFilterFromRequest(r *http.Request) ports.RecommendationFilter {
	q := r.URL.Query()
	f := ports.RecommendationFilter{
		Environments:       QueryEnvironments(r, "environment"),
		AccountIDs:         QueryAccountIDs(r, "account_id"),
		ApplicationID:      core.ID(q.Get("application_id")),
		ResourceID:         core.ID(q.Get("resource_id")),
		MinSaving:          QueryMoney(r, "min_saving"),
		MaxRisk:            core.RiskLevel(q.Get("max_risk")),
		AutoExecutableOnly: QueryBool(r, "auto_executable_only"),
	}
	f.MinConfidence = QueryFloat(r, "min_confidence")
	for _, s := range queryList(r, "status") {
		f.Statuses = append(f.Statuses, optimize.Status(s))
	}
	for _, c := range queryList(r, "category") {
		f.Categories = append(f.Categories, optimize.Category(c))
	}
	for _, a := range queryList(r, "action") {
		f.Actions = append(f.Actions, optimize.ActionType(a))
	}
	for _, rid := range queryList(r, "rule_id") {
		f.RuleIDs = append(f.RuleIDs, optimize.RuleID(rid))
	}
	return f
}

func (h *recommendationsHandler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRecommendRead)
	if !ok {
		return
	}
	v, err := h.svc.List(r.Context(), p.TenantID, recommendationFilterFromRequest(r), ParseListOptions(r))
	respond(w, r, http.StatusOK, v, err)
}

func (h *recommendationsHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRecommendRead)
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *recommendationsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRecommendRead)
	if !ok {
		return
	}
	v, err := h.svc.Summary(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

type dismissRequest struct {
	Reason string `json:"reason"`
}

func (h *recommendationsHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRecommendRun)
	if !ok {
		return
	}
	req, ok := decodeBody[dismissRequest](w, r)
	if !ok {
		return
	}
	err := h.svc.Dismiss(r.Context(), p.TenantID, PathID(r, "id"), req.Reason, p.Describe())
	respond(w, r, http.StatusNoContent, nil, err)
}

type snoozeRequest struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

func (h *recommendationsHandler) Snooze(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRecommendRun)
	if !ok {
		return
	}
	req, ok := decodeBody[snoozeRequest](w, r)
	if !ok {
		return
	}
	err := h.svc.Snooze(r.Context(), p.TenantID, PathID(r, "id"), req.Until, req.Reason, p.Describe())
	respond(w, r, http.StatusNoContent, nil, err)
}

func (h *recommendationsHandler) Explain(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRecommendRead)
	if !ok {
		return
	}
	v, err := h.svc.Explain(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *recommendationsHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermRecommendRead)
	if !ok {
		return
	}
	v, err := h.svc.ListRules(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}
