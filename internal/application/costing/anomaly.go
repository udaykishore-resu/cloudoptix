package costing

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// madConsistencyConstant rescales the median absolute deviation so it
// estimates the standard deviation of a normal distribution (1/Φ⁻¹(0.75)) —
// the standard correction every robust z-score implementation applies, so a
// threshold like 3.5 means the same thing here as it would for a mean-based
// z-score on normally distributed data.
const madConsistencyConstant = 1.4826

// DetectAnomalies scores every key in the service, account and application
// dimensions against a robust z-score of its own trailing history, using the
// median and median absolute deviation rather than the mean and standard
// deviation. The reason is structural, not stylistic: a real cost anomaly —
// a runaway job, a forgotten commitment purchase — sits inside the very
// window used to compute the baseline. The mean and stddev are dragged
// toward that outlier by construction (a single 10x day pulls a 14-day mean
// up by ~65%), so by the second or third day of a real spike a mean-based
// z-score has already absorbed it into "normal" and stops firing. The
// median ignores everything past the middle-ranked observation and the MAD
// is derived the same way, so a single outlying day — or even several —
// cannot drag the baseline toward itself.
func (s *Service) DetectAnomalies(ctx context.Context, tenant core.TenantID, lookback core.Period) ([]cost.Anomaly, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	windowDays := s.AnomalyWindowDays
	if windowDays <= 0 {
		windowDays = 14
	}
	threshold := s.AnomalyZThreshold
	if threshold <= 0 {
		threshold = 3.5
	}
	now := s.clock().Now()

	var anomalies []cost.Anomaly
	for _, dim := range []string{"service", "account", "application"} {
		found, err := s.detectAnomaliesForDimension(ctx, tenant, dim, lookback, windowDays, threshold, now)
		if err != nil {
			return nil, err
		}
		anomalies = append(anomalies, found...)
	}
	if len(anomalies) == 0 {
		return anomalies, nil
	}
	if err := s.Repos.Costs.SaveAnomalies(ctx, tenant, anomalies); err != nil {
		return nil, err
	}
	if s.Events != nil {
		for _, a := range anomalies {
			_ = s.Events.Publish(ctx, ports.Event{
				ID: string(core.NewID("evt")), Type: ports.EventCostAnomalyDetected, TenantID: tenant,
				SubjectID: a.ID, OccurredAt: now,
				Payload: map[string]any{"dimension": a.Dimension, "key": a.Key, "delta_pct": a.DeltaPct, "score": a.Score},
			})
		}
	}
	return anomalies, nil
}

func (s *Service) detectAnomaliesForDimension(ctx context.Context, tenant core.TenantID, dim string, lookback core.Period, windowDays int, threshold float64, now time.Time) ([]cost.Anomaly, error) {
	needed := windowDays + 2
	if int(math.Ceil(lookback.Days())) < needed {
		lookback = core.Period{Start: lookback.End.AddDate(0, 0, -needed), End: lookback.End}
	}
	breakdown, err := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: lookback}, dim)
	if err != nil {
		return nil, err
	}

	var out []cost.Anomaly
	for _, item := range breakdown.Items {
		if item.Key == "" || item.Key == "__unknown__" || item.Key == "__unattributed__" || item.Key == "__other__" {
			continue
		}
		f := ports.CostFilter{Period: lookback, Granularity: cost.GranularityDaily}
		switch dim {
		case "service":
			f.Services = []string{item.Key}
		case "account":
			f.AccountIDs = []core.AccountID{core.AccountID(item.Key)}
		case "application":
			f.ApplicationID = core.ID(item.Key)
		}
		series, err := s.Repos.Costs.Series(ctx, tenant, f)
		if err != nil {
			continue
		}
		series = series.Sorted()
		values := series.Values()
		if len(values) < windowDays+1 {
			continue
		}

		testIdx := len(values) - 1
		train := values[:testIdx]
		if len(train) > windowDays {
			train = train[len(train)-windowDays:]
		}
		actual := values[testIdx]
		median, mad := medianAndMAD(train)
		z := robustZ(actual, median, mad)
		if math.Abs(z) < threshold {
			continue
		}

		delta := actual - median
		deltaPct := 0.0
		if median != 0 {
			deltaPct = delta / median * 100
		}
		point := series.Points[testIdx]
		a := cost.Anomaly{
			ID: core.NewID("anm"), TenantID: tenant, DetectedAt: now, Period: point.Period,
			Dimension: dim, Key: item.Key,
			Expected: core.USDollars(median), Actual: core.USDollars(actual),
			Delta: core.USDollars(delta), DeltaPct: deltaPct, Score: z,
			Severity: severityForZ(math.Abs(z)),
		}
		a.Explanation = fmt.Sprintf(
			"%s %q moved to %s against a %d-day trailing median of %s — a robust z-score of %.1f",
			dim, item.Key, a.Actual.Format(), windowDays, a.Expected.Format(), z)
		a.Contributors = s.contributorsFor(ctx, tenant, dim, item.Key, point.Period)
		out = append(out, a)
	}
	return out, nil
}

