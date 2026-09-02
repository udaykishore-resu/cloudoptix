package copilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newListRecommendationsTool answers "what should we optimize first" / "what's
// wasting money" — the open recommendation queue, ranked by CloudOptix's own
// priority score (which already blends saving size, confidence and risk), so
// the copilot never has to invent its own ranking heuristic.
func newListRecommendationsTool(uow ports.UnitOfWork) ports.Tool {
	return listRecommendationsTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "list_recommendations",
		Description: "Lists open cost-optimization recommendations, ranked by priority, optionally filtered by category (waste, rightsizing, storage, commitment, network, architecture, scheduling, licensing, data_lifecycle, observability_cost).",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"category": map[string]any{"type": "string"},
			"limit":    map[string]any{"type": "number"},
		}},
		ReadOnly: true, RequiredPermission: core.PermRecommendRead,
	}}}
}

type listRecommendationsTool struct{ baseTool }

func (t listRecommendationsTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	filter := ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen}}
	if cat := argString(args, "category", ""); cat != "" {
		filter.Categories = []optimize.Category{optimize.Category(cat)}
	}
	limit := argInt(args, "limit", 5)
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		summary, err := repos.Recommendations.Summary(ctx, tenant)
		if err != nil {
			return nil, err
		}
		page, err := repos.Recommendations.List(ctx, tenant, filter, ports.ListOptions{Limit: limit, SortBy: "priority_score", Desc: true})
		if err != nil {
			return nil, err
		}
		if len(page.Items) == 0 {
			return toolResult("There are no open recommendations matching that filter right now.", nil), nil
		}
		var parts []string
		for i, r := range page.Items {
			parts = append(parts, fmt.Sprintf("%d) %s — %s/mo saving, %s risk (id %s)", i+1, r.Title, money(r.EstimatedMonthlySaving), r.Risk.Level, r.ID))
		}
		text := fmt.Sprintf("There are %d open recommendation(s) worth %s/mo in identified savings. Top by priority: %s.",
			summary.Open, money(summary.TotalMonthlySaving), strings.Join(parts, "; "))
		return toolResult(text, map[string]any{
			"open_count": summary.Open, "total_monthly_saving_usd": summary.TotalMonthlySaving.Units(),
			"recommendations": recommendationsToAny(page.Items),
		}), nil
	})
	if err != nil {
		return toolError("could not list recommendations: %v", err), nil
	}
	return res, nil
}

// newGetRecommendationTool answers "tell me more about recommendation X" —
// one recommendation's full rationale, blast radius and current status.
func newGetRecommendationTool(uow ports.UnitOfWork) ports.Tool {
	return getRecommendationTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_recommendation",
		Description: "Full detail for one recommendation by id: rationale, saving, confidence, risk and blast radius.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"recommendation_id": map[string]any{"type": "string"},
		}, "required": []string{"recommendation_id"}},
		ReadOnly: true, RequiredPermission: core.PermRecommendRead,
	}}}
}

type getRecommendationTool struct{ baseTool }

func (t getRecommendationTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	id := core.ID(firstArgString(args, "recommendation_id", "id"))
	if id == "" {
		return toolError("recommendation_id is required"), nil
	}
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		r, err := repos.Recommendations.Get(ctx, tenant, id)
		if err != nil {
			return nil, err
		}
		summary := fmt.Sprintf("%s (status %s): %s Saves an estimated %s/mo (%s confidence) with %s blast radius: %s",
			r.Title, r.Status, r.Rationale, money(r.EstimatedMonthlySaving), r.Confidence, r.Risk.Level, r.BlastRadius.Describe())
		return toolResult(summary, map[string]any{
			"id": r.ID, "status": r.Status, "monthly_saving_usd": r.EstimatedMonthlySaving.Units(),
			"risk": r.Risk.Level, "confidence": r.Confidence, "requires_approval": r.RequiresApproval,
			"auto_executable": r.AutoExecutable,
		}), nil
	})
	if err != nil {
		return toolError("could not find recommendation %s: %v", id, err), nil
	}
	return res, nil
}

// newBlastRadiusTool answers "what's our highest blast radius service" /
// "what would break if we changed X" by ranking open recommendations'
// precomputed blast radius, or, given a recommendation_id, explaining that
// one's blast radius directly.
func newBlastRadiusTool(uow ports.UnitOfWork) ports.Tool {
	return blastRadiusTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_blast_radius",
		Description: "The blast radius (resources, services, transactions and users affected) of one recommendation by id, or, with none given, the open recommendation with the largest blast radius.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"recommendation_id": map[string]any{"type": "string", "description": "a recommendation id (rec_...); alternatively pass resource_id to find the blast radius of recommendations affecting that resource"},
			"resource_id":       map[string]any{"type": "string"},
		}},
		ReadOnly: true, RequiredPermission: core.PermRecommendRead,
	}}}
}

type blastRadiusTool struct{ baseTool }

func (t blastRadiusTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	ident := firstArgString(args, "recommendation_id", "resource_id", "id")
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		var r optimize.Recommendation
		switch {
		case ident != "" && core.ID(ident).Prefix() == "rec":
			rec, err := repos.Recommendations.Get(ctx, tenant, core.ID(ident))
			if err != nil {
				return nil, err
			}
			r = rec
		case ident != "":
			page, err := repos.Recommendations.List(ctx, tenant, ports.RecommendationFilter{ResourceID: core.ID(ident)}, ports.ListOptions{Limit: 10})
			if err != nil {
				return nil, err
			}
			if len(page.Items) == 0 {
				return nil, fmt.Errorf("no recommendations reference %q", ident)
			}
			r = page.Items[0]
			for _, cand := range page.Items {
				if cand.BlastRadius.Score > r.BlastRadius.Score {
					r = cand
				}
			}
		default:
			page, err := repos.Recommendations.List(ctx, tenant, ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen}}, ports.ListOptions{Limit: 200})
			if err != nil {
				return nil, err
			}
			if len(page.Items) == 0 {
				return toolResult("There are no open recommendations to assess blast radius for.", nil), nil
			}
			r = page.Items[0]
			for _, cand := range page.Items {
				if cand.BlastRadius.Score > r.BlastRadius.Score {
					r = cand
				}
			}
		}
		summary := fmt.Sprintf("%q has the largest blast radius: %s", r.Title, r.BlastRadius.Describe())
		if ident != "" {
			summary = fmt.Sprintf("%q blast radius: %s", r.Title, r.BlastRadius.Describe())
		}
		if r.BlastRadius.Explanation != "" {
			summary += " " + r.BlastRadius.Explanation
		}
		return toolResult(summary, map[string]any{
			"recommendation_id": r.ID, "resources_affected": r.BlastRadius.ResourcesAffected,
			"services_affected": r.BlastRadius.ServicesAffected, "critical_services": r.BlastRadius.CriticalServices,
			"level": r.BlastRadius.Level, "score": r.BlastRadius.Score,
		}), nil
	})
	if err != nil {
		return toolError("could not assess blast radius: %v", err), nil
	}
	return res, nil
}

func recommendationsToAny(items []optimize.Recommendation) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, r := range items {
		out = append(out, map[string]any{
			"id": r.ID, "title": r.Title, "category": r.Finding.Category,
			"monthly_saving_usd": r.EstimatedMonthlySaving.Units(), "risk": r.Risk.Level,
			"confidence": r.Confidence, "status": r.Status,
		})
	}
	return out
}
