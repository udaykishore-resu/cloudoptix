package memstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func mkCostRecord(tenant core.TenantID, day time.Time, service string, resourceID core.ID, amount float64) cost.Record {
	return cost.Record{
		ID:         core.NewID("cst"),
		TenantID:   tenant,
		AccountID:  core.AccountID("111122223333"),
		Region:     core.Region("us-east-1"),
		Period:     core.NewPeriod(day, day.AddDate(0, 0, 1)),
		Service:    service,
		ResourceID: resourceID,
		ChargeType: cost.ChargeUsage,
		Basis:      cost.BasisAmortized,
		Amount:     core.USDollars(amount),
		Source:     "test",
		IngestedAt: time.Now().UTC(),
	}
}

func TestCostRepo_AggregationExactness(t *testing.T) {
	s := New()
	repo := s.Repositories().Costs
	ctx := ctxFor(tenantA)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	resA := core.NewID("res")
	resB := core.NewID("res")

	var records []cost.Record
	// Odd micro amounts, deliberately not round numbers, to catch any float
	// accumulation: $12.333333 * many records must still reconcile exactly
	// against the integer-micros total.
	for i := 0; i < 30; i++ {
		day := base.AddDate(0, 0, i)
		records = append(records, mkCostRecord(tenantA, day, "Amazon EC2", resA, 12.333333))
		records = append(records, mkCostRecord(tenantA, day, "Amazon RDS", resB, 4.010101))
	}
	n, err := repo.UpsertBatch(ctx, tenantA, records)
	require.NoError(t, err)
	require.Equal(t, len(records), n)

	f := ports.CostFilter{
		Period:      core.NewPeriod(base, base.AddDate(0, 0, 30)),
		Granularity: cost.GranularityDaily,
	}

	total, err := repo.Total(ctx, tenantA, f)
	require.NoError(t, err)

	var want int64
	for _, r := range records {
		want += r.Amount.Micros()
	}
	assert.Equal(t, want, total.Micros(), "Total must reconcile exactly with the sum of matched records' micros")

	series, err := repo.Series(ctx, tenantA, f)
	require.NoError(t, err)
	assert.Len(t, series.Points, 30, "one bucket per day in the period")
	var seriesSum int64
	for _, p := range series.Points {
		seriesSum += p.Amount.Micros()
	}
	assert.Equal(t, want, seriesSum, "Series buckets must sum to exactly the same total")

	breakdown, err := repo.Breakdown(ctx, tenantA, f, "service")
	require.NoError(t, err)
	var breakdownSum int64
	for _, item := range breakdown.Items {
		breakdownSum += item.Amount.Micros()
	}
	assert.Equal(t, want, breakdownSum, "Breakdown items must sum to exactly the same total")
	assert.Equal(t, want, breakdown.Total.Micros())
	// Shares must sum to (approximately) 1.
	var shareSum float64
	for _, item := range breakdown.Items {
		shareSum += item.Share
	}
	assert.InDelta(t, 1.0, shareSum, 0.0001)

	byResource, err := repo.ByResource(ctx, tenantA, f)
	require.NoError(t, err)
	var byResourceSum int64
	for _, amt := range byResource {
		byResourceSum += amt.Micros()
	}
	assert.Equal(t, want, byResourceSum)
}

func TestCostRepo_BasisDefaultsToAmortizedToAvoidDoubleCounting(t *testing.T) {
	s := New()
	repo := s.Repositories().Costs
	ctx := ctxFor(tenantA)

	day := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	amortized := mkCostRecord(tenantA, day, "Amazon EC2", core.NewID("res"), 100)
	amortized.Basis = cost.BasisAmortized
	unblended := amortized
	unblended.ID = core.NewID("cst")
	unblended.Basis = cost.BasisUnblended
	unblended.Amount = core.USDollars(97) // a different figure for the same usage

	_, err := repo.UpsertBatch(ctx, tenantA, []cost.Record{amortized, unblended})
	require.NoError(t, err)

	total, err := repo.Total(ctx, tenantA, ports.CostFilter{Period: core.NewPeriod(day, day.AddDate(0, 0, 1))})
	require.NoError(t, err)
	assert.Equal(t, amortized.Amount.Micros(), total.Micros(),
		"an unfiltered query must default to one basis, not sum every basis stored for the same usage")
}

func TestCostRepo_TenantIsolation(t *testing.T) {
	s := New()
	repo := s.Repositories().Costs

	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rec := mkCostRecord(tenantA, day, "Amazon EC2", core.NewID("res"), 50)
	_, err := repo.UpsertBatch(ctxFor(tenantA), tenantA, []cost.Record{rec})
	require.NoError(t, err)

	total, err := repo.Total(ctxFor(tenantB), tenantB, ports.CostFilter{})
	require.NoError(t, err)
	assert.True(t, total.IsZero(), "tenant B must see none of tenant A's cost records")

	_, err = repo.Total(ctxFor(tenantB), tenantA, ports.CostFilter{})
	require.Error(t, err)
}

func TestCostRepo_ApplicationFilterJoinsThroughResource(t *testing.T) {
	s := New()
	repos := s.Repositories()
	ctx := ctxFor(tenantA)

	res := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 0, nil)
	appID := core.NewID("app")
	res.ApplicationID = appID
	_, err := repos.Resources.UpsertBatch(ctx, tenantA, []cloud.Resource{res})
	require.NoError(t, err)

	day := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	attributed := mkCostRecord(tenantA, day, "Amazon EC2", res.ID, 30)
	unattributed := mkCostRecord(tenantA, day, "Amazon EC2", core.NewID("res"), 70)
	_, err = repos.Costs.UpsertBatch(ctx, tenantA, []cost.Record{attributed, unattributed})
	require.NoError(t, err)

	total, err := repos.Costs.Total(ctx, tenantA, ports.CostFilter{ApplicationID: appID})
	require.NoError(t, err)
	assert.Equal(t, attributed.Amount.Micros(), total.Micros())
}
