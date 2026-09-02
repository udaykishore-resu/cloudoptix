package economics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
)

func TestCompute_PricesEveryScopeEntityAndTransaction(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)
	svc.Clock = core.FixedClock{T: testPeriod().End}

	wl := core.NewID("wl")
	require.NoError(t, repos.Applications.UpsertWorkload(ctxFor(tenant), cloudWorkload(tenant, app1, wl)))
	seedTransaction(t, repos, tenant, []core.ID{wl}, econ.VolumeSource{Kind: "declared", DeclaredMonthly: 500})

	result, err := svc.Compute(ctxFor(tenant), tenant, testPeriod())
	require.NoError(t, err)

	// Organization + 2 applications + 1 workload, at minimum (accounts and
	// environments depend on whether any AWSAccount was onboarded, which
	// this fixture does not do).
	assert.GreaterOrEqual(t, result.FootprintsComputed, 4)
	assert.Equal(t, 1, result.TransactionsPriced)
	assert.False(t, result.TotalAttributed.IsNegative())
	assert.GreaterOrEqual(t, result.Coverage, 0.0)
	assert.LessOrEqual(t, result.Coverage, 1.0)

	orgFP, err := repos.Economics.GetFootprint(ctxFor(tenant), tenant, econ.ScopeOrganization, core.ID(tenant), testPeriod())
	require.NoError(t, err)
	assert.Equal(t, result.TotalAttributed.Micros(), orgFP.Total.Micros())
	assert.Equal(t, result.TotalUnattributed.Micros(), orgFP.Unattributed.Micros())

	// A fresh organization-level efficiency score must have been computed as
	// a side effect, ready for ExecutiveSummary to read.
	es, err := repos.Economics.GetEfficiencyScore(ctxFor(tenant), tenant, econ.ScopeOrganization, core.ID(tenant))
	require.NoError(t, err)
	assert.Greater(t, es.Score, 0.0)
}

func TestExecutiveSummary_AssemblesEveryEngineIntoOneView(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, res := econEstate(t, repos)
	svc := NewService(repos)
	svc.Clock = core.FixedClock{T: testPeriod().End}

	seedWasteRecommendation(t, repos, tenant, res["web-1"].ID, core.USDollars(50))
	_, err := svc.UpsertCostSLO(ctxFor(tenant), econ.CostSLO{
		TenantID: tenant, Name: "ceiling", Kind: econ.SLOAbsoluteSpend, Direction: econ.DirectionAtMost,
		Scope: econ.ScopeApplication, ScopeID: app1, Target: core.USDollars(10),
		Window: econ.WindowRolling30d, Enabled: true,
	})
	require.NoError(t, err)
	_, err = svc.EvaluateSLOs(ctxFor(tenant), tenant)
	require.NoError(t, err)

	summary, err := svc.ExecutiveSummary(ctxFor(tenant), tenant)
	require.NoError(t, err)

	assert.False(t, summary.MonthlySpend.IsNegative())
	assert.NotEmpty(t, summary.EfficiencyGrade)
	assert.Equal(t, 1, summary.CostSLOsBreached+summary.CostSLOsAtRisk+summary.CostSLOsHealthy)
	assert.NotZero(t, summary.GeneratedAt)
	assert.NotEmpty(t, summary.TopOpportunities)
	assert.Equal(t, core.USDollars(50), summary.TopOpportunities[0].EstimatedMonthlySaving)
}

// cloudWorkload builds a minimal cloud.Workload for tests that only need a
// workload's identity, not its full onboarding detail.
func cloudWorkload(tenant core.TenantID, appID, id core.ID) cloud.Workload {
	return cloud.Workload{ID: id, TenantID: tenant, ApplicationID: appID, Name: "checkout-api", Type: cloud.WorkloadAPI, Platform: cloud.PlatformEC2}
}
