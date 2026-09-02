package economics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func seedWasteRecommendation(t *testing.T, repos ports.Repositories, tenant core.TenantID, resourceID core.ID, saving core.Money) {
	t.Helper()
	rec := optimize.Recommendation{
		ID: core.NewID("rec"), TenantID: tenant,
		Finding:                optimize.Finding{ResourceID: resourceID, RuleID: "idle-instance", Category: optimize.CategoryWaste},
		EstimatedMonthlySaving: saving, Status: optimize.StatusOpen,
	}
	require.NoError(t, repos.Recommendations.SaveBatch(ctxFor(tenant), tenant, []optimize.Recommendation{rec}))
}

func TestEfficiencyScore_ComposesAllEightWeightedFactors(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)
	svc.Clock = core.FixedClock{T: testPeriod().End}

	score, err := svc.EfficiencyScore(ctxFor(tenant), tenant, econ.ScopeApplication, app1)
	require.NoError(t, err)

	require.Len(t, score.Factors, 8, "every StandardFactorWeights entry must produce exactly one factor")
	seen := map[string]bool{}
	var weightSum float64
	for _, f := range score.Factors {
		seen[f.Name] = true
		weightSum += f.Weight
		assert.GreaterOrEqual(t, f.Score, 0.0)
		assert.LessOrEqual(t, f.Score, 100.0)
		assert.NotEmpty(t, f.Detail, "factor %s must explain itself", f.Name)
	}
	for name := range econ.StandardFactorWeights {
		assert.True(t, seen[name], "factor %s from the standard weighting must be present", name)
	}
	assert.InDelta(t, 1.0, weightSum, 1e-9, "the eight factor weights must sum to one")

	assert.GreaterOrEqual(t, score.Score, 0.0)
	assert.LessOrEqual(t, score.Score, 100.0)
	assert.NotEmpty(t, score.Grade)
	// TotalSpend is the scope's own direct spend (alb 20 + web-1 100 + web-2
	// 100) — the same base resourcesForScope returns, not the footprint's
	// broader Direct+Indirect+Shared total.
	assert.Equal(t, core.USDollars(220), score.TotalSpend)
}

func TestEfficiencyScore_TracksPriorScoreAcrossRecomputation(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)
	svc.Clock = core.FixedClock{T: testPeriod().End}

	first, err := svc.EfficiencyScore(ctxFor(tenant), tenant, econ.ScopeApplication, app1)
	require.NoError(t, err)
	assert.Zero(t, first.PriorScore, "the first computation has no history to compare against")

	second, err := svc.EfficiencyScore(ctxFor(tenant), tenant, econ.ScopeApplication, app1)
	require.NoError(t, err)
	assert.Equal(t, first.Score, second.PriorScore)
	assert.InDelta(t, second.Score-first.Score, second.Delta, 1e-9)
}

func TestEfficiencyScore_WasteEliminationPenalizesIdentifiedFindings(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, res := econEstate(t, repos)
	svc := NewService(repos)
	svc.Clock = core.FixedClock{T: testPeriod().End}

	before, err := svc.EfficiencyScore(ctxFor(tenant), tenant, econ.ScopeApplication, app1)
	require.NoError(t, err)

	seedWasteRecommendation(t, repos, tenant, res["web-1"].ID, core.USDollars(90))

	after, err := svc.EfficiencyScore(ctxFor(tenant), tenant, econ.ScopeApplication, app1)
	require.NoError(t, err)

	var beforeWaste, afterWaste econ.EfficiencyFactor
	for _, f := range before.Factors {
		if f.Name == "waste_elimination" {
			beforeWaste = f
		}
	}
	for _, f := range after.Factors {
		if f.Name == "waste_elimination" {
			afterWaste = f
		}
	}
	assert.True(t, afterWaste.Score < beforeWaste.Score, "identifying waste must lower the waste_elimination factor, not raise it")
	assert.Equal(t, core.USDollars(90), afterWaste.Opportunity)
}
