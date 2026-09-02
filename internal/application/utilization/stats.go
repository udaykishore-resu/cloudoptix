package utilization

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const (
	dailyLagHours        = 24
	weeklyLagHours       = 24 * 7
	minHoursForDailyACF  = 2 * dailyLagHours
	minHoursForWeeklyACF = 2 * weeklyLagHours
	// acfThreshold is the autocorrelation coefficient above which a cycle is
	// treated as real rather than coincidental noise. 0.5 is conservative on
	// purpose: Seasonal gates a scheduling recommendation that stops a
	// resource outside its detected peak hours, and a false positive there is
	// an availability incident, not a cosmetic mislabel.
	acfThreshold = 0.5
)

// Summarize turns a raw, timestamped metric series into the full
// core.Percentiles summary: distribution statistics from core.SummarizeSamples
// plus the time-aware fields — Trend, Seasonal, PeakHours — that only this
// package can compute because they require the timestamps and the window the
// series was collected over.
func Summarize(points []ports.MetricPoint, window core.Period) core.Percentiles {
	values := make([]float64, 0, len(points))
	for _, p := range points {
		values = append(values, p.Value)
	}
	summary := core.SummarizeSamples(values, coverageOf(points, window))
	summary.Trend = trendPerDay(points)
	summary.Seasonal, summary.PeakHours = detectSeasonality(points, window)
	return summary
}

// coverageOf reports the fraction of the window's hours that have at least
// one observation. A resource with 8 hours of data in a 720-hour month is not
// a resource that ran idle for 712 hours — it is a resource CloudOptix barely
// saw, and every rule that reads Coverage discounts its confidence
// accordingly.
func coverageOf(points []ports.MetricPoint, window core.Period) float64 {
	if window.IsZero() || window.Hours() <= 0 {
		if len(points) == 0 {
			return 0
		}
		return 1
	}
	totalHours := int(math.Ceil(window.Hours()))
	if totalHours <= 0 {
		return 0
	}
	seen := make(map[int]bool, totalHours)
	for _, p := range points {
		if p.At.Before(window.Start) || !p.At.Before(window.End) {
			continue
		}
		seen[int(p.At.Sub(window.Start).Hours())] = true
	}
	return float64(len(seen)) / float64(totalHours)
}

// trendPerDay fits a least-squares line to the series' daily averages and
// returns the slope per day. It mirrors cost.Series.Trend deliberately: a
// utilization trend and a cost trend are computed the same way so the copilot
// never has to explain why two "trend" numbers disagree in method.
func trendPerDay(points []ports.MetricPoint) float64 {
	daily := dailyAverages(points)
	if len(daily) < 3 {
		return 0
	}
	slope, _ := leastSquares(daily)
	return slope
}

func dailyAverages(points []ports.MetricPoint) []float64 {
	sums := map[string]float64{}
	counts := map[string]int{}
	for _, p := range points {
		d := p.At.UTC().Format("2006-01-02")
		sums[d] += p.Value
		counts[d]++
	}
	days := make([]string, 0, len(sums))
	for d := range sums {
		days = append(days, d)
	}
	sort.Strings(days)
	out := make([]float64, len(days))
	for i, d := range days {
		out[i] = sums[d] / float64(counts[d])
	}
	return out
}

