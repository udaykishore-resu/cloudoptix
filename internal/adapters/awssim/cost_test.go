package awssim

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func sessionFor(t *testing.T, e *Estate, scope cloud.RoleScope) ports.AWSSession {
	t.Helper()
	broker := NewBroker(e, cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute)
	s, err := broker.Assume(context.Background(), cloud.AWSAccount{AccountID: e.AccountID}, scope)
	require.NoError(t, err)
	return s
}

func TestCostIngestor_ReconcilesWithEstateTotal(t *testing.T) {
	e := BuildDemoEstate()
	ing := NewCostIngestor()
	period := core.NewPeriod(demoNow.AddDate(0, 0, -30), demoNow)

	records, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: testTenant, Session: sessionFor(t, e, cloud.ScopeAnalyze),
		Account: cloud.AWSAccount{AccountID: e.AccountID}, Period: period, Granularity: cost.GranularityDaily,
	})
	require.NoError(t, err)
	require.NotEmpty(t, records)

	total := core.ZeroUSD()
	for _, r := range records {
		total = total.MustAdd(r.Amount)
	}
	expected := e.TotalMonthlyCost().Units() * (period.Days() / core.AverageDaysPerMonth)
	assert.InDelta(t, expected, total.Units(), expected*0.001, "30 days of records should reconcile to ~30/30.4375 of the monthly total")
}

func TestCostIngestor_DailyRecordsCoverEveryDay(t *testing.T) {
	e := BuildDemoEstate()
	ing := NewCostIngestor()
	period := core.NewPeriod(demoNow.AddDate(0, 0, -7), demoNow)

	records, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: testTenant, Session: sessionFor(t, e, cloud.ScopeAnalyze),
		Account: cloud.AWSAccount{AccountID: e.AccountID}, Period: period,
	})
	require.NoError(t, err)

	days := map[string]bool{}
	for _, r := range records {
		days[r.Period.Start.Format("2006-01-02")] = true
		assert.Equal(t, cost.GranularityDaily, r.Granularity)
		assert.Equal(t, "simulator", r.Source)
		assert.Equal(t, cost.ChargeUsage, r.ChargeType)
		assert.NotEmpty(t, r.Service)
		assert.NotEmpty(t, r.UsageType)
	}
	assert.Equal(t, 7, len(days), "expected exactly 7 distinct days of records")
}

func TestCostIngestor_WeeklySeasonalityOnVariableSpend(t *testing.T) {
	e := BuildDemoEstate()
	ing := NewCostIngestor()
	period := core.NewPeriod(demoNow.AddDate(0, 0, -28), demoNow)

	records, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: testTenant, Session: sessionFor(t, e, cloud.ScopeAnalyze),
		Account: cloud.AWSAccount{AccountID: e.AccountID}, Period: period,
	})
	require.NoError(t, err)

	weekdayTotal, weekendTotal := 0.0, 0.0
	weekdayDays, weekendDays := map[string]bool{}, map[string]bool{}
	for _, r := range records {
		if r.Service != "Amazon CloudFront" {
			continue
		}
		key := r.Period.Start.Format("2006-01-02")
		wd := r.Period.Start.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			weekendTotal += r.Amount.Units()
			weekendDays[key] = true
		} else {
			weekdayTotal += r.Amount.Units()
			weekdayDays[key] = true
		}
	}
	require.NotZero(t, len(weekdayDays))
	require.NotZero(t, len(weekendDays))
	weekdayAvg := weekdayTotal / float64(len(weekdayDays))
	weekendAvg := weekendTotal / float64(len(weekendDays))
	assert.Greater(t, weekendAvg, weekdayAvg, "CloudFront (traffic-driven) spend should be higher on weekends")
}

func TestCostIngestor_AnomalyIsDetectable(t *testing.T) {
	e := BuildDemoEstate()
	ing := NewCostIngestor()
	period := core.NewPeriod(demoNow.AddDate(0, 0, -30), demoNow)

	records, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: testTenant, Session: sessionFor(t, e, cloud.ScopeAnalyze),
		Account: cloud.AWSAccount{AccountID: e.AccountID}, Period: period,
	})
	require.NoError(t, err)

	byDay := map[string]float64{}
	for _, r := range records {
		if r.UsageType != "NatGateway-Bytes" {
			continue
		}
		byDay[r.Period.Start.Format("2006-01-02")] += r.Amount.Units()
	}
	require.NotEmpty(t, byDay)

	var sum, max float64
	var maxDay string
	for day, v := range byDay {
		sum += v
		if v > max {
			max, maxDay = v, day
		}
	}
	mean := sum / float64(len(byDay))
	assert.Greater(t, max, mean*2.5, "the anomaly day (%s) should be a clear outlier against the %.2f daily mean", maxDay, mean)
}

func TestCostIngestor_Deterministic(t *testing.T) {
	e := BuildDemoEstate()
	ing := NewCostIngestor()
	period := core.NewPeriod(demoNow.AddDate(0, 0, -10), demoNow)
	in := ports.CostIngestInput{
		TenantID: testTenant, Session: sessionFor(t, e, cloud.ScopeAnalyze),
		Account: cloud.AWSAccount{AccountID: e.AccountID}, Period: period,
	}

	r1, err := ing.Fetch(context.Background(), in)
	require.NoError(t, err)
	r2, err := ing.Fetch(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, len(r1), len(r2))
	for i := range r1 {
		assert.Equal(t, r1[i].Amount.Micros(), r2[i].Amount.Micros())
	}
}

func TestCostIngestor_EmptyPeriod(t *testing.T) {
	e := BuildDemoEstate()
	ing := NewCostIngestor()
	records, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: testTenant, Session: sessionFor(t, e, cloud.ScopeAnalyze),
		Account: cloud.AWSAccount{AccountID: e.AccountID}, Period: core.Period{},
	})
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestCostIngestor_Available(t *testing.T) {
	e := BuildDemoEstate()
	ing := NewCostIngestor()
	assert.True(t, ing.Available(context.Background(), sessionFor(t, e, cloud.ScopeAnalyze), cloud.AWSAccount{}))
	assert.Equal(t, "simulator", ing.Source())
}
