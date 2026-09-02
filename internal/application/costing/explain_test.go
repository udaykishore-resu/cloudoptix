package costing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

func TestExplain_DecomposesMovementAndRanksContributors(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_ex1")
	svc := &Service{Repos: repos, Clock: core.SystemClock{}}

	baseline := core.Period{Start: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC)}
	current := core.Period{Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)}

	seedServiceDailyCost(t, repos, tenant, "NAT Gateway", baseline.Start, 7, func(int) float64 { return 10 })
	seedServiceDailyCost(t, repos, tenant, "NAT Gateway", current.Start, 7, func(int) float64 { return 100 }) // biggest mover
	seedServiceDailyCost(t, repos, tenant, "S3", baseline.Start, 7, func(int) float64 { return 20 })
	seedServiceDailyCost(t, repos, tenant, "S3", current.Start, 7, func(int) float64 { return 20 }) // unchanged

	explanation, err := svc.Explain(testCtx(tenant), tenant, current, baseline)
	require.NoError(t, err)

	assert.True(t, explanation.CurrentTotal.GreaterThan(explanation.BaselineTotal))
	assert.True(t, explanation.Delta.GreaterThan(core.ZeroUSD()))
	require.NotEmpty(t, explanation.Contributors)
	assert.Equal(t, "NAT Gateway", explanation.Contributors[0].Key, "the service that actually moved must rank first")
	assert.NotEmpty(t, explanation.Narrative)
	assert.Contains(t, explanation.Narrative, "NAT Gateway")
}

func TestExplain_LinksExecutedChangesWithinWindow(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_ex2")
	svc := &Service{Repos: repos, Clock: core.SystemClock{}}

	baseline := core.Period{Start: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC)}
	current := core.Period{Start: time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)}
	seedServiceDailyCost(t, repos, tenant, "EC2", baseline.Start, 7, func(int) float64 { return 200 })
	seedServiceDailyCost(t, repos, tenant, "EC2", current.Start, 7, func(int) float64 { return 100 })

	finishedAt := baseline.End.Add(12 * time.Hour)
	plan := execute.Plan{
		ID: core.NewID("pln"), TenantID: tenant, Action: optimize.ActionResizeInstance,
		Title: "Rightsize checkout-api", State: execute.PlanValidated, FinishedAt: &finishedAt,
		ExpectedMonthlySaving: core.USDollars(700), CreatedAt: baseline.Start,
	}
	require.NoError(t, repos.Executions.CreatePlan(testCtx(tenant), plan))

	explanation, err := svc.Explain(testCtx(tenant), tenant, current, baseline)
	require.NoError(t, err)
	require.NotEmpty(t, explanation.LinkedChanges)
	assert.Equal(t, "execution", explanation.LinkedChanges[0].Kind)
	assert.True(t, explanation.LinkedChanges[0].CostImpact.IsNegative(), "an executed optimization is a saving, so its cost impact must be negative")
}
