package costing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func testCtx(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
}

func seedDailyCost(t *testing.T, repos ports.Repositories, tenant core.TenantID, start time.Time, days int, valueFn func(day int) float64) {
	t.Helper()
	records := make([]cost.Record, 0, days)
	for d := 0; d < days; d++ {
		day := start.AddDate(0, 0, d)
		records = append(records, cost.Record{
			ID: core.NewID("cst"), TenantID: tenant, AccountID: "111111111111",
			Period: core.NewPeriod(day, day.AddDate(0, 0, 1)), Granularity: cost.GranularityDaily,
			Service: "compute", ChargeType: cost.ChargeUsage, Basis: cost.BasisAmortized,
			Amount: core.USDollars(valueFn(d)), IngestedAt: time.Now().UTC(),
		})
	}
	_, err := repos.Costs.UpsertBatch(testCtx(tenant), tenant, records)
	require.NoError(t, err)
}

// wobble is small, deterministic pseudo-random noise with no period-7
// component (a multiplicative hash, not modular arithmetic tied to 7), so it
// never accidentally manufactures the weekly autocorrelation the seasonal
// forecast test relies on being absent from the "flat" and "trending" series.
func wobble(d int) float64 {
	x := uint32(d)*2654435761 + 1
	x ^= x >> 15
	x *= 2246822519
	x ^= x >> 13
	return float64(x%1000)/1000*6 - 3
}

func TestForecast_InsufficientDataRefuses(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_fc1")
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := &Service{Repos: repos, Clock: core.FixedClock{T: now}, ForecastLookbackDays: 60}

	seedDailyCost(t, repos, tenant, now.AddDate(0, 0, -3), 3, func(d int) float64 { return 100 })

	horizon := core.Period{Start: now, End: now.AddDate(0, 0, 7)}
	f, err := svc.Forecast(testCtx(tenant), tenant, ports.CostFilter{}, horizon)
	require.NoError(t, err)
	assert.Equal(t, cost.ForecastInsufficient, f.Method)
	assert.NotEmpty(t, f.Note)
}

func TestForecast_RunRateOnNoisyFlatSeries(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_fc2")
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	svc := &Service{Repos: repos, Clock: core.FixedClock{T: now}, ForecastLookbackDays: 60}

	seedDailyCost(t, repos, tenant, now.AddDate(0, 0, -20), 20, func(d int) float64 { return 100 + wobble(d) })

	horizon := core.Period{Start: now, End: now.AddDate(0, 0, 5)}
	f, err := svc.Forecast(testCtx(tenant), tenant, ports.CostFilter{}, horizon)
	require.NoError(t, err)
	assert.Equal(t, cost.ForecastRunRate, f.Method)
	assert.InDelta(t, 500, f.Expected.Units(), 60) // ~100/day * 5 days
}

func TestForecast_LinearTrendOnRisingSeries(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_fc3")
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	svc := &Service{Repos: repos, Clock: core.FixedClock{T: now}, ForecastLookbackDays: 60}

	seedDailyCost(t, repos, tenant, now.AddDate(0, 0, -18), 18, func(d int) float64 { return 100 + float64(d)*10 + wobble(d)*0.2 })

	horizon := core.Period{Start: now, End: now.AddDate(0, 0, 5)}
	f, err := svc.Forecast(testCtx(tenant), tenant, ports.CostFilter{}, horizon)
	require.NoError(t, err)
	assert.Equal(t, cost.ForecastLinearTrend, f.Method)
	assert.True(t, f.Expected.GreaterThan(core.USDollars(1000)), "a rising trend must project forward above the recent daily rate")
}

func TestForecast_SeasonalNaiveOnWeeklyPattern(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_fc4")
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	svc := &Service{Repos: repos, Clock: core.FixedClock{T: now}, ForecastLookbackDays: 60}

	seedDailyCost(t, repos, tenant, now.AddDate(0, 0, -28), 28, func(d int) float64 {
		weekday := d % 7
		if weekday == 5 || weekday == 6 {
			return 40 + wobble(d)*0.3
		}
		return 150 + wobble(d)*0.3
	})

	horizon := core.Period{Start: now, End: now.AddDate(0, 0, 7)}
	f, err := svc.Forecast(testCtx(tenant), tenant, ports.CostFilter{}, horizon)
	require.NoError(t, err)
	assert.Equal(t, cost.ForecastSeasonalNaive, f.Method)
}

func TestForecast_MonthToDateHorizon(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_fc5")
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := &Service{Repos: repos, Clock: core.FixedClock{T: now}, ForecastLookbackDays: 60}

	seedDailyCost(t, repos, tenant, now.AddDate(0, 0, -20), 20, func(d int) float64 { return 100 + wobble(d) })

	month := core.MonthOf(now)
	horizon := core.Period{Start: now, End: month.End}
	f, err := svc.Forecast(testCtx(tenant), tenant, ports.CostFilter{}, horizon)
	require.NoError(t, err)
	assert.Equal(t, cost.ForecastMonthToDate, f.Method)
}

func TestForecast_ConfidenceIsBounded(t *testing.T) {
	assert.GreaterOrEqual(t, forecastConfidence(1, 1, cost.ForecastLinearTrend, 100), 0.0)
	assert.LessOrEqual(t, forecastConfidence(1, 1, cost.ForecastLinearTrend, 100), 1.0)
	assert.LessOrEqual(t, forecastConfidence(0, 0, cost.ForecastRunRate, 1), forecastConfidence(1, 1, cost.ForecastLinearTrend, 100))
}
