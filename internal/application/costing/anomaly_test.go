package costing

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestRobustZ_CatchesWhatMeanBasedWouldMiss is the core claim of this
// package's anomaly detector: a single large outlier inside the training
// window drags a mean/stddev estimator's baseline toward itself, hiding a
// real, more moderate deviation on the day being tested. The median/MAD
// estimator ignores the outlier's magnitude entirely (only its rank matters)
// and so still flags the real deviation.
func TestRobustZ_CatchesWhatMeanBasedWouldMiss(t *testing.T) {
	train := make([]float64, 14)
	for i := range train {
		train[i] = 100
	}
	train[5] = 5000    // one wild day inside the training window
	testValue := 150.0 // a real, moderate elevation on the day under test

	median, mad := medianAndMAD(train)
	robust := robustZ(testValue, median, mad)

	meanBased := meanZ(testValue, train)

	assert.Less(t, math.Abs(meanBased), 3.5, "the mean/stddev estimator is dragged by the outlier and must NOT flag this day")
	assert.GreaterOrEqual(t, math.Abs(robust), 3.5, "the median/MAD estimator must flag this day despite the outlier")
}

func meanZ(x float64, sample []float64) float64 {
	m := meanF(sample)
	var ss float64
	for _, v := range sample {
		d := v - m
		ss += d * d
	}
	std := math.Sqrt(ss / float64(len(sample)))
	if std == 0 {
		return 0
	}
	return (x - m) / std
}

func TestMedianAndMAD(t *testing.T) {
	median, mad := medianAndMAD([]float64{1, 2, 3, 4, 5})
	assert.Equal(t, 3.0, median)
	assert.InDelta(t, 1.4826, mad, 0.001) // MAD of {1,2,3,4,5} around median 3 is 1, scaled by the constant
}

func TestDetectAnomalies_FlagsASpikeAndIgnoresStableServices(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_anom1")
	now := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	svc := &Service{Repos: repos, Clock: core.FixedClock{T: now}, AnomalyWindowDays: 14, AnomalyZThreshold: 3.5}

	seedServiceDailyCost(t, repos, tenant, "Amazon Elastic Compute Cloud - Compute", now.AddDate(0, 0, -14), 15, func(d int) float64 {
		if d == 14 {
			return 5000 // today's spike
		}
		return 100 + wobble(d)*0.1
	})
	seedServiceDailyCost(t, repos, tenant, "Amazon Simple Storage Service", now.AddDate(0, 0, -14), 15, func(d int) float64 {
		return 50 + wobble(d)*0.1 // stable, must not be flagged
	})

	lookback := core.Period{Start: now.AddDate(0, 0, -14), End: now.AddDate(0, 0, 1)}
	anomalies, err := svc.DetectAnomalies(testCtx(tenant), tenant, lookback)
	require.NoError(t, err)

	require.NotEmpty(t, anomalies)
	var flaggedCompute bool
	for _, a := range anomalies {
		if a.Dimension == "service" && a.Key == "Amazon Elastic Compute Cloud - Compute" {
			flaggedCompute = true
			assert.NotEmpty(t, a.Contributors, "a service-dimension anomaly should decompose into usage-type contributors")
		}
		assert.NotEqual(t, "Amazon Simple Storage Service", a.Key, "a stable service must never be flagged")
	}
	assert.True(t, flaggedCompute)

	// Persisted and listable.
	stored, err := svc.ListAnomalies(testCtx(tenant), tenant, now.AddDate(0, 0, -20), now.AddDate(0, 0, 1))
	require.NoError(t, err)
	assert.NotEmpty(t, stored)
}

func seedServiceDailyCost(t *testing.T, repos ports.Repositories, tenant core.TenantID, service string, start time.Time, days int, valueFn func(day int) float64) {
	t.Helper()
	records := make([]cost.Record, 0, days)
	for d := 0; d < days; d++ {
		day := start.AddDate(0, 0, d)
		records = append(records, cost.Record{
			ID: core.NewID("cst"), TenantID: tenant, AccountID: "111111111111",
			Period: core.NewPeriod(day, day.AddDate(0, 0, 1)), Granularity: cost.GranularityDaily,
			Service: service, ChargeType: cost.ChargeUsage, Basis: cost.BasisAmortized,
			Amount: core.USDollars(valueFn(d)), IngestedAt: time.Now().UTC(),
		})
	}
	_, err := repos.Costs.UpsertBatch(testCtx(tenant), tenant, records)
	require.NoError(t, err)
}
