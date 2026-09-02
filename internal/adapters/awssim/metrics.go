// This file implements ports.MetricCollector against the Estate.
//
// The design decision: only six resource kinds in this package carry a
// UtilizationProfile (EC2Instance, RDSInstance, DynamoDBTable,
// LambdaFunction, ECSService, ElastiCacheCluster) — that field exists
// specifically to drive this collector, so Collect answers "what would
// CloudWatch have recorded" only for those, and silently returns nothing
// for every other kind rather than inventing a metric for a VPC or a KMS
// key that AWS itself has no metric for either.
//
// Generation is a two-step pipeline: profileSeries turns one
// UtilizationProfile into a raw time series shaped the way that profile
// promises (idle stays under 20%, spiky's tail is 4x-plus its median,
// cyclical follows a business-hours sine wave, saturated hugs the
// ceiling), then core.SummarizeSamples — the same function every rule
// engine reads its percentiles through — turns that series into the
// Percentiles the rest of the platform consumes. That second step is
// deliberate: two rules must never disagree about what "P95" means, and
// routing simulated data through the one real summarizer is what
// guarantees this simulator produces exactly the shape a real collector's
// output would have.
//
// A resource's series is seeded from its native ID and the requested
// window (seededSourceForResource), the same pattern CostIngestor uses for
// its own determinism: the same resource over the same window always
// produces the same numbers, and a different window produces different —
// but still reproducible — numbers.
package awssim

