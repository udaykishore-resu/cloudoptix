package middleware

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// MetricsProvider wraps a ports.LLMProvider with Prometheus counters and
// histograms for latency and token usage, labelled by provider and purpose
// so a dashboard can separate "onboarding extraction" cost from "copilot"
// cost without a log query. A caller-supplied prometheus.Registerer means
// tests can use a private registry instead of the global default one, which
// is what keeps repeated test runs from panicking on duplicate
// registration.
type MetricsProvider struct {
	inner ports.LLMProvider

	requests    *prometheus.CounterVec
	errors      *prometheus.CounterVec
	latency     *prometheus.HistogramVec
	inputTokens *prometheus.CounterVec
	outTokens   *prometheus.CounterVec
}

var _ ports.LLMProvider = (*MetricsProvider)(nil)

// NewMetricsProvider wraps inner, registering its collectors against reg. reg
// may be prometheus.DefaultRegisterer in production; tests should pass a
// fresh prometheus.NewRegistry().
func NewMetricsProvider(inner ports.LLMProvider, reg prometheus.Registerer) *MetricsProvider {
	m := &MetricsProvider{
		inner: inner,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cloudoptix_llm_requests_total", Help: "Completed LLM provider calls.",
		}, []string{"provider", "purpose"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cloudoptix_llm_errors_total", Help: "Failed LLM provider calls.",
		}, []string{"provider", "purpose"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "cloudoptix_llm_latency_ms", Help: "LLM provider call latency in milliseconds.",
			Buckets: []float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000},
		}, []string{"provider", "purpose"}),
		inputTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cloudoptix_llm_input_tokens_total", Help: "Input tokens consumed.",
		}, []string{"provider", "purpose"}),
		outTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cloudoptix_llm_output_tokens_total", Help: "Output tokens produced.",
		}, []string{"provider", "purpose"}),
	}
	if reg != nil {
		reg.MustRegister(m.requests, m.errors, m.latency, m.inputTokens, m.outTokens)
	}
	return m
}

func (m *MetricsProvider) Name() string { return m.inner.Name() }

func (m *MetricsProvider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	purpose := req.Purpose
	if purpose == "" {
		purpose = "unspecified"
	}
	start := time.Now()
	resp, err := m.inner.Complete(ctx, req)
	elapsedMS := float64(time.Since(start).Milliseconds())

	m.requests.WithLabelValues(m.inner.Name(), purpose).Inc()
	m.latency.WithLabelValues(m.inner.Name(), purpose).Observe(elapsedMS)
	if err != nil {
		m.errors.WithLabelValues(m.inner.Name(), purpose).Inc()
		return resp, err
	}
	m.inputTokens.WithLabelValues(m.inner.Name(), purpose).Add(float64(resp.InputTokens))
	m.outTokens.WithLabelValues(m.inner.Name(), purpose).Add(float64(resp.OutputTokens))
	return resp, nil
}

func (m *MetricsProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return m.inner.Embed(ctx, texts)
}

func (m *MetricsProvider) Healthy(ctx context.Context) bool { return m.inner.Healthy(ctx) }
