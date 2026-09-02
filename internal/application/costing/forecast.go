package costing

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/application/utilization"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const (
	minDaysForForecast     = 7  // below this, refuse rather than guess
	minDaysForTrend        = 14 // below this, a fitted trend is not trustworthy even with a high R²
	minDaysForWeeklySeason = 21 // three weeks, so the weekly lag has at least two full comparisons
	trendR2Threshold       = 0.5
	weeklySeasonACFThresh  = 0.5
)

// Forecast selects one of the four cost.ForecastMethod projections from the
// data's own shape — never from caller intent — and refuses outright
// (cost.ForecastInsufficient) when the lookback window cannot support any of
// them. A forecast built on four noisy days is a worse business input than
// an honest "not enough data yet": it gets put in front of a budget owner
// with the same confident formatting as a forecast built on ninety days of
// stable trend, and nothing about its presentation would tell them apart.
func (s *Service) Forecast(ctx context.Context, tenant core.TenantID, f ports.CostFilter, horizon core.Period) (cost.Forecast, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cost.Forecast{}, err
	}
	lookbackDays := s.ForecastLookbackDays
	if lookbackDays <= 0 {
		lookbackDays = 60
	}
	now := s.clock().Now()

	sf := f
	sf.Period = core.PeriodOfDays(now, lookbackDays)
	sf.Granularity = cost.GranularityDaily
	series, err := s.Repos.Costs.Series(ctx, tenant, sf)
	if err != nil {
		return cost.Forecast{}, err
	}
	series = series.Sorted()
	// Series() zero-fills every bucket across the requested lookback window,
	// including days before the tenant had any cost ingested at all — a
	// tenant connected 20 days ago still gets a 60-day lookback window full
	// of leading zeros. Trimming those off is what keeps the trend fit, the
	// seasonality test and the projections honest about how much real history
	// actually exists, rather than treating "no data yet" as "zero spend".
	values := trimLeadingZeros(series.Values())

	observedDays := countNonZero(values)
	if observedDays < minDaysForForecast {
		return cost.Forecast{
			Period: horizon, Method: cost.ForecastInsufficient, BasedOnDays: observedDays,
			Note: fmt.Sprintf("only %d day(s) of nonzero cost history are available; at least %d are required to forecast responsibly rather than guess", observedDays, minDaysForForecast),
		}, nil
	}

	// r2 is fit on the trimmed values (real history only), not on
	// series.Trend()'s view of the full zero-padded lookback window.
	_, r2 := trendSlopeR2(values)
	weeklyACF := 0.0
	if len(values) >= 2*7 {
		weeklyACF = utilization.Autocorrelation(values, 7)
	}

	horizonDays := horizon.Days()
	if horizonDays <= 0 {
		horizonDays = 1
	}

	var method cost.ForecastMethod
	var projected []float64
	switch {
	case isMonthToDateHorizon(horizon, now):
		method = cost.ForecastMonthToDate
		projected = monthToDateProjection(values, horizonDays)
	case len(values) >= minDaysForWeeklySeason && weeklyACF >= weeklySeasonACFThresh:
		method = cost.ForecastSeasonalNaive
		projected = seasonalNaiveProjection(values, horizonDays)
	case len(values) >= minDaysForTrend && r2 >= trendR2Threshold:
		method = cost.ForecastLinearTrend
		projected = linearTrendProjection(values, horizonDays)
	default:
		method = cost.ForecastRunRate
		projected = runRateProjection(values, horizonDays)
	}

	expected := 0.0
	for _, v := range projected {
		expected += v
	}
	resid := residualStdDev(values)
	// Uncertainty widens with the square root of the horizon length, the
	// standard growth rate for an accumulated random walk of daily noise —
	// a 30-day forecast is not three times as uncertain as a 10-day one, it
	// is √3 times as uncertain.
	band := resid * math.Sqrt(horizonDays)
	low := math.Max(0, expected-band)
	high := expected + band

	confidence := forecastConfidence(r2, weeklyACF, method, len(values))

	return cost.Forecast{
		Period: horizon, Expected: core.USDollars(expected), Low: core.USDollars(low), High: core.USDollars(high),
		Method: method, Confidence: core.Confidence(confidence).Clamp(), BasedOnDays: len(values),
		Note: forecastNote(method, r2, weeklyACF),
	}, nil
}

func trimLeadingZeros(values []float64) []float64 {
	for i, v := range values {
		if v != 0 {
			return values[i:]
		}
	}
	return nil
}

func countNonZero(values []float64) int {
	n := 0
	for _, v := range values {
		if v != 0 {
			n++
		}
	}
	return n
}

