package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestMetricsProvider_RecordsRequestsAndTokens(t *testing.T) {
	reg := prometheus.NewRegistry()
	inner := newMockProvider()
	inner.responses = []ports.CompletionResponse{{InputTokens: 100, OutputTokens: 20}}
	m := NewMetricsProvider(inner, reg)

	_, err := m.Complete(context.Background(), ports.CompletionRequest{Purpose: "copilot"})
	require.NoError(t, err)

	families, err := reg.Gather()
	require.NoError(t, err)

	var found bool
	for _, f := range families {
		if f.GetName() == "cloudoptix_llm_input_tokens_total" {
			found = true
			require.Equal(t, float64(100), sumCounter(f))
		}
	}
	require.True(t, found, "input token counter must be registered and populated")
}

func TestMetricsProvider_RecordsErrors(t *testing.T) {
	reg := prometheus.NewRegistry()
	inner := newMockProvider()
	inner.errs = []error{errors.New("boom")}
	m := NewMetricsProvider(inner, reg)

	_, err := m.Complete(context.Background(), ports.CompletionRequest{Purpose: "copilot"})
	require.Error(t, err)

	families, err := reg.Gather()
	require.NoError(t, err)
	var errCount float64
	for _, f := range families {
		if f.GetName() == "cloudoptix_llm_errors_total" {
			errCount = sumCounter(f)
		}
	}
	require.Equal(t, float64(1), errCount)
}

func sumCounter(f *dto.MetricFamily) float64 {
	var total float64
	for _, m := range f.GetMetric() {
		total += m.GetCounter().GetValue()
	}
	return total
}
