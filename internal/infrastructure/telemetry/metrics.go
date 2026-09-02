package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics bundles every Prometheus collector CloudOptix registers, so a
// constructor call site gets one value to pass around instead of two dozen
// package-level globals — package-level metric globals make it impossible to
// run two instrumented components (e.g. two test servers) in the same
// process without them silently sharing counters.
type Metrics struct {
	Registry *prometheus.Registry

	// HTTP API surface, labelled by route (the chi pattern, not the raw path
	// with IDs in it — that would create unbounded cardinality) and status.
	HTTPRequestDuration  *prometheus.HistogramVec
	HTTPRequestsTotal    *prometheus.CounterVec
	HTTPRequestsInFlight *prometheus.GaugeVec

	// Outbound AWS calls, by service and operation.
	AWSAPICallsTotal     *prometheus.CounterVec
	AWSAPIFailuresTotal  *prometheus.CounterVec
	AWSAPIThrottlesTotal *prometheus.CounterVec
	AWSAPILatency        *prometheus.HistogramVec

	// Discovery.
	DiscoveryDuration *prometheus.HistogramVec
	DiscoveryCoverage *prometheus.GaugeVec

	// Optimization / recommendations.
	RecommendationGenLatency *prometheus.HistogramVec

	// LLM usage, by purpose (onboarding/copilot/narrative/...).
	LLMLatency      *prometheus.HistogramVec
	LLMInputTokens  *prometheus.CounterVec
	LLMOutputTokens *prometheus.CounterVec
	LLMCostUSD      *prometheus.CounterVec

	// Governance.
	PolicyEvaluationsTotal *prometheus.CounterVec

	// Execution / automation.
	ExecutionsTotal *prometheus.CounterVec // labelled result=success|failure|rolled_back

	// Savings.
	SavingsRealizedUSD *prometheus.CounterVec

	// Cache.
	CacheHitsTotal   *prometheus.CounterVec
	CacheMissesTotal *prometheus.CounterVec

	// Background work.
	QueueDepth *prometheus.GaugeVec
}

// NewMetrics constructs and registers every collector against a fresh
// registry. namespace/subsystem follow Prometheus convention
// (namespace_subsystem_name); CloudOptix uses namespace "cloudoptix" for
// everything so a Grafana dashboard can glob cloudoptix_*.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	const ns = "cloudoptix"

	m := &Metrics{
		Registry: reg,

		HTTPRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: ns, Subsystem: "http", Name: "request_duration_seconds",
			Help:    "API request latency by route and status.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),

		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "http", Name: "requests_total",
			Help: "API requests by route, method and status.",
		}, []string{"route", "method", "status"}),

		HTTPRequestsInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns, Subsystem: "http", Name: "requests_in_flight",
			Help: "API requests currently being handled, by route.",
		}, []string{"route"}),

		AWSAPICallsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "aws", Name: "api_calls_total",
			Help: "Outbound AWS API calls by service and operation.",
		}, []string{"service", "operation"}),

		AWSAPIFailuresTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "aws", Name: "api_failures_total",
			Help: "Outbound AWS API calls that failed, by service, operation and error class.",
		}, []string{"service", "operation", "error_class"}),

		AWSAPIThrottlesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "aws", Name: "api_throttles_total",
			Help: "Outbound AWS API calls throttled, by service and operation.",
		}, []string{"service", "operation"}),

		AWSAPILatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: ns, Subsystem: "aws", Name: "api_latency_seconds",
			Help:    "Outbound AWS API call latency by service and operation.",
			Buckets: prometheus.ExponentialBuckets(0.005, 2, 14),
		}, []string{"service", "operation"}),

		DiscoveryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: ns, Subsystem: "discovery", Name: "run_duration_seconds",
			Help:    "Discovery run duration by trigger.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 12),
		}, []string{"trigger"}),

		DiscoveryCoverage: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns, Subsystem: "discovery", Name: "coverage_ratio",
			Help: "Fraction of expected services/regions successfully scanned in the most recent run, by tenant.",
		}, []string{"tenant"}),

		RecommendationGenLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: ns, Subsystem: "optimization", Name: "recommendation_generation_seconds",
			Help:    "Time to generate a full recommendation set for an analysis run.",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 12),
		}, []string{"tenant"}),

		// Subsystem "llm_usage", not "llm": these four are the platform's
		// spend-and-usage roll-up (note the cost series, which only this set
		// carries). The per-call provider instrumentation lives in
		// internal/adapters/llm/middleware and publishes under a flat
		// cloudoptix_llm_* prefix. Both used "llm" until they were first
		// registered against one registry, at which point three of the names
		// collided outright and the process panicked at startup. Separating
		// the subsystem is the fix; merging them is not, because they answer
		// different questions — "what did the model cost us this month"
		// against "is the provider slow right now".
		LLMLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: ns, Subsystem: "llm_usage", Name: "completion_latency_seconds",
			Help:    "LLM completion latency by purpose and provider.",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 12),
		}, []string{"purpose", "provider"}),

		LLMInputTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "llm_usage", Name: "input_tokens_total",
			Help: "LLM input tokens consumed by purpose and provider.",
		}, []string{"purpose", "provider"}),

		LLMOutputTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "llm_usage", Name: "output_tokens_total",
			Help: "LLM output tokens produced by purpose and provider.",
		}, []string{"purpose", "provider"}),

		LLMCostUSD: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "llm_usage", Name: "cost_usd_total",
			Help: "Estimated LLM spend in USD by purpose and provider.",
		}, []string{"purpose", "provider"}),

		PolicyEvaluationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "policy", Name: "evaluations_total",
			Help: "Policy evaluations by resulting effect (auto_execute|require_approval|prohibit|advisory_only).",
		}, []string{"effect"}),

		ExecutionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "execution", Name: "results_total",
			Help: "Execution plan outcomes by result (success|failure|rolled_back) and action type.",
		}, []string{"result", "action"}),

		SavingsRealizedUSD: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "savings", Name: "realized_usd_total",
			Help: "Monthly savings realized and validated, in USD, by category.",
		}, []string{"category"}),

		CacheHitsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "cache", Name: "hits_total",
			Help: "Cache hits by cache name.",
		}, []string{"cache"}),

		CacheMissesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: ns, Subsystem: "cache", Name: "misses_total",
			Help: "Cache misses by cache name.",
		}, []string{"cache"}),

		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: ns, Subsystem: "worker", Name: "queue_depth",
			Help: "Pending items by queue name.",
		}, []string{"queue"}),
	}

	reg.MustRegister(
		m.HTTPRequestDuration, m.HTTPRequestsTotal, m.HTTPRequestsInFlight,
		m.AWSAPICallsTotal, m.AWSAPIFailuresTotal, m.AWSAPIThrottlesTotal, m.AWSAPILatency,
		m.DiscoveryDuration, m.DiscoveryCoverage,
		m.RecommendationGenLatency,
		m.LLMLatency, m.LLMInputTokens, m.LLMOutputTokens, m.LLMCostUSD,
		m.PolicyEvaluationsTotal,
		m.ExecutionsTotal,
		m.SavingsRealizedUSD,
		m.CacheHitsTotal, m.CacheMissesTotal,
		m.QueueDepth,
	)
	return m
}

