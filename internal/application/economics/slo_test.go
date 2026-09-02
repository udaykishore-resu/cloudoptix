package economics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
)

func TestEvaluateSLOs_AbsoluteSpendBurnRateAndExhaustionProjection(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)

	// Anchor "now" at exactly testPeriod()'s End and evaluate against a
	// rolling 30-day window, so slo.Window.Period(now) resolves to exactly
	// testPeriod() — the same window econEstate's cost fixture was seeded
	// against — and the SLO's priced "actual" is the exact $300 app1
	// footprint total (Direct 220 + Indirect 40 + Shared 40) computed in
	// the attribution tests. A rolling window evaluated precisely at its own
	// end also makes elapsed exactly 1 (the window has fully elapsed),
	// which keeps the pro-rating arithmetic exact rather than approximate.
	now := testPeriod().End
	svc.Clock = core.FixedClock{T: now}

	slo, err := svc.UpsertCostSLO(ctxFor(tenant), econ.CostSLO{
		TenantID: tenant, Name: "prod-ceiling", Kind: econ.SLOAbsoluteSpend, Direction: econ.DirectionAtMost,
		Scope: econ.ScopeApplication, ScopeID: app1, Target: core.USDollars(100),
		Window: econ.WindowRolling30d, ErrorBudgetPct: 0.05, Enabled: true,
	})
	require.NoError(t, err)
	assert.False(t, slo.ID.IsZero())

	budgets, err := svc.EvaluateSLOs(ctxFor(tenant), tenant)
	require.NoError(t, err)
	require.Len(t, budgets, 1)
	b := budgets[0]

	// Actual ($300) exceeds the $100 target outright, which EvaluateBudget
	// treats as a hard breach regardless of budget consumption, and the
	// burn rate (consumed/elapsed, with elapsed=1) must be well above 1x.
	assert.Equal(t, core.USDollars(300), b.Actual)
	assert.Equal(t, econ.BudgetBreached, b.State)
	assert.Greater(t, b.BurnRate, 1.0)
	assert.NotEmpty(t, b.Explanation)

	states, err := svc.BudgetStates(ctxFor(tenant), tenant)
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, b.SLOID, states[0].SLOID)
}

func TestEvaluateSLOs_HealthyWhenActualWellUnderTarget(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)
	svc.Clock = core.FixedClock{T: testPeriod().End}

	slo, err := svc.UpsertCostSLO(ctxFor(tenant), econ.CostSLO{
		TenantID: tenant, Name: "generous-ceiling", Kind: econ.SLOAbsoluteSpend, Direction: econ.DirectionAtMost,
		Scope: econ.ScopeApplication, ScopeID: app1, Target: core.USDollars(1_000_000),
		Window: econ.WindowRolling30d, Enabled: true,
	})
	require.NoError(t, err)

	budgets, err := svc.EvaluateSLOs(ctxFor(tenant), tenant)
	require.NoError(t, err)
	require.Len(t, budgets, 1)
	assert.Equal(t, econ.BudgetHealthy, budgets[0].State)
	assert.Equal(t, slo.ID, budgets[0].SLOID)
}

func TestEvaluateSLOs_DisabledSLOsAreSkipped(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)

	_, err := svc.UpsertCostSLO(ctxFor(tenant), econ.CostSLO{
		TenantID: tenant, Name: "off", Kind: econ.SLOAbsoluteSpend, Scope: econ.ScopeApplication, ScopeID: app1,
		Target: core.USDollars(10), Window: econ.WindowCalendarMonth, Enabled: false,
	})
	require.NoError(t, err)

	budgets, err := svc.EvaluateSLOs(ctxFor(tenant), tenant)
	require.NoError(t, err)
	assert.Empty(t, budgets)
}

func TestEvaluateSLOs_EfficiencyScoreFloorBreachesWhenBelowTarget(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)
	svc.Clock = core.FixedClock{T: testPeriod().End}

	// A floor of 95 is unreachable from an estate with essentially no
	// utilisation telemetry, no commitments and no policy coverage — every
	// factor lands well under 95, so the floor SLO must show a shortfall.
	_, err := svc.UpsertCostSLO(ctxFor(tenant), econ.CostSLO{
		TenantID: tenant, Name: "efficiency-floor", Kind: econ.SLOEfficiencyScore, Direction: econ.DirectionAtLeast,
		Scope: econ.ScopeApplication, ScopeID: app1, TargetRatio: 0.95,
		Window: econ.WindowCalendarMonth, ErrorBudgetPct: 0.05, Enabled: true,
	})
	require.NoError(t, err)

	budgets, err := svc.EvaluateSLOs(ctxFor(tenant), tenant)
	require.NoError(t, err)
	require.Len(t, budgets, 1)
	b := budgets[0]

	assert.NotEqual(t, econ.BudgetHealthy, b.State, "an unreachable efficiency floor must not report healthy")
	assert.True(t, b.Consumed.GreaterThan(core.ZeroUSD()) || b.State == econ.BudgetExhausted)
	// A floor SLO's actual is reported as the real (unmirrored) score.
	assert.False(t, b.Actual.IsNegative())
}

func TestEvaluateSLOs_EfficiencyScoreFloorHealthyWhenAboveTarget(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)

	_, err := svc.UpsertCostSLO(ctxFor(tenant), econ.CostSLO{
		TenantID: tenant, Name: "trivial-floor", Kind: econ.SLOEfficiencyScore, Direction: econ.DirectionAtLeast,
		Scope: econ.ScopeApplication, ScopeID: app1, TargetRatio: 0.01,
		Window: econ.WindowCalendarMonth, Enabled: true,
	})
	require.NoError(t, err)

	budgets, err := svc.EvaluateSLOs(ctxFor(tenant), tenant)
	require.NoError(t, err)
	require.Len(t, budgets, 1)
	assert.Equal(t, econ.BudgetHealthy, budgets[0].State)
}
