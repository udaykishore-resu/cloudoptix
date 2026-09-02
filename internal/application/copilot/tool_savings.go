package copilot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newSavingsFunnelTool answers "how do we cut cost by 30%" and "what's our
// savings funnel look like" by comparing the recommendation engine's
// identified savings potential (and where it leaks between rungs) against a
// requested cost-reduction target, when one is given.
func newSavingsFunnelTool(uow ports.UnitOfWork) ports.Tool {
	return savingsFunnelTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name: "get_savings_funnel",
		Description: "The savings funnel — potential, approved, planned, executed, validated and realized monthly savings, with " +
			"where value leaks between stages. Given target_pct, also reports whether that reduction target is achievable from " +
			"currently identified opportunities alone.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"target_pct": map[string]any{"type": "string", "description": "a cost-reduction percentage target, e.g. \"30\""},
			"days":       map[string]any{"type": "number"},
		}},
		ReadOnly: true, RequiredPermission: core.PermEconomicsRead,
	}}}
}

type savingsFunnelTool struct{ baseTool }

func (t savingsFunnelTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	period := periodFromArgs(args)
	targetPct := parseTargetPct(argString(args, "target_pct", ""))
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		funnel, err := repos.Savings.Funnel(ctx, tenant, period)
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "The savings funnel has %s/mo of potential savings identified, of which %s/mo is approved, %s/mo planned, %s/mo executed and %s/mo actually realized (%.0f%% prediction accuracy).",
			money(funnel.Potential), money(funnel.Approved), money(funnel.Planned), money(funnel.Executed), money(funnel.Realized), funnel.PredictionAccuracy*100)
		if len(funnel.Leakage) > 0 {
			worst := funnel.Leakage[0]
			for _, l := range funnel.Leakage {
				if l.Amount.GreaterThan(worst.Amount) {
					worst = l
				}
			}
			fmt.Fprintf(&b, " The biggest leak is between %s and %s, losing %s/mo (%.0f%% conversion).", worst.From, worst.To, money(worst.Amount), worst.Rate*100)
		}
		if targetPct > 0 {
			total, terr := repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: period})
			if terr == nil {
				targetAmount := total.Scale(targetPct / 100)
				if funnel.Potential.GreaterThan(targetAmount) || funnel.Potential.Cmp(targetAmount) == 0 {
					fmt.Fprintf(&b, " Cutting cost by %.0f%% means finding %s/mo — the funnel already has %s/mo identified, so the target is reachable from open recommendations alone if they are approved and executed.",
						targetPct, money(targetAmount), money(funnel.Potential))
				} else {
					shortfall := targetAmount.MustSub(funnel.Potential)
					fmt.Fprintf(&b, " Cutting cost by %.0f%% means finding %s/mo — currently identified opportunities cover %s/mo, leaving a %s/mo shortfall that will need new recommendations or a structural change.",
						targetPct, money(targetAmount), money(funnel.Potential), money(shortfall))
				}
			}
		}
		return toolResult(b.String(), map[string]any{
			"potential_usd": funnel.Potential.Units(), "approved_usd": funnel.Approved.Units(),
			"realized_usd": funnel.Realized.Units(), "prediction_accuracy": funnel.PredictionAccuracy,
		}), nil
	})
	if err != nil {
		return toolError("could not load the savings funnel: %v", err), nil
	}
	return res, nil
}

func parseTargetPct(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
