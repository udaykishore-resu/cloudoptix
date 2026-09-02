package copilot

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newRunCounterfactualTool answers "what happens if traffic doubles" /
// "what if we cut X in half" scenario questions.
//
// KEY DESIGN DECISION — this is a deliberately coarse, transparently-stated
// linear projection (projected = current total cost x multiplier), not a
// call into a full architecture simulator. CloudOptix's simulation domain
// (internal/domain/simulate) models named what-if scenarios against a
// specific architecture, which is out of reach for a generic "what if
// traffic doubles" question with no scenario definition supplied. Every
// number here is grounded in the tenant's real current spend, and the
// summary says outright that it assumes uniform scaling — a caveat, not a
// fabrication, and one a GroundingVerifier cannot catch because it is
// stated as an assumption rather than asserted as fact.
func newRunCounterfactualTool(uow ports.UnitOfWork) ports.Tool {
	return counterfactualTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "run_counterfactual",
		Description: "Projects cost under a traffic or usage multiplier (e.g. 2.0 for traffic doubling, 0.5 for halved), assuming cost scales linearly with usage — a coarse estimate, not an architecture-aware simulation.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"multiplier": map[string]any{"type": "number", "description": "usage multiplier, e.g. 2.0 for doubled traffic"},
			"days":       map[string]any{"type": "number"},
		}},
		ReadOnly: true, RequiredPermission: core.PermCostRead,
	}}}
}

type counterfactualTool struct{ baseTool }

func (t counterfactualTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	multiplier := argFloat(args, "multiplier", 1.5)
	if multiplier <= 0 {
		multiplier = 1.5
	}
	period := periodFromArgs(args)
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		total, err := repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: period})
		if err != nil {
			return nil, err
		}
		projected := total.Scale(multiplier)
		delta := projected.MustSub(total)
		direction := "increase"
		if multiplier < 1 {
			direction = "decrease"
		}
		summary := fmt.Sprintf(
			"Assuming cost scales linearly with usage, a %.1fx change in traffic would %s monthly cost from %s to about %s (a change of %s). "+
				"This is a coarse, uniform-scaling estimate — reserved capacity and storage typically do not scale with traffic the way compute and data transfer do, so the real number is likely lower than a pure linear projection for a traffic increase.",
			multiplier, direction, money(total), money(projected), money(delta.Abs()))
		return toolResult(summary, map[string]any{
			"multiplier": multiplier, "current_monthly_usd": total.Units(), "projected_monthly_usd": projected.Units(),
			"delta_usd": delta.Units(),
		}), nil
	})
	if err != nil {
		return toolError("could not run counterfactual: %v", err), nil
	}
	return res, nil
}
