package utilization

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func hourlyPoints(start time.Time, hours int, fn func(h int, t time.Time) float64) []ports.MetricPoint {
	out := make([]ports.MetricPoint, 0, hours)
	for h := 0; h < hours; h++ {
		t := start.Add(time.Duration(h) * time.Hour)
		out = append(out, ports.MetricPoint{At: t, Value: fn(h, t)})
	}
	return out
}

// pseudoNoise is a small deterministic wobble so a test series is not
// perfectly periodic (which would be an unrealistically easy case) while
// remaining fully reproducible across runs.
func pseudoNoise(h int) float64 {
	return math.Mod(float64(h)*37, 11) / 11 * 2 // 0..2
}

func TestSummarize_DailyCycleIsDetectedAsSeasonal(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hours := 24 * 14 // two weeks, hourly
	points := hourlyPoints(start, hours, func(h int, _ time.Time) float64 {
		hourOfDay := h % 24
		return 40 + 35*math.Sin(2*math.Pi*float64(hourOfDay)/24) + pseudoNoise(h)
	})
	window := core.NewPeriod(start, start.Add(time.Duration(hours)*time.Hour))

	summary := Summarize(points, window)

	require.True(t, summary.Seasonal, "a clean daily sine cycle over two weeks must be detected as seasonal")
	assert.NotEmpty(t, summary.PeakHours, "a seasonal series must report its peak hours")
	assert.InDelta(t, 1.0, summary.Coverage, 0.01)
}

func TestSummarize_FlatSeriesIsNotSeasonal(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hours := 24 * 14
	points := hourlyPoints(start, hours, func(h int, _ time.Time) float64 {
		// Tiny wobble around a flat baseline: real noise, no cycle.
		return 20 + pseudoNoise(h)*0.05
	})
	window := core.NewPeriod(start, start.Add(time.Duration(hours)*time.Hour))

	summary := Summarize(points, window)

	assert.False(t, summary.Seasonal, "a flat series must never be reported as seasonal")
}

func TestSummarize_SparseSeriesReportsLowCoverage(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	window := core.NewPeriod(start, start.Add(720*time.Hour)) // 30-day month
	points := hourlyPoints(start, 12, func(h int, _ time.Time) float64 { return 10 })

	summary := Summarize(points, window)

	assert.InDelta(t, 12.0/720.0, summary.Coverage, 0.001)
	assert.False(t, summary.Seasonal, "12 hours of data cannot establish a daily cycle")
}

func TestTrendPerDay_RisingSeries(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var points []ports.MetricPoint
	for day := 0; day < 10; day++ {
		for h := 0; h < 24; h++ {
			points = append(points, ports.MetricPoint{
				At:    start.AddDate(0, 0, day).Add(time.Duration(h) * time.Hour),
				Value: float64(day) * 5, // +5 units per day, flat within a day
			})
		}
	}
	trend := trendPerDay(points)
	assert.InDelta(t, 5.0, trend, 0.01)
}

func TestTrendPerDay_TooFewDaysIsZero(t *testing.T) {
	points := []ports.MetricPoint{
		{At: time.Now(), Value: 1},
		{At: time.Now().Add(time.Hour), Value: 2},
	}
	assert.Equal(t, 0.0, trendPerDay(points))
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		p    core.Percentiles
		want string
	}{
		{"no samples", core.Percentiles{}, "unknown"},
		{"saturated", core.Percentiles{Samples: 100, P95: 92, P50: 80}, "saturated"},
		{"idle", core.Percentiles{Samples: 100, P95: 10, P99: 15, P50: 5, Stability: 0.95}, "idle"},
		{"cyclical", core.Percentiles{Samples: 100, P95: 60, P99: 70, P50: 40, Stability: 0.6, Seasonal: true}, "cyclical"},
		{"spiky", core.Percentiles{Samples: 100, P95: 50, P99: 90, P50: 10, Stability: 0.6}, "spiky"},
		{"steady", core.Percentiles{Samples: 100, P95: 55, P99: 60, P50: 50, Stability: 0.8}, "steady"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, Classify(c.p))
		})
	}
}

func TestProfileBusinessHours_SchedulingCandidate(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) // a Monday
	var points []ports.MetricPoint
	for day := 0; day < 10; day++ {
		d := start.AddDate(0, 0, day)
		for h := 0; h < 24; h++ {
			t := d.Add(time.Duration(h) * time.Hour)
			v := 5.0
			if DefaultBusinessHours.contains(t) {
				v = 70.0
			}
			points = append(points, ports.MetricPoint{At: t, Value: v})
		}
	}
	profile := ProfileBusinessHours(points, DefaultBusinessHours)
	assert.True(t, profile.SchedulingCandidate)
	assert.Less(t, profile.OffToBusinessRatio, 0.25)
	assert.NotEmpty(t, profile.Rationale)
}

func TestProfileBusinessHours_AlwaysOnIsNotACandidate(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	points := hourlyPoints(start, 24*10, func(h int, _ time.Time) float64 { return 60 + pseudoNoise(h) })
	profile := ProfileBusinessHours(points, DefaultBusinessHours)
	assert.False(t, profile.SchedulingCandidate)
}

func TestDetectBursts(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var points []ports.MetricPoint
	for i := 0; i < 100; i++ {
		v := 10.0
		if i == 50 || i == 51 {
			v = 500.0 // an isolated two-point spike
		}
		points = append(points, ports.MetricPoint{At: start.Add(time.Duration(i) * time.Minute), Value: v})
	}
	bursts := DetectBursts(points, 3)
	require.Len(t, bursts, 1)
	assert.Equal(t, 500.0, bursts[0].PeakValue)
	assert.True(t, bursts[0].MagnitudeStdDevs > 3)
}

func TestDetectBursts_NoBurstsInUniformSeries(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := hourlyPoints(start, 100, func(h int, _ time.Time) float64 { return 42 })
	assert.Empty(t, DetectBursts(points, 3))
}