// Handler returns the /metrics HTTP handler for this registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// RecordCacheHit and RecordCacheMiss increment the named cache's counters.
// The hit rate itself is a PromQL ratio
// (rate(cloudoptix_cache_hits_total[5m]) /
//
//	(rate(cloudoptix_cache_hits_total[5m]) + rate(cloudoptix_cache_misses_total[5m])))
//
// rather than something this process computes and exposes as its own gauge —
// a rate is only meaningful over a window, and the window belongs to
// whoever is querying, not to the process emitting the raw counters.
func (m *Metrics) RecordCacheHit(cache string)  { m.CacheHitsTotal.WithLabelValues(cache).Inc() }
func (m *Metrics) RecordCacheMiss(cache string) { m.CacheMissesTotal.WithLabelValues(cache).Inc() }

// ObserveHTTPRequest is the instrumentation helper for a handler: call it
// with the route pattern, method, status and elapsed duration once a
// response has been written. The transport middleware chain (middleware.go)
// is the sole caller in production, wrapping every handler exactly once, so
// this method is the single point where request metrics are recorded rather
// than being scattered per-handler.
func (m *Metrics) ObserveHTTPRequest(route, method string, status int, d time.Duration) {
	statusStr := strconv.Itoa(status)
	m.HTTPRequestDuration.WithLabelValues(route, method, statusStr).Observe(d.Seconds())
	m.HTTPRequestsTotal.WithLabelValues(route, method, statusStr).Inc()
}

// InFlight returns increment/decrement functions for the in-flight gauge,
// used by the middleware as `done := m.InFlight(route); defer done()`.
func (m *Metrics) InFlight(route string) (done func()) {
	g := m.HTTPRequestsInFlight.WithLabelValues(route)
	g.Inc()
	return g.Dec
}

// ObserveAWSCall records one outbound AWS API call's outcome. errClass is ""
// on success, or a short classification ("throttled", "timeout",
// "access_denied", "unavailable", ...) on failure.
func (m *Metrics) ObserveAWSCall(service, operation string, d time.Duration, errClass string) {
	m.AWSAPICallsTotal.WithLabelValues(service, operation).Inc()
	m.AWSAPILatency.WithLabelValues(service, operation).Observe(d.Seconds())
	if errClass != "" {
		m.AWSAPIFailuresTotal.WithLabelValues(service, operation, errClass).Inc()
		if errClass == "throttled" {
			m.AWSAPIThrottlesTotal.WithLabelValues(service, operation).Inc()
		}
	}
}

// ObserveLLMCall records one completion call's latency, token usage and
// estimated cost.
func (m *Metrics) ObserveLLMCall(purpose, provider string, d time.Duration, inputTokens, outputTokens int, costUSD float64) {
	m.LLMLatency.WithLabelValues(purpose, provider).Observe(d.Seconds())
	m.LLMInputTokens.WithLabelValues(purpose, provider).Add(float64(inputTokens))
	m.LLMOutputTokens.WithLabelValues(purpose, provider).Add(float64(outputTokens))
	m.LLMCostUSD.WithLabelValues(purpose, provider).Add(costUSD)
}

// InstrumentJob wraps a background job function with duration logging and a
// panic-safe boundary, for use by the discovery/execution/notification
// workers. It does not itself pick a metric to update — callers pass the
// specific histogram (DiscoveryDuration, RecommendationGenLatency, ...)
// because each job type has a different label set.
func InstrumentJob(ctx context.Context, hist *prometheus.HistogramVec, labels []string, fn func(ctx context.Context) error) (err error) {
	start := time.Now()
	defer func() {
		hist.WithLabelValues(labels...).Observe(time.Since(start).Seconds())
		if r := recover(); r != nil {
			err = panicToError(r)
		}
	}()
	return fn(ctx)
}
