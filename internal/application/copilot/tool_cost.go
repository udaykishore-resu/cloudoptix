package copilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newCostSummaryTool answers "how much are we spending" / "cost summary" /
// "total cost" with the trailing-30-day total and the top three services by
// spend, so a single call already covers the most common opening question.
func newCostSummaryTool(uow ports.UnitOfWork) ports.Tool {
	return costSummaryTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_cost_summary",
		Description: "Total AWS cost over a period, with the top services by spend.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"days": map[string]any{"type": "number", "description": "Trailing window in days; default 30."},
		}},
		ReadOnly: true, RequiredPermission: core.PermCostRead,
	}}}
}

type costSummaryTool struct{ baseTool }

func (t costSummaryTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	period := periodFromArgs(args)
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		total, err := repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: period})
		if err != nil {
			return nil, err
		}
		bd, err := repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: period}, "service")
		if err != nil {
			return nil, err
		}
		top := bd.Items
		if len(top) > 3 {
			top = top[:3]
		}
		var parts []string
		for _, it := range top {
			parts = append(parts, fmt.Sprintf("%s (%s, %s)", serviceLabel(it), money(it.Amount), pct(it.Share)))
		}
		summary := fmt.Sprintf("Total spend over the last %.0f days was %s.", period.Days(), money(total))
		if len(parts) > 0 {
			summary += " The largest contributors were " + strings.Join(parts, ", ") + "."
		}
		return toolResult(summary, map[string]any{
			"total_usd": total.Units(), "period_days": period.Days(), "top_services": breakdownItemsToAny(top),
		}), nil
	})
	if err != nil {
		return toolError("could not load cost summary: %v", err), nil
	}
	return res, nil
}

// newCostBreakdownTool answers "most expensive service" / "cost by X".
func newCostBreakdownTool(uow ports.UnitOfWork) ports.Tool {
	return costBreakdownTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_cost_breakdown",
		Description: "Cost broken down by a dimension: service, account, region, environment, application or usage_type.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"dimension": map[string]any{"type": "string", "description": "service|account|region|environment|application|usage_type"},
			"days":      map[string]any{"type": "number"},
		}},
		ReadOnly: true, RequiredPermission: core.PermCostRead,
	}}}
}

type costBreakdownTool struct{ baseTool }

func (t costBreakdownTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	period := periodFromArgs(args)
	dimension := argString(args, "dimension", "service")
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		bd, err := repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: period}, dimension)
		if err != nil {
			return nil, err
		}
		if len(bd.Items) == 0 {
			return toolResult(fmt.Sprintf("No cost data is available broken down by %s for the last %.0f days.", dimension, period.Days()), nil), nil
		}
		top := bd.Items[0]
		summary := fmt.Sprintf("By %s over the last %.0f days, %s is the largest at %s (%s of %s total).",
			dimension, period.Days(), serviceLabel(top), money(top.Amount), pct(top.Share), money(bd.Total))
		return toolResult(summary, map[string]any{
			"dimension": dimension, "total_usd": bd.Total.Units(), "items": breakdownItemsToAny(bd.Items),
		}), nil
	})
	if err != nil {
		return toolError("could not load cost breakdown: %v", err), nil
	}
	return res, nil
}

// newExplainCostChangeTool answers "why did cost increase" (period-over-
// period comparison) and "what Terraform change increased cost" (a specific
// compilation result) — see its compilation_id branch.
func newExplainCostChangeTool(uow ports.UnitOfWork) ports.Tool {
	return explainCostChangeTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name: "explain_cost_change",
		Description: "Explains what changed in cost: either the biggest service-level movers over the trailing period " +
			"(compared to the prior period of equal length), or, given a compilation_id, the cost impact of a specific " +
			"Terraform/CloudFormation change that was priced ahead of merge.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"days":           map[string]any{"type": "number"},
			"compilation_id": map[string]any{"type": "string", "description": "A prior simulate.CompilationResult id, when explaining a specific infrastructure change rather than a general trend."},
		}},
		ReadOnly: true, RequiredPermission: core.PermCostRead,
	}}}
}