import (
	"context"
	"hash/fnv"
	"math"
	"math/rand"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// MetricCollector implements ports.MetricCollector over an Estate.
type MetricCollector struct{}

var _ ports.MetricCollector = (*MetricCollector)(nil)

// NewMetricCollector builds the simulated metric collector.
func NewMetricCollector() *MetricCollector { return &MetricCollector{} }

// Source names this collector as the port's "simulator" source.
func (m *MetricCollector) Source() string { return "simulator" }

// Available is always true for a simulated account.
func (m *MetricCollector) Available(ctx context.Context, session ports.AWSSession) bool { return true }

// Collect generates a ResourceMetrics summary for every requested resource
// whose kind carries a declared UtilizationProfile. Resources of any other
// kind are silently skipped, matching how CloudWatch itself has no
// utilisation metric for, say, a security group.
func (m *MetricCollector) Collect(ctx context.Context, in ports.MetricCollectInput) ([]ports.ResourceMetrics, error) {
	estate, err := FromSession(in.Session, in.Region)
	if err != nil {
		return nil, err
	}
	estate.mu.RLock()
	defer estate.mu.RUnlock()

	step := in.StepSeconds
	if step <= 0 {
		step = 300 // 5-minute resolution, CloudWatch's finest standard granularity
	}
	now := time.Now().UTC()

	var out []ports.ResourceMetrics
	for _, r := range in.Resources {
		rm, ok := m.metricsFor(estate, r, in.Window, step)
		if !ok {
			continue
		}
		rm.TenantID = in.TenantID
		rm.Window = in.Window
		rm.Source = "simulator"
		rm.CollectedAt = now
		out = append(out, rm)
	}
	return out, nil
}

// metricsFor dispatches by kind. Each case reads the one Estate resource
// backing r.NativeID and turns its Profile into the metric fields real
// CloudWatch data for that AWS service would actually populate.
func (m *MetricCollector) metricsFor(e *Estate, r cloud.Resource, window core.Period, step int) (ports.ResourceMetrics, bool) {
	switch r.Kind {
	case cloud.KindEC2Instance:
		inst, ok := e.EC2Instances[r.NativeID]
		if !ok {
			return ports.ResourceMetrics{}, false
		}
		baseline := inst.CPUBaselineP50
		if baseline <= 0 {
			baseline = defaultBaseline(inst.Profile)
		}
		rng := rngFor(r.NativeID, window)
		cpu, seasonal, peaks := profileSeries(inst.Profile, baseline, window, step, rng)
		cpuP := summarize(cpu, seasonal, peaks)
		netIn := summarize(scaleSeries(cpu, 0, 50_000_000), seasonal, peaks)  // bytes/s, up to 50 MB/s at 100% CPU
		netOut := summarize(scaleSeries(cpu, 0, 30_000_000), seasonal, peaks) // bytes/s
		return ports.ResourceMetrics{
			ResourceID: r.ID, CPU: &cpuP, NetworkIn: &netIn, NetworkOut: &netOut, Coverage: 1.0,
		}, true

	case cloud.KindRDSInstance:
		inst, ok := e.RDSInstances[r.NativeID]
		if !ok {
			return ports.ResourceMetrics{}, false
		}
		rng := rngFor(r.NativeID, window)
		cpu, seasonal, peaks := profileSeries(inst.Profile, defaultBaseline(inst.Profile), window, step, rng)
		cpuP := summarize(cpu, seasonal, peaks)
		conns := summarize(scaleSeries(cpu, 1, 400), seasonal, peaks)
		return ports.ResourceMetrics{ResourceID: r.ID, CPU: &cpuP, Connections: &conns, Coverage: 1.0}, true

	case cloud.KindDynamoDBTable:
		t, ok := e.DynamoDBTables[r.NativeID]
		if !ok {
			return ports.ResourceMetrics{}, false
		}
		rng := rngFor(r.NativeID, window)
		series, seasonal, peaks := profileSeries(t.Profile, defaultBaseline(t.Profile), window, step, rng)
		reqs := summarize(scaleSeries(series, 0, 2000), seasonal, peaks) // consumed capacity units/s, illustrative ceiling
		return ports.ResourceMetrics{ResourceID: r.ID, Requests: &reqs, Coverage: 1.0}, true

	case cloud.KindLambdaFunction:
		f, ok := e.LambdaFunctions[r.NativeID]
		if !ok {
			return ports.ResourceMetrics{}, false
		}
		rng := rngFor(r.NativeID, window)
		series, seasonal, peaks := profileSeries(f.Profile, defaultBaseline(f.Profile), window, step, rng)
		conc := summarize(scaleSeries(series, 0, 200), seasonal, peaks)
		reqs := summarize(scaleSeries(series, 0, 500), seasonal, peaks)
		latency := summarize(scaleSeries(series, f.AvgDurationMS*0.6, f.AvgDurationMS*2.5), seasonal, peaks)
		errRate := summarize(scaleSeries(invert(series), 0, 0.02), seasonal, peaks) // errors rise when traffic is low (cold starts)
		return ports.ResourceMetrics{
			ResourceID: r.ID, Concurrency: &conc, Requests: &reqs, LatencyP99: &latency, ErrorRate: &errRate, Coverage: 1.0,
		}, true

	case cloud.KindECSService:
		s, ok := e.ECSServices[r.NativeID]
		if !ok {
			return ports.ResourceMetrics{}, false
		}
		rng := rngFor(r.NativeID, window)
		cpu, seasonal, peaks := profileSeries(s.Profile, defaultBaseline(s.Profile), window, step, rng)
		cpuP := summarize(cpu, seasonal, peaks)
		mem := summarize(scaleSeries(cpu, 20, 90), seasonal, peaks)
		return ports.ResourceMetrics{ResourceID: r.ID, CPU: &cpuP, Memory: &mem, Coverage: 1.0}, true

	case cloud.KindElastiCache:
		c, ok := e.ElastiCacheClusters[r.NativeID]
		if !ok {
			return ports.ResourceMetrics{}, false
		}
		rng := rngFor(r.NativeID, window)
		cpu, seasonal, peaks := profileSeries(c.Profile, defaultBaseline(c.Profile), window, step, rng)
		cpuP := summarize(cpu, seasonal, peaks)
		conns := summarize(scaleSeries(cpu, 1, 1000), seasonal, peaks)
		return ports.ResourceMetrics{ResourceID: r.ID, CPU: &cpuP, Connections: &conns, Coverage: 1.0}, true

	default:
		return ports.ResourceMetrics{}, false
	}
}

// defaultBaseline picks a representative median for a profile when the
// resource itself carries no per-resource anchor (only EC2Instance has
// CPUBaselineP50; every other kind uses this).
func defaultBaseline(p UtilizationProfile) float64 {
	switch p {
	case ProfileUnused:
		return 0.8
	case ProfileIdle:
		return 5
	case ProfileSteady:
		return 55
	case ProfileSpiky:
		return 10
	case ProfileCyclical:
		return 35
	case ProfileSaturated:
		return 92
	default:
		return 25
	}
}

// rngFor returns a PRNG seeded deterministically from a resource's native
// ID and the requested window, so Collect is reproducible run to run for
// the same inputs (Go's map iteration order elsewhere in this package
// never leaks into this seed — it depends only on the resource's own id
// string and the window's bounds).
func rngFor(nativeID string, window core.Period) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(nativeID))
	seed := int64(h.Sum64()) ^ window.Start.Unix() ^ (window.End.Unix() << 1)
	return rand.New(rand.NewSource(seed))
}