// isMonthToDateHorizon reports whether the requested horizon is exactly "the
// rest of the current calendar month", the shape CostService.Summary asks
// for. That case gets its own method because the right predictor is the
// recent run rate completing a known-length remainder, not a general-purpose
// trend or seasonal fit.
func isMonthToDateHorizon(horizon core.Period, now time.Time) bool {
	month := core.MonthOf(now)
	return horizon.End.Equal(month.End) && !horizon.Start.After(now) && !horizon.Start.Before(month.Start)
}

func meanF(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

func lastN(x []float64, n int) []float64 {
	if len(x) <= n {
		return x
	}
	return x[len(x)-n:]
}

func fitLine(values []float64) (slope, intercept float64) {
	n := float64(len(values))
	if n < 2 {
		if n == 1 {
			return 0, values[0]
		}
		return 0, 0
	}
	var sumX, sumY, sumXY, sumXX float64
	for i, y := range values {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return 0, sumY / n
	}
	slope = (n*sumXY - sumX*sumY) / denom
	intercept = (sumY - slope*sumX) / n
	return slope, intercept
}

// trendSlopeR2 fits a least-squares line and reports both the slope and the
// coefficient of determination, mirroring cost.Series.Trend but operating on
// an arbitrary values slice so it can be run on the leading-zero-trimmed
// history rather than the full zero-padded lookback window.
func trendSlopeR2(values []float64) (slope, r2 float64) {
	n := float64(len(values))
	if n < 3 {
		return 0, 0
	}
	slope, intercept := fitLine(values)
	meanY := meanF(values)
	var ssTot, ssRes float64
	for i, y := range values {
		pred := intercept + slope*float64(i)
		ssRes += (y - pred) * (y - pred)
		ssTot += (y - meanY) * (y - meanY)
	}
	if ssTot == 0 {
		return slope, 1
	}
	return slope, math.Max(0, 1-(ssRes/ssTot))
}

func residualStdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	slope, intercept := fitLine(values)
	var ss float64
	for i, y := range values {
		pred := intercept + slope*float64(i)
		d := y - pred
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(values)))
}

func monthToDateProjection(values []float64, horizonDays float64) []float64 {
	rate := meanF(lastN(values, 7))
	days := int(math.Ceil(horizonDays))
	out := make([]float64, days)
	for i := range out {
		out[i] = math.Max(0, rate)
	}
	return out
}

func seasonalNaiveProjection(values []float64, horizonDays float64) []float64 {
	days := int(math.Ceil(horizonDays))
	out := make([]float64, days)
	if len(values) < 7 {
		m := meanF(values)
		for i := range out {
			out[i] = m
		}
		return out
	}
	lastWeek := values[len(values)-7:]
	for i := 0; i < days; i++ {
		out[i] = math.Max(0, lastWeek[i%7])
	}
	return out
}

func linearTrendProjection(values []float64, horizonDays float64) []float64 {
	slope, intercept := fitLine(values)
	n := len(values)
	days := int(math.Ceil(horizonDays))
	out := make([]float64, days)
	for i := 0; i < days; i++ {
		v := intercept + slope*float64(n+i)
		out[i] = math.Max(0, v)
	}
	return out
}

func runRateProjection(values []float64, horizonDays float64) []float64 {
	m := meanF(values)
	days := int(math.Ceil(horizonDays))
	out := make([]float64, days)
	for i := range out {
		out[i] = math.Max(0, m)
	}
	return out
}

func forecastConfidence(r2, weeklyACF float64, method cost.ForecastMethod, days int) float64 {
	fit := 0.5
	switch method {
	case cost.ForecastLinearTrend:
		fit = r2
	case cost.ForecastSeasonalNaive:
		fit = weeklyACF
	case cost.ForecastMonthToDate:
		fit = 0.85
	}
	dataFactor := math.Min(1, float64(days)/30.0)
	v := 0.3 + 0.45*fit*dataFactor + 0.2*dataFactor
	return math.Min(0.95, math.Max(0.1, v))
}

func forecastNote(method cost.ForecastMethod, r2, weeklyACF float64) string {
	switch method {
	case cost.ForecastLinearTrend:
		return fmt.Sprintf("extrapolated from a fitted trend line (R²=%.2f)", r2)
	case cost.ForecastSeasonalNaive:
		return fmt.Sprintf("projected from the observed weekly pattern (lag-7 autocorrelation=%.2f)", weeklyACF)
	case cost.ForecastMonthToDate:
		return "projected from the trailing 7-day run rate to complete the current month"
	case cost.ForecastRunRate:
		return "no reliable trend or weekly pattern was found; extrapolated from the trailing average"
	default:
		return ""
	}
}
