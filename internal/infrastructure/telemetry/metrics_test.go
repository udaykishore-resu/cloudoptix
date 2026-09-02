package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMetrics_RegistersWithoutPanic(t *testing.T) {
	m := NewMetrics()
	require.NotNil(t, m.Registry)

	// A vector metric with no observed label combination yet is legal to
	// register but produces no series until touched — Gather() must not
	// error even though nothing has been observed.
	_, err := m.Registry.Gather()
	require.NoError(t, err)

	m.ObserveHTTPRequest("/api/v1/resources", "GET", 200, time.Millisecond)
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	assert.NotEmpty(t, families)
}

func TestObserveHTTPRequest(t *testing.T) {
	m := NewMetrics()
	m.ObserveHTTPRequest("/api/v1/resources", "GET", 200, 12*time.Millisecond)

	families, err := m.Registry.Gather()
	require.NoError(t, err)

	var found bool
	for _, f := range families {
		if f.GetName() == "cloudoptix_http_requests_total" {
			found = true
			require.Len(t, f.GetMetric(), 1)
			assert.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue())
		}
	}
	assert.True(t, found, "expected cloudoptix_http_requests_total to be present")
}

func TestInFlight_IncrementsAndDecrements(t *testing.T) {
	m := NewMetrics()
	done := m.InFlight("/api/v1/costs")
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	assert.Equal(t, 1.0, gaugeValue(t, families, "cloudoptix_http_requests_in_flight"))

	done()
	families, err = m.Registry.Gather()
	require.NoError(t, err)
	assert.Equal(t, 0.0, gaugeValue(t, families, "cloudoptix_http_requests_in_flight"))
}

func TestInstrumentJob_RecordsDurationAndRecoversPanic(t *testing.T) {
	m := NewMetrics()

	err := InstrumentJob(context.Background(), m.DiscoveryDuration, []string{"manual"}, func(ctx context.Context) error {
		panic("boom")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")

	families, gatherErr := m.Registry.Gather()
	require.NoError(t, gatherErr)
	var sawSample bool
	for _, f := range families {
		if f.GetName() == "cloudoptix_discovery_run_duration_seconds" {
			for _, mm := range f.GetMetric() {
				if mm.GetHistogram().GetSampleCount() == 1 {
					sawSample = true
				}
			}
		}
	}
	assert.True(t, sawSample, "expected the histogram to record one observation even though fn panicked")
}

func TestInstrumentJob_PropagatesRealError(t *testing.T) {
	m := NewMetrics()
	sentinel := errors.New("discovery failed")
	err := InstrumentJob(context.Background(), m.DiscoveryDuration, []string{"scheduled"}, func(ctx context.Context) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func gaugeValue(t *testing.T, families []*dto.MetricFamily, name string) float64 {
	t.Helper()
	for _, f := range families {
		if f.GetName() == name {
			require.Len(t, f.GetMetric(), 1)
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric family %s not found", name)
	return 0
}
