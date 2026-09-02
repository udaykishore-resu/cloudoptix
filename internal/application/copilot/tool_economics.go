package copilot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newEconomicFootprintTool answers "what does checkout cost us" / "cost per
// transaction for X" at the direct+indirect+shared level, before it is
// normalized by volume (that normalization is get_unit_economics).
func newEconomicFootprintTool(uow ports.UnitOfWork) ports.Tool {
	return economicFootprintTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_economic_footprint",
		Description: "The direct, indirect and shared cost attributed to a business scope (organization, application, workload, api or transaction) over a period.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"scope":    map[string]any{"type": "string", "description": "organization|account|environment|application|workload|business_capability|api|transaction"},
			"scope_id": map[string]any{"type": "string", "description": "id of the scoped entity; omit for organization scope"},
			"days":     map[string]any{"type": "number"},
		}},
		ReadOnly: true, RequiredPermission: core.PermEconomicsRead,
	}}}
}

type economicFootprintTool struct{ baseTool }

func (t economicFootprintTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	scope := econ.Scope(argString(args, "scope", string(econ.ScopeOrganization)))
	scopeID := core.ID(argString(args, "scope_id", ""))
	period := periodFromArgs(args)
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		fp, err := repos.Economics.GetFootprint(ctx, tenant, scope, scopeID, period)
		if err != nil {
			return nil, err
		}
		summary := fmt.Sprintf("%s's total cost over the last %.0f days was %s (direct %s, indirect %s, shared %s).",
			footprintLabel(fp), period.Days(), money(fp.Total), money(fp.Direct), money(fp.Indirect), money(fp.Shared))
		if fp.Unattributed.Units() > 0 {
			summary += fmt.Sprintf(" %s (%s of the total) could not be confidently attributed.", money(fp.Unattributed), pct(1-fp.Coverage))
		}
		if fp.ChangePct != 0 {
			dir := "up"
			if fp.ChangePct < 0 {
				dir = "down"
			}
			summary += fmt.Sprintf(" That is %s %s versus the prior period.", dir, pct(absf(fp.ChangePct)))
		}
		return toolResult(summary, map[string]any{
			"total_usd": fp.Total.Units(), "direct_usd": fp.Direct.Units(),
			"indirect_usd": fp.Indirect.Units(), "shared_usd": fp.Shared.Units(),
			"coverage": fp.Coverage,
		}), nil
	})
	if err != nil {
		return toolError("could not load economic footprint: %v", err), nil
	}
	return res, nil
}

func footprintLabel(fp econ.Footprint) string {
	if fp.Label != "" {
		return fp.Label
	}
	return string(fp.Scope)
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// newUnitEconomicsTool answers "what does it cost per transaction/order/API
// call" — the volume-normalized figure, with the direct/shared split and
// what is driving any change.
func newUnitEconomicsTool(uow ports.UnitOfWork) ports.Tool {
	return unitEconomicsTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_unit_economics",
		Description: "Cost per unit (e.g. cost per transaction, per order, per API call) for a named business transaction, with drivers of any change.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"transaction": map[string]any{"type": "string", "description": "business transaction name, e.g. checkout, payment_processed"},
			"days":        map[string]any{"type": "number"},
		}},
		ReadOnly: true, RequiredPermission: core.PermEconomicsRead,
	}}}
}

type unitEconomicsTool struct{ baseTool }

func (t unitEconomicsTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	name := argString(args, "transaction", "")
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		var txID core.ID
		var txName string
		if name != "" {
			tx, err := repos.Economics.GetTransactionByName(ctx, tenant, name)
			if err != nil {
				return nil, err
			}
			txID, txName = tx.ID, tx.Name
		} else {
			txs, err := repos.Economics.ListTransactions(ctx, tenant)
			if err != nil {
				return nil, err
			}
			if len(txs) == 0 {
				return nil, fmt.Errorf("no business transactions are configured for this tenant")
			}
			txID, txName = txs[0].ID, txs[0].Name
		}
		now := time.Now().UTC()
		series, err := repos.Economics.ListUnitEconomics(ctx, tenant, txID, now.AddDate(0, 0, -90), now)
		if err != nil {
			return nil, err
		}
		if len(series) == 0 {
			return toolResult(fmt.Sprintf("No unit economics have been computed yet for %s.", txName), nil), nil
		}
		latest := series[len(series)-1]
		summary := fmt.Sprintf("Cost per %s is %s, from %s total cost across %.0f units.",
			txName, money(latest.CostPerUnit), money(latest.TotalCost), latest.Volume)
		if latest.ChangePct != 0 {
			dir := "up"
			if latest.ChangePct < 0 {
				dir = "down"
			}
			summary += fmt.Sprintf(" That is %s %s versus the prior period.", dir, pct(absf(latest.ChangePct)))
			if len(latest.Drivers) > 0 {
				var parts []string
				for _, d := range latest.Drivers {
					parts = append(parts, fmt.Sprintf("%s (%s)", d.Kind, d.Explanation))
				}
				summary += " Drivers: " + strings.Join(parts, "; ") + "."
			}
		}
		return toolResult(summary, map[string]any{
			"transaction": txName, "cost_per_unit_usd": latest.CostPerUnit.Units(),
			"volume": latest.Volume, "total_cost_usd": latest.TotalCost.Units(),
		}), nil
	})
	if err != nil {
		return toolError("could not load unit economics: %v", err), nil
	}
	return res, nil
}

