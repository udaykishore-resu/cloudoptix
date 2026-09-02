package http

import (
	"net/http"
	"strconv"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// twinHandler serves the Architecture tag (the digital twin graph) and, by
// reuse, the Resources tag.
//
// ports.Services has no dedicated ResourceService use case — resource
// inventory is a driven-side concern (ports.ResourceRepository) that no
// application service exposes as a read-oriented driving port on its own.
// TwinService.Graph and TwinService.Node are the closest, and in fact the
// right, fit: TwinNode already carries everything a "resource" endpoint
// would need to return (kind, account, region, cost, risk, tags,
// utilisation) because the twin *is* the resource model enriched with cost
// and topology, not a separate representation of it. Handlers here that
// serve /resources therefore call the twin, not a repository — the "never
// reach a repository directly" rule holds exactly the same way it does for
// every other handler in this package.
type twinHandler struct{ svc ports.TwinService }

func twinQueryFromRequest(r *http.Request, view string) ports.TwinQuery {
	q := r.URL.Query()
	kinds := make([]cloud.Kind, 0)
	for _, k := range queryList(r, "kind") {
		kinds = append(kinds, cloud.Kind(k))
	}
	maxDepth, _ := strconv.Atoi(q.Get("max_depth"))
	return ports.TwinQuery{
		View:           view,
		AccountIDs:     QueryAccountIDs(r, "account_id"),
		Regions:        QueryRegions(r, "region"),
		Environments:   QueryEnvironments(r, "environment"),
		ApplicationID:  core.ID(q.Get("application_id")),
		WorkloadID:     core.ID(q.Get("workload_id")),
		Kinds:          kinds,
		RootID:         core.ID(q.Get("root_id")),
		MaxDepth:       maxDepth,
		MinMonthlyCost: QueryMoney(r, "min_monthly_cost"),
		Search:         q.Get("search"),
		Collapse:       QueryBool(r, "collapse"),
	}
}

func (h *twinHandler) Graph(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "architecture"
	}
	v, err := h.svc.Graph(r.Context(), p.TenantID, twinQueryFromRequest(r, view))
	respond(w, r, http.StatusOK, v, err)
}

func (h *twinHandler) Node(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	v, err := h.svc.Node(r.Context(), p.TenantID, PathID(r, "id"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *twinHandler) CostFlow(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermCostRead)
	if !ok {
		return
	}
	v, err := h.svc.CostFlow(r.Context(), p.TenantID, twinQueryFromRequest(r, "cost"))
	respond(w, r, http.StatusOK, v, err)
}

func (h *twinHandler) Rebuild(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermDiscoveryRun)
	if !ok {
		return
	}
	v, err := h.svc.Rebuild(r.Context(), p.TenantID)
	respond(w, r, http.StatusOK, v, err)
}

func (h *twinHandler) Dependents(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	v, err := h.svc.Dependents(r.Context(), p.TenantID, PathID(r, "id"), QueryInt(r, "max_depth", 5))
	respond(w, r, http.StatusOK, v, err)
}

// --- Resources tag: thin views over the same TwinService ------------------

// ListResources projects the architecture-view graph's nodes as a resource
// list. It accepts the same filters as Graph — a resource list is, in
// CloudOptix's model, an unfiltered-for-topology view of the same twin.
func (h *twinHandler) ListResources(w http.ResponseWriter, r *http.Request) {
	p, ok := authorize(w, r, core.PermResourceRead)
	if !ok {
		return
	}
	q := twinQueryFromRequest(r, "architecture")
	q.Collapse = false // a resource listing must show every resource, not a collapsed topology summary
	graph, err := h.svc.Graph(r.Context(), p.TenantID, q)
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"items": graph.Nodes,
		"total": graph.Stats.NodeCount,
	})
}

func (h *twinHandler) GetResource(w http.ResponseWriter, r *http.Request) {
	h.Node(w, r)
}
