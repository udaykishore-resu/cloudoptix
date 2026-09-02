package economics

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
)

// EvaluateSLOs prices every enabled Cost SLO's current actual value and
// evaluates its error-budget position, persisting each result so
// BudgetStates and ExecutiveSummary can read it back cheaply.
func (s *Service) EvaluateSLOs(ctx context.Context, tenant core.TenantID) ([]econ.EconomicErrorBudget, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	slos, err := s.Repos.Economics.ListCostSLOs(ctx, tenant)
	if err != nil {
		return nil, err
	}
	now := s.clock().Now()
	out := make([]econ.EconomicErrorBudget, 0, len(slos))
	for _, slo := range slos {
		if !slo.Enabled {
			continue
		}
		budget, everr := s.evaluateOne(ctx, tenant, slo, now)
		if everr != nil {
			// One misconfigured SLO (e.g. naming a transaction that was
			// since deleted) must not hide every other SLO's evaluation.
			continue
		}
		_ = s.Repos.Economics.SaveBudgetState(ctx, budget)
		out = append(out, budget)
	}
	return out, nil
}

// evaluateOne prices an SLO's actual value and evaluates its budget,
// switching between econ.EvaluateBudget (ceiling objectives) and
// evaluateFloorSLO (the one floor objective, SLOEfficiencyScore) by
// Direction, and substituting TargetRatio into Target via moneyOfRatio for
// the two ratio-denominated kinds so both evaluators only ever need to read
// slo.Target.
func (s *Service) evaluateOne(ctx context.Context, tenant core.TenantID, slo econ.CostSLO, now time.Time) (econ.EconomicErrorBudget, error) {
	actual, err := s.actualForSLO(ctx, tenant, slo, now)
	if err != nil {
		return econ.EconomicErrorBudget{}, err
	}
	effective := slo
	if slo.Kind == econ.SLOWasteRatio || slo.Kind == econ.SLOEfficiencyScore {
		effective.Target = moneyOfRatio(slo.TargetRatio)
	}
	if effective.Direction == econ.DirectionAtLeast {
		return evaluateFloorSLO(effective, actual, now), nil
	}
	return econ.EvaluateBudget(effective, actual, now), nil
}

// actualForSLO prices the current actual value an SLO is measured against,
// drawing on whichever engine owns that figure: footprints for absolute
// spend, unit economics for per-transaction cost, and the efficiency score
// for the two ratio kinds.
func (s *Service) actualForSLO(ctx context.Context, tenant core.TenantID, slo econ.CostSLO, now time.Time) (core.Money, error) {
	period := slo.Window.Period(now)
	switch slo.Kind {
	case econ.SLOAbsoluteSpend:
		fp, err := s.Footprint(ctx, tenant, slo.Scope, slo.ScopeID, period)
		if err != nil {
			return core.ZeroUSD(), err
		}
		return fp.Total, nil
	case econ.SLOCostPerTransaction, econ.SLOCostPerRequest, econ.SLOCostPerCustomer:
		ue, err := s.UnitEconomics(ctx, tenant, slo.TransactionID, period)
		if err != nil {
			return core.ZeroUSD(), err
		}
		return ue.CostPerUnit, nil
	case econ.SLOWasteRatio:
		es, err := s.EfficiencyScore(ctx, tenant, slo.Scope, slo.ScopeID)
		if err != nil {
			return core.ZeroUSD(), err
		}
		return moneyOfRatio(es.WasteRatio), nil
	case econ.SLOEfficiencyScore:
		es, err := s.EfficiencyScore(ctx, tenant, slo.Scope, slo.ScopeID)
		if err != nil {
			return core.ZeroUSD(), err
		}
		return moneyOfRatio(es.Score / 100), nil
	default:
		return core.ZeroUSD(), core.Invalid("unsupported cost SLO kind %q", slo.Kind)
	}
}

// evaluateFloorSLO evaluates a Direction=AtLeast Cost SLO — today only
// SLOEfficiencyScore uses this direction.
//
// econ.EvaluateBudget's burn-rate math assumes the objective accumulates
// linearly toward the target as the window elapses, which is exactly right
// for a spend ceiling (a $100K/month budget really is consumed day by day)
// and exactly wrong for a floor: an efficiency score that is ten points
// under its floor on day one of the month is already a breach, not 3% of
// one, because nothing about a score "accumulates". Rather than force a
// floor through EvaluateBudget's pro-rating (which would require faking the
// evaluation instant and risks the SLO's Window resolving to a different
// period on the second call), this evaluates the shortfall against the
// target directly and reports the same EconomicErrorBudget shape so callers
// need no second code path. BurnRate and ExhaustionDate are left at their
// zero values — not applicable when nothing is accumulating over time.
func evaluateFloorSLO(slo econ.CostSLO, actual core.Money, now time.Time) econ.EconomicErrorBudget {
	period := slo.Window.Period(now)
	tolerance := slo.ErrorBudgetPct
	if tolerance <= 0 {
		tolerance = 0.05
	}
	budget := slo.Target.Scale(tolerance)
	shortfall := slo.Target.MustSub(actual)
	if shortfall.IsNegative() {
		shortfall = core.ZeroUSD()
	}
	b := econ.EconomicErrorBudget{
		ID: core.NewID("eeb"), TenantID: slo.TenantID, SLOID: slo.ID, SLOName: slo.Name,
		Kind: slo.Kind, Period: period, Target: slo.Target, Actual: actual,
		BudgetAmount: budget, Consumed: shortfall, EvaluatedAt: now.UTC(),
	}
	b.Remaining = b.BudgetAmount.MustSub(shortfall)
	if !budget.IsZero() {
		b.ConsumedRatio = shortfall.Ratio(budget)
	}
	switch {
	case shortfall.IsZero():
		b.State = econ.BudgetHealthy
	case b.ConsumedRatio >= 1:
		b.State = econ.BudgetExhausted
	case b.ConsumedRatio >= 0.75:
		b.State = econ.BudgetAtRisk
	case b.ConsumedRatio >= 0.5:
		b.State = econ.BudgetWatch
	default:
		b.State = econ.BudgetHealthy
	}
	if b.State == econ.BudgetExhausted {
		b.TriggeredActions = slo.BreachActions
		if len(b.TriggeredActions) == 0 {
			b.TriggeredActions = []econ.BreachAction{econ.ActionNotify, econ.ActionRequireApproval}
		}
	}
	b.Explanation = explainFloor(b)
	return b
}

func explainFloor(b econ.EconomicErrorBudget) string {
	switch b.State {
	case econ.BudgetHealthy:
		return fmt.Sprintf("At or above the %s floor (actual %s).", b.Target.Format(), b.Actual.Format())
	case econ.BudgetWatch, econ.BudgetAtRisk:
		return fmt.Sprintf("%.0f%% of the tolerated shortfall below the %s floor consumed (actual %s).",
			b.ConsumedRatio*100, b.Target.Format(), b.Actual.Format())
	case econ.BudgetExhausted:
		return fmt.Sprintf("Below floor beyond tolerance: actual %s against a %s floor.", b.Actual.Format(), b.Target.Format())
	default:
		return "Insufficient data to evaluate."
	}
}