// newEfficiencyScoreTool answers "how efficient are we" / "where is waste
// coming from" with the composite efficiency score and its factor breakdown.
func newEfficiencyScoreTool(uow ports.UnitOfWork) ports.Tool {
	return efficiencyScoreTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_efficiency_score",
		Description: "The composite cost-efficiency score (0-100) for a scope, its letter grade, waste ratio, and the factors driving it.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"scope":    map[string]any{"type": "string", "description": "organization|account|environment|application|workload"},
			"scope_id": map[string]any{"type": "string"},
		}},
		ReadOnly: true, RequiredPermission: core.PermEconomicsRead,
	}}}
}

type efficiencyScoreTool struct{ baseTool }

func (t efficiencyScoreTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	scope := econ.Scope(argString(args, "scope", string(econ.ScopeOrganization)))
	scopeID := core.ID(argString(args, "scope_id", ""))
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		s, err := repos.Economics.GetEfficiencyScore(ctx, tenant, scope, scopeID)
		if err != nil {
			return nil, err
		}
		summary := fmt.Sprintf("%s scores %.0f/100 (grade %s) on cost efficiency; %s of spend (%s) is identified waste.",
			efficiencyLabel(s), s.Score, s.Grade, pct(s.WasteRatio), money(s.IdentifiedWaste))
		if s.Delta != 0 {
			dir := "up"
			if s.Delta < 0 {
				dir = "down"
			}
			summary += fmt.Sprintf(" That score is %s %.1f points from the prior period.", dir, absf(s.Delta))
		}
		if len(s.Factors) > 0 {
			worst := s.Factors[0]
			for _, f := range s.Factors {
				if f.Score < worst.Score {
					worst = f
				}
			}
			summary += fmt.Sprintf(" The weakest factor is %s at %.0f/100.", worst.Name, worst.Score)
		}
		return toolResult(summary, map[string]any{
			"score": s.Score, "grade": s.Grade, "waste_ratio": s.WasteRatio,
			"identified_waste_usd": s.IdentifiedWaste.Units(), "total_spend_usd": s.TotalSpend.Units(),
		}), nil
	})
	if err != nil {
		return toolError("could not load efficiency score: %v", err), nil
	}
	return res, nil
}

func efficiencyLabel(s econ.EfficiencyScore) string {
	if s.Label != "" {
		return s.Label
	}
	return string(s.Scope)
}

// newCostSLOStatusTool answers "are we within budget" / "what's our error
// budget burn" against every configured cost SLO.
func newCostSLOStatusTool(uow ports.UnitOfWork) ports.Tool {
	return costSLOStatusTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_cost_slo_status",
		Description: "Status of every configured cost SLO / budget: consumed ratio, burn rate and projected overage.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		ReadOnly:    true, RequiredPermission: core.PermEconomicsRead,
	}}}
}

type costSLOStatusTool struct{ baseTool }

func (t costSLOStatusTool) Invoke(ctx context.Context, tenant core.TenantID, _ map[string]any) (any, error) {
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		states, err := repos.Economics.ListBudgetStates(ctx, tenant)
		if err != nil {
			return nil, err
		}
		if len(states) == 0 {
			return toolResult("No cost SLOs are configured for this tenant.", nil), nil
		}
		var parts []string
		var atRisk int
		for _, b := range states {
			parts = append(parts, fmt.Sprintf("%s is %s (%s of budget consumed, burn rate %.1fx)", b.SLOName, b.State, pct(b.ConsumedRatio), b.BurnRate))
			if b.State == econ.BudgetAtRisk || b.State == econ.BudgetExhausted || b.State == econ.BudgetBreached {
				atRisk++
			}
		}
		summary := fmt.Sprintf("%d cost SLO(s) tracked, %d at risk or breached. ", len(states), atRisk) + strings.Join(parts, "; ") + "."
		return toolResult(summary, map[string]any{"budgets": budgetStatesToAny(states)}), nil
	})
	if err != nil {
		return toolError("could not load cost SLO status: %v", err), nil
	}
	return res, nil
}

func budgetStatesToAny(states []econ.EconomicErrorBudget) []map[string]any {
	out := make([]map[string]any, 0, len(states))
	for _, b := range states {
		out = append(out, map[string]any{
			"slo_name": b.SLOName, "state": b.State, "consumed_ratio": b.ConsumedRatio,
			"burn_rate": b.BurnRate, "remaining_usd": b.Remaining.Units(),
		})
	}
	return out
}
