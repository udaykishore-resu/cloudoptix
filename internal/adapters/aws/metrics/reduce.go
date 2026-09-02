// This file turns the raw, per-query values cloudwatch.go's fetchBatch
// collected into the ports.ResourceMetrics the rest of the platform reads,
// via core.SummarizeSamples — the one function every rule engine's
// percentile reasoning already goes through.
package metrics

import (
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// assemble groups raw's converted values back by (resource, field) and
// reduces each group into ports.ResourceMetrics.
//
// ErrorRate is the one field not reduced through SummarizeSamples over a
// per-bucket series: CloudWatch's per-bucket error count and per-bucket
// request count do not divide meaningfully bucket-by-bucket when either
// side can be zero for many buckets (a service with sparse traffic has
// mostly 0/0 buckets), so this instead sums both series over the whole
// window and reports one scalar ratio — expressed as a degenerate
// single-sample Percentiles (Min == P50 == ... == Max == that ratio) so it
// still fits the Percentiles shape every rule already reads, honestly
// representing "this is a windowed rate, not a distribution" rather than
// fabricating a false per-bucket spread.
func assemble(in ports.MetricCollectInput, raw map[string][]float64, targets map[string]queryTarget) []ports.ResourceMetrics {
	type accum struct {
		byField map[field][]float64
		custom  map[string][]float64
	}
	perResource := make(map[int]*accum)

	for id, values := range raw {
		t := targets[id]
		a, ok := perResource[t.resourceIdx]
		if !ok {
			a = &accum{byField: map[field][]float64{}, custom: map[string][]float64{}}
			perResource[t.resourceIdx] = a
		}
		if t.field == fieldCustom {
			a.custom[t.customKey] = append(a.custom[t.customKey], values...)
		} else {
			a.byField[t.field] = append(a.byField[t.field], values...)
		}
	}

	now := time.Now().UTC()
	expected := expectedBuckets(in.Window)

	var out []ports.ResourceMetrics
	for idx, a := range perResource {
		if idx >= len(in.Resources) {
			continue // defensive: a stale target referencing a resource index out of range
		}
		rm := ports.ResourceMetrics{
			ResourceID: in.Resources[idx].ID, TenantID: in.TenantID, Window: in.Window,
			Source: "cloudwatch", CollectedAt: now,
		}

		maxCoverage := 0.0
		assignPercentile := func(dst **core.Percentiles, values []float64) {
			if len(values) == 0 {
				return
			}
			cov := coverage(len(values), expected)
			p := core.SummarizeSamples(values, cov)
			*dst = &p
			if cov > maxCoverage {
				maxCoverage = cov
			}
		}

		assignPercentile(&rm.CPU, a.byField[fieldCPU])
		assignPercentile(&rm.Memory, a.byField[fieldMemory])
		assignPercentile(&rm.NetworkIn, a.byField[fieldNetworkIn])
		assignPercentile(&rm.NetworkOut, a.byField[fieldNetworkOut])
		assignPercentile(&rm.Throughput, a.byField[fieldThroughput])
		assignPercentile(&rm.Requests, a.byField[fieldRequests])
		assignPercentile(&rm.LatencyP99, a.byField[fieldLatencyP99])
		assignPercentile(&rm.Concurrency, a.byField[fieldConcurrency])
		assignPercentile(&rm.Connections, a.byField[fieldConnections])

		if errRate, cov, ok := errorRate(a.byField[fieldErrorNumerator], a.byField[fieldErrorDenominator], expected); ok {
			p := core.SummarizeSamples([]float64{errRate}, cov)
			rm.ErrorRate = &p
			if cov > maxCoverage {
				maxCoverage = cov
			}
		}

		if len(a.custom) > 0 {
			rm.Custom = make(map[string]core.Percentiles, len(a.custom))
			for key, values := range a.custom {
				if len(values) == 0 {
					continue
				}
				cov := coverage(len(values), expected)
				rm.Custom[key] = core.SummarizeSamples(values, cov)
				if cov > maxCoverage {
					maxCoverage = cov
				}
			}
		}

		// Coverage is reported honestly at the resource level as the best
		// coverage any single metric achieved, not an average across
		// metrics of very different natural cadences (a 5-minute CPU
		// series and a once-daily S3 size series would otherwise drag
		// each other's coverage number to a number that describes
		// neither).
		rm.Coverage = maxCoverage
		out = append(out, rm)
	}
	return out
}

// expectedBuckets estimates how many datapoints a full-coverage series
// would have over the window, for the coverage = actual/expected honesty
// check MetricCollectInput.StepSeconds's own doc comment implies rules
// need ("a resource with 12% coverage is not a resource with low
// utilisation" — ResourceMetrics.HasSignal's doc comment). The period used
// for this estimate is deliberately the same minPeriodForWindow the
// collector itself requested with, not any per-metric override (S3's daily
// period), so a metric published on a coarser cadence than the window's
// default reports a correspondingly and honestly lower coverage rather
// than a misleadingly inflated one.
func expectedBuckets(window core.Period) float64 {
	period := float64(minPeriodForWindow(window))
	duration := window.End.Sub(window.Start).Seconds()
	if period <= 0 || duration <= 0 {
		return 0
	}
	return duration / period
}

func coverage(actual int, expected float64) float64 {
	if expected <= 0 {
		return 0
	}
	c := float64(actual) / expected
	if c > 1 {
		c = 1
	}
	return c
}

// errorRate sums both series independently (never pairing them by index —
// CloudWatch does not guarantee the numerator and denominator series share
// the same timestamps or bucket count when either has missing data) and
// divides. ok is false when there is no denominator data at all — an
// undefined rate, not a zero one.
func errorRate(numerator, denominator []float64, expected float64) (rate, cov float64, ok bool) {
	if len(denominator) == 0 {
		return 0, 0, false
	}
	var numSum, denSum float64
	for _, v := range numerator {
		numSum += v
	}
	for _, v := range denominator {
		denSum += v
	}
	if denSum <= 0 {
		return 0, coverage(len(denominator), expected), true // no requests observed: a defined, zero error rate
	}
	return numSum / denSum, coverage(len(denominator), expected), true
}