// summarize wraps core.SummarizeSamples and layers on the two fields it
// deliberately leaves at their zero value: Seasonal and PeakHours, which
// this package's own generator already knows the answer to (it decided
// whether the series has a daily cycle, and which hours peaked, while
// building it) and a percentile pass over the numbers alone cannot
// recover.
func summarize(values []float64, seasonal bool, peakHours []int) core.Percentiles {
	p := core.SummarizeSamples(values, 1.0)
	p.Seasonal = seasonal
	p.PeakHours = peakHours
	return p
}

// clamp01to100 keeps a percent value in AWS's own [0, 100] range.
func clamp01to100(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// profileSeries generates one resource's raw utilisation series (percent,
// 0-100) at the requested step across the window, along with whether the
// series carries a genuine daily/weekly cycle and, if so, which UTC hours
// its peaks land in.
func profileSeries(profile UtilizationProfile, baseline float64, window core.Period, step int, rng *rand.Rand) (values []float64, seasonal bool, peakHours []int) {
	n := sampleCount(window, step)
	if n == 0 {
		return nil, false, nil
	}
	values = make([]float64, n)

	switch profile {
	case ProfileUnused:
		// Flat at the background noise floor: no diurnal shape, no tail, and
		// a coefficient of variation small enough that the series reads as
		// "nothing ever happened here" rather than "we did not watch for
		// long enough". Both halves matter — a P99 below the never-used
		// rule's ceiling is what makes the finding fire, and the high
		// stability is what carries its confidence past the bar a policy
		// needs before it will act unattended.
		for i := range values {
			values[i] = clamp01to100(baseline + rng.NormFloat64()*(baseline*0.05))
		}
		return values, false, nil

	case ProfileIdle:
		// Consistently near zero: Label()'s "consistently idle" needs high
		// stability (low variance relative to mean) and P99 under 20. Noise
		// scales with the baseline itself (real idle CPU noise is a small
		// fraction of an already-tiny mean, not a fixed absolute jitter),
		// which is what keeps stability high across the full 2-8% baseline
		// range the demo estate's idle instances use.
		noise := math.Max(baseline*0.12, 0.15)
		for i := range values {
			values[i] = clamp01to100(baseline + rng.NormFloat64()*noise)
		}
		return values, false, nil

	case ProfileSteady:
		// A flat plateau with only measurement-noise variance: coefficient
		// of variation well under 0.2, which is what Label() reads as
		// "steady".
		for i := range values {
			values[i] = clamp01to100(baseline + rng.NormFloat64()*(baseline*0.05+1))
		}
		return values, false, nil

	case ProfileSpiky:
		// Mostly resting near baseline, with an occasional sharp spike —
		// P99 several times the median, P50 barely above the resting
		// level, which is exactly Label()'s spiky test.
		for i := range values {
			if rng.Float64() < 0.03 {
				values[i] = clamp01to100(baseline*3 + rng.Float64()*(95-baseline*3))
			} else {
				values[i] = clamp01to100(baseline*0.4 + rng.NormFloat64()*2)
			}
		}
		return values, false, nil

	case ProfileSaturated:
		// Pinned near the ceiling: a resource with nowhere left to give,
		// which is the shape that should read as "no headroom" rather than
		// "idle" or "steady" even though its own variance is low.
		for i := range values {
			values[i] = clamp01to100(baseline + rng.NormFloat64()*2.5)
		}
		return values, false, nil

	case ProfileCyclical:
		return cyclicalSeries(baseline, window, step, rng)

	default:
		for i := range values {
			values[i] = clamp01to100(baseline + rng.NormFloat64()*5)
		}
		return values, false, nil
	}
}

// cyclicalSeries models a business-hours workload: a daily sine wave
// peaking mid-afternoon UTC, damped to a third of its amplitude on
// weekends, plus noise. It reports the true peak hours (computed from the
// same hour-of-day buckets a real seasonality detector would build) rather
// than leaving PeakHours empty.
func cyclicalSeries(baseline float64, window core.Period, step int, rng *rand.Rand) (values []float64, seasonal bool, peakHours []int) {
	n := sampleCount(window, step)
	values = make([]float64, n)
	amplitude := math.Max(baseline*0.8, 10)

	hourSums := make([]float64, 24)
	hourCounts := make([]int, 24)

	t := window.Start
	for i := 0; i < n; i++ {
		hour := float64(t.Hour()) + float64(t.Minute())/60
		// Peaks around 15:00 UTC, trough around 03:00 UTC.
		phase := (hour - 15) / 24 * 2 * math.Pi
		wave := math.Cos(phase)
		amp := amplitude
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
			amp *= 0.35
		}
		v := clamp01to100(baseline + amp*wave + rng.NormFloat64()*2)
		values[i] = v
		hourSums[t.Hour()] += v
		hourCounts[t.Hour()]++
		t = t.Add(time.Duration(step) * time.Second)
	}

	// Peak hours: the top quarter of hours by average value, matching how
	// core.Percentiles.PeakHours is documented ("UTC hours of observed
	// peaks").
	type avg struct {
		hour int
		mean float64
	}
	avgs := make([]avg, 0, 24)
	for h := 0; h < 24; h++ {
		if hourCounts[h] == 0 {
			continue
		}
		avgs = append(avgs, avg{h, hourSums[h] / float64(hourCounts[h])})
	}
	for i := 1; i < len(avgs); i++ {
		for j := i; j > 0 && avgs[j].mean > avgs[j-1].mean; j-- {
			avgs[j], avgs[j-1] = avgs[j-1], avgs[j]
		}
	}
	top := len(avgs) / 4
	if top < 1 && len(avgs) > 0 {
		top = 1
	}
	for i := 0; i < top; i++ {
		peakHours = append(peakHours, avgs[i].hour)
	}
	return values, true, peakHours
}