func leastSquares(vals []float64) (slope, r2 float64) {
	n := float64(len(vals))
	if n < 2 {
		return 0, 0
	}
	var sumX, sumY, sumXY, sumXX float64
	for i, y := range vals {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return 0, 0
	}
	slope = (n*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / n
	meanY := sumY / n
	var ssTot, ssRes float64
	for i, y := range vals {
		pred := intercept + slope*float64(i)
		ssRes += (y - pred) * (y - pred)
		ssTot += (y - meanY) * (y - meanY)
	}
	if ssTot == 0 {
		return slope, 1
	}
	return slope, math.Max(0, 1-(ssRes/ssTot))
}

// detectSeasonality resamples the series onto an hourly grid across the
// window, then tests autocorrelation at the daily and weekly lags. Missing
// hours are filled with the mean of the hours that do have data (mean
// imputation) purely so the autocorrelation formula has a regularly-spaced
// series to operate on; the flat-series guard below is evaluated on the real
// observations only, so a sparse series padded with its own mean can never
// manufacture a false seasonal signal.
func detectSeasonality(points []ports.MetricPoint, window core.Period) (bool, []int) {
	if window.IsZero() || len(points) == 0 {
		return false, nil
	}
	series, filled := hourlySeries(points, window)
	n := len(series)
	if n < minHoursForDailyACF || !hasVariance(series, filled) {
		return false, nil
	}
	dailyACF := autocorrelation(series, dailyLagHours)
	weeklyACF := 0.0
	if n >= minHoursForWeeklyACF {
		weeklyACF = autocorrelation(series, weeklyLagHours)
	}
	if dailyACF < acfThreshold && weeklyACF < acfThreshold {
		return false, nil
	}
	return true, peakHoursOf(points)
}

func hourlySeries(points []ports.MetricPoint, window core.Period) (series []float64, filled []bool) {
	totalHours := int(math.Ceil(window.Hours()))
	if totalHours <= 0 {
		return nil, nil
	}
	sums := make([]float64, totalHours)
	counts := make([]int, totalHours)
	for _, p := range points {
		if p.At.Before(window.Start) || !p.At.Before(window.End) {
			continue
		}
		h := int(p.At.Sub(window.Start).Hours())
		if h < 0 || h >= totalHours {
			continue
		}
		sums[h] += p.Value
		counts[h]++
	}
	series = make([]float64, totalHours)
	filled = make([]bool, totalHours)
	var sum float64
	var n int
	for h := 0; h < totalHours; h++ {
		if counts[h] > 0 {
			series[h] = sums[h] / float64(counts[h])
			filled[h] = true
			sum += series[h]
			n++
		}
	}
	mean := 0.0
	if n > 0 {
		mean = sum / float64(n)
	}
	for h := 0; h < totalHours; h++ {
		if !filled[h] {
			series[h] = mean
		}
	}
	return series, filled
}

// hasVariance reports whether the real (non-imputed) observations vary
// enough to even be capable of a cycle. A perfectly flat series — an idle
// resource sitting at 1% CPU all month — must never be reported as seasonal
// just because autocorrelation of a constant is technically undefined-safe
// (it would compute as 0/0 territory, but this guard stops it long before
// that): a 5% coefficient-of-variation floor separates a real cycle from
// measurement noise around a flat baseline.
func hasVariance(series []float64, filled []bool) bool {
	var vals []float64
	for i, f := range filled {
		if f {
			vals = append(vals, series[i])
		}
	}
	if len(vals) < 4 {
		return false
	}
	mean := meanOf(vals)
	var ss float64
	for _, v := range vals {
		d := v - mean
		ss += d * d
	}
	std := math.Sqrt(ss / float64(len(vals)))
	if mean == 0 {
		return std > 1e-9
	}
	return std/math.Abs(mean) > 0.05
}

// autocorrelation is the standard lag-k sample autocorrelation: the
// covariance between the series and itself shifted by lag, normalized by the
// series' own variance. It is Pearson correlation restricted to a fixed time
// offset, which is exactly "how well does the value k hours ago predict the
// value now" — the question seasonality detection needs answered.
func autocorrelation(x []float64, lag int) float64 {
	n := len(x) - lag
	if n <= 1 || lag <= 0 || lag >= len(x) {
		return 0
	}
	mean := meanOf(x)
	var den float64
	for _, v := range x {
		d := v - mean
		den += d * d
	}
	if den == 0 {
		return 0
	}
	var num float64
	for i := 0; i < n; i++ {
		num += (x[i] - mean) * (x[i+lag] - mean)
	}
	return num / den
}

// Autocorrelation exposes the lag-k sample autocorrelation used internally
// for seasonality detection, for reuse by other engines that need the same
// "does k-ago predict now" test on their own series — the cost forecaster
// tests a daily cost series for a weekly cycle with exactly this function.
func Autocorrelation(x []float64, lag int) float64 { return autocorrelation(x, lag) }

func meanOf(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

// peakHoursOf averages every observation by its UTC hour-of-day and returns
// the hours within 85% of the busiest hour's average, which is what the
// scheduling rule reads to decide the window a cyclical resource must stay
// running in.
func peakHoursOf(points []ports.MetricPoint) []int {
	sums := make([]float64, 24)
	counts := make([]int, 24)
	for _, p := range points {
		h := p.At.UTC().Hour()
		sums[h] += p.Value
		counts[h]++
	}
	max := 0.0
	any := false
	avg := make([]float64, 24)
	for h := 0; h < 24; h++ {
		if counts[h] > 0 {
			avg[h] = sums[h] / float64(counts[h])
			any = true
			if avg[h] > max {
				max = avg[h]
			}
		}
	}
	if !any || max <= 0 {
		return nil
	}
	var peaks []int
	for h := 0; h < 24; h++ {
		if counts[h] > 0 && avg[h] >= 0.85*max {
			peaks = append(peaks, h)
		}
	}
	return peaks
}

// Classify labels a resource's utilization shape from its percentile summary.
// The five labels are the vocabulary every downstream consumer — rules, the
// twin's performance view, the copilot — uses to talk about a resource's
// behaviour, so this is the one place that vocabulary is produced.
func Classify(p core.Percentiles) string {
	switch {
	case p.Samples == 0:
		return "unknown"
	case p.P95 >= 85:
		return "saturated"
	case p.Stability > 0.85 && p.P99 < 20:
		return "idle"
	case p.Seasonal:
		return "cyclical"
	case p.P99 > 3*math.Max(p.P50, 1) || p.Stability < 0.4:
		return "spiky"
	default:
		return "steady"
	}
}

// BusinessHoursWindow declares the tenant's working hours in UTC, used to
// split a series into business-hours and off-hours observations.
type BusinessHoursWindow struct {
	StartHourUTC int
	EndHourUTC   int // exclusive; may be < StartHourUTC to express a window that wraps midnight
	WeekdaysOnly bool
}

// DefaultBusinessHours is 09:00-17:00 US-Eastern expressed in UTC (UTC-5,
// ignoring DST) weekdays only — a reasonable default until a tenant's spec
// states its own.
var DefaultBusinessHours = BusinessHoursWindow{StartHourUTC: 14, EndHourUTC: 22, WeekdaysOnly: true}

func (w BusinessHoursWindow) contains(t time.Time) bool {
	u := t.UTC()
	if w.WeekdaysOnly && (u.Weekday() == time.Saturday || u.Weekday() == time.Sunday) {
		return false
	}
	h := u.Hour()
	if w.StartHourUTC <= w.EndHourUTC {
		return h >= w.StartHourUTC && h < w.EndHourUTC
	}
	return h >= w.StartHourUTC || h < w.EndHourUTC
}

// HoursProfile compares a resource's business-hours and off-hours load, which
// is the evidence a "schedule this off outside business hours" recommendation
// is built on.
type HoursProfile struct {
	BusinessMean        float64
	BusinessP95         float64
	OffHoursMean        float64
	OffHoursP95         float64
	OffToBusinessRatio  float64
	SchedulingCandidate bool
	Rationale           string
}

// ProfileBusinessHours splits the series by the given window and summarizes
// each half. SchedulingCandidate requires both real business-hours load (so
// stopping the resource would matter) and near-idle off-hours load (so
// stopping it is safe) — a resource whose usage merely varies without that
// shape is not a scheduling candidate, it is just noisy.
func ProfileBusinessHours(points []ports.MetricPoint, w BusinessHoursWindow) HoursProfile {
	var biz, off []float64
	for _, p := range points {
		if w.contains(p.At) {
			biz = append(biz, p.Value)
		} else {
			off = append(off, p.Value)
		}
	}
	bizSummary := core.SummarizeSamples(biz, 1)
	offSummary := core.SummarizeSamples(off, 1)
	hp := HoursProfile{
		BusinessMean: bizSummary.Mean, BusinessP95: bizSummary.P95,
		OffHoursMean: offSummary.Mean, OffHoursP95: offSummary.P95,
	}
	if hp.BusinessMean > 0 {
		hp.OffToBusinessRatio = hp.OffHoursMean / hp.BusinessMean
	}
	hp.SchedulingCandidate = len(biz) >= 8 && len(off) >= 8 && hp.BusinessMean > 5 && hp.OffToBusinessRatio < 0.25
	if hp.SchedulingCandidate {
		hp.Rationale = fmt.Sprintf("off-hours load is %.0f%% of business-hours load across %d business-hour and %d off-hour samples",
			hp.OffToBusinessRatio*100, len(biz), len(off))
	}
	return hp
}

// Burst is one isolated excursion above a series' own baseline.
type Burst struct {
	Start            time.Time
	End              time.Time
	PeakValue        float64
	BaselineMean     float64
	MagnitudeStdDevs float64
}

// DetectBursts flags contiguous runs of observations that exceed the
// series' own mean by thresholdStdDevs standard deviations (default 3),
// which is what separates "this resource occasionally spikes" from "this
// resource is simply busy" — the latter shows up as elevated percentiles, the
// former as isolated episodes a rightsizing rule must not smooth away.
func DetectBursts(points []ports.MetricPoint, thresholdStdDevs float64) []Burst {
	if thresholdStdDevs <= 0 {
		thresholdStdDevs = 3
	}
	if len(points) < 5 {
		return nil
	}
	sorted := make([]ports.MetricPoint, len(points))
	copy(sorted, points)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })

	values := make([]float64, len(sorted))
	for i, p := range sorted {
		values[i] = p.Value
	}
	mean := meanOf(values)
	var ss float64
	for _, v := range values {
		d := v - mean
		ss += d * d
	}
	std := math.Sqrt(ss / float64(len(values)))
	if std == 0 {
		return nil
	}
	threshold := mean + thresholdStdDevs*std

	var bursts []Burst
	var cur *Burst
	flush := func() {
		if cur == nil {
			return
		}
		cur.MagnitudeStdDevs = (cur.PeakValue - mean) / std
		bursts = append(bursts, *cur)
		cur = nil
	}
	for _, p := range sorted {
		if p.Value > threshold {
			if cur == nil {
				cur = &Burst{Start: p.At, End: p.At, PeakValue: p.Value, BaselineMean: mean}
			} else {
				cur.End = p.At
				if p.Value > cur.PeakValue {
					cur.PeakValue = p.Value
				}
			}
		} else {
			flush()
		}
	}
	flush()
	return bursts
}