type explainCostChangeTool struct{ baseTool }

func (t explainCostChangeTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	if compID := argString(args, "compilation_id", ""); compID != "" {
		return t.explainCompilation(ctx, tenant, compID)
	}
	return t.explainTrend(ctx, tenant, args)
}

func (t explainCostChangeTool) explainCompilation(ctx context.Context, tenant core.TenantID, compID string) (any, error) {
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		comp, err := repos.Simulations.GetCompilation(ctx, tenant, core.ID(compID))
		if err != nil {
			return nil, err
		}
		direction := "increased"
		if comp.MonthlyDelta.IsNegative() {
			direction = "decreased"
		}
		summary := fmt.Sprintf("The infrastructure change %q %s projected monthly cost by %s (from %s to %s, %s), across %d created, %d updated and %d deleted resources, at %s pricing coverage.",
			comp.Label, direction, money(comp.MonthlyDelta.Abs()), money(comp.BaselineMonthly), money(comp.ProjectedMonthly),
			pct(comp.DeltaPct), comp.CreatedCount, comp.UpdatedCount, comp.DeletedCount, pct(comp.Coverage))
		return toolResult(summary, map[string]any{
			"compilation_id": compID, "monthly_delta_usd": comp.MonthlyDelta.Units(),
			"baseline_usd": comp.BaselineMonthly.Units(), "projected_usd": comp.ProjectedMonthly.Units(),
		}), nil
	})
	if err != nil {
		return toolError("could not load compilation %s: %v", compID, err), nil
	}
	return res, nil
}

func (t explainCostChangeTool) explainTrend(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	period := periodFromArgs(args)
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		bd, err := repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: period}, "service")
		if err != nil {
			return nil, err
		}
		var risers []cost.BreakdownItem
		for _, it := range bd.Items {
			if it.ChangePct > 0.05 {
				risers = append(risers, it)
			}
		}
		if len(risers) == 0 {
			anomalies, aerr := repos.Costs.ListAnomalies(ctx, tenant, period.Start, period.End, ports.ListOptions{Limit: 3})
			if aerr == nil && len(anomalies.Items) > 0 {
				var parts []string
				var extra []map[string]any
				for _, a := range anomalies.Items {
					parts = append(parts, fmt.Sprintf("%s cost was %s against an expected %s (%+.0f%%)", a.Key, money(a.Actual), money(a.Expected), a.DeltaPct*100))
					extra = append(extra, map[string]any{
						"key": a.Key, "actual_usd": a.Actual.Units(), "expected_usd": a.Expected.Units(), "delta_usd": a.Delta.Units(),
					})
				}
				return toolResult("The clearest cost movements were anomalies CloudOptix flagged: "+strings.Join(parts, "; ")+".",
					map[string]any{"anomalies": extra}), nil
			}
			return toolResult(fmt.Sprintf("Cost over the last %.0f days looks stable — no service moved more than 5%% against the prior period.", period.Days()), nil), nil
		}
		var parts []string
		for _, r := range risers {
			parts = append(parts, fmt.Sprintf("%s rose %s to %s (up %s)", serviceLabel(r), pct(r.ChangePct), money(r.Amount), money(r.Amount.MustSub(r.PriorAmount))))
		}
		return toolResult("The main drivers of the cost change were: "+strings.Join(parts, "; ")+".", map[string]any{"risers": breakdownItemsToAny(risers)}), nil
	})
	if err != nil {
		return toolError("could not explain cost change: %v", err), nil
	}
	return res, nil
}

// serviceLabel prefers a breakdown item's human label, falling back to its
// raw key (the Cost Explorer dimension value) when no label was set.
func serviceLabel(it cost.BreakdownItem) string {
	if it.Label != "" {
		return it.Label
	}
	return it.Key
}

// breakdownItemsToAny renders a slice of cost.BreakdownItem as plain data
// for a tool result's structured payload.
func breakdownItemsToAny(items []cost.BreakdownItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"key": it.Key, "label": serviceLabel(it), "amount_usd": it.Amount.Units(), "share": it.Share,
		})
	}
	return out
}