// sampleCount turns a window and a step into a sample count, capped so a
// long window with a fine step cannot blow up generation cost.
func sampleCount(window core.Period, step int) int {
	if step <= 0 {
		step = 300
	}
	total := window.Duration().Seconds()
	if total <= 0 {
		return 0
	}
	n := int(total / float64(step))
	if n < 1 {
		n = 1
	}
	if n > 20_000 {
		n = 20_000
	}
	return n
}

// scaleSeries maps a 0-100 percent series onto an arbitrary [min, max]
// range, preserving its shape. Reusing the same underlying series for a
// resource's other metrics (network bytes tracking CPU, connections
// tracking CPU) is deliberate: on a real instance those really do move
// together, which is what makes the simulated telemetry internally
// consistent rather than independently random per field.
func scaleSeries(percent []float64, min, max float64) []float64 {
	out := make([]float64, len(percent))
	for i, v := range percent {
		out[i] = min + (v/100)*(max-min)
	}
	return out
}

// invert flips a 0-100 percent series (100-v), used for metrics that move
// opposite to load, such as Lambda cold-start-driven error rate rising
// when traffic (and therefore warm concurrency) is low.
func invert(percent []float64) []float64 {
	out := make([]float64, len(percent))
	for i, v := range percent {
		out[i] = 100 - v
	}
	return out
}