// medianAndMAD returns the median and the consistency-scaled median absolute
// deviation of a sample.
func medianAndMAD(values []float64) (median, mad float64) {
	if len(values) == 0 {
		return 0, 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	median = medianOf(sorted)

	devs := make([]float64, len(sorted))
	for i, v := range sorted {
		devs[i] = math.Abs(v - median)
	}
	sort.Float64s(devs)
	mad = medianOf(devs) * madConsistencyConstant
	return median, mad
}

func medianOf(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// robustZ is (actual - median) / MAD. When every training day was identical
// the MAD is zero; rather than divide by zero or silently report no anomaly,
// a small floor proportional to the median keeps the score finite while
// still firing on a genuine departure from a flat baseline.
func robustZ(actual, median, mad float64) float64 {
	if mad == 0 {
		if actual == median {
			return 0
		}
		mad = math.Max(1, math.Abs(median)*0.01)
	}
	return (actual - median) / mad
}

func severityForZ(absZ float64) core.Severity {
	switch {
	case absZ >= 8:
		return core.SeverityCritical
	case absZ >= 5:
		return core.SeverityHigh
	case absZ >= 4:
		return core.SeverityMedium
	default:
		return core.SeverityLow
	}
}

// contributorsFor decomposes a service-dimension anomaly into the usage
// types that actually moved, so "cost increased" becomes "NAT gateway data
// processing increased". It compares the anomalous day's usage-type
// breakdown against each type's average daily spend over the preceding two
// weeks.
func (s *Service) contributorsFor(ctx context.Context, tenant core.TenantID, dim, key string, day core.Period) []cost.Contribution {
	if dim != "service" {
		return nil
	}
	baselineWindow := core.Period{Start: day.Start.AddDate(0, 0, -14), End: day.Start}
	todayBD, err1 := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: day, Services: []string{key}}, "usage_type")
	baseBD, err2 := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: baselineWindow, Services: []string{key}}, "usage_type")
	if err1 != nil || err2 != nil {
		return nil
	}
	baseDays := math.Max(1, baselineWindow.Days())
	baseline := map[string]float64{}
	for _, it := range baseBD.Items {
		baseline[it.Key] = it.Amount.Units() / baseDays
	}

	seen := map[string]bool{}
	var contribs []cost.Contribution
	var magnitude float64
	for _, it := range todayBD.Items {
		seen[it.Key] = true
		delta := it.Amount.Units() - baseline[it.Key]
		contribs = append(contribs, cost.Contribution{Dimension: "usage_type", Key: it.Key, Delta: core.USDollars(delta)})
		magnitude += math.Abs(delta)
	}
	for k, avg := range baseline {
		if seen[k] {
			continue
		}
		contribs = append(contribs, cost.Contribution{Dimension: "usage_type", Key: k, Delta: core.USDollars(-avg)})
		magnitude += avg
	}
	sort.Slice(contribs, func(i, j int) bool { return contribs[i].Delta.Abs().Micros() > contribs[j].Delta.Abs().Micros() })
	if magnitude > 0 {
		for i := range contribs {
			contribs[i].Share = contribs[i].Delta.Abs().Units() / magnitude
		}
	}
	if len(contribs) > 5 {
		contribs = contribs[:5]
	}
	return contribs
}
