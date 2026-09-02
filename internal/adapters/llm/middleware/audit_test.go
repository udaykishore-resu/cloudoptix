package middleware

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestAuditingProvider_LogsSuccessfulCall(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	inner := newMockProvider()
	inner.responses = []ports.CompletionResponse{{Content: "answer", Model: "det-v1", InputTokens: 42, OutputTokens: 7, StopReason: "end_turn"}}
	a := NewAuditingProvider(inner, logger)

	_, err := a.Complete(context.Background(), ports.CompletionRequest{TenantID: "t1", Purpose: "copilot"})
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, "llm_call")
	require.Contains(t, out, "t1")
	require.Contains(t, out, "copilot")
	require.Contains(t, out, "det-v1")
	require.Contains(t, out, `"input_tokens":42`)
	require.Contains(t, out, `"output_tokens":7`)
}

func TestAuditingProvider_LogsFailedCall(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	inner := newMockProvider()
	inner.errs = []error{errors.New("provider down")}
	a := NewAuditingProvider(inner, logger)

	_, err := a.Complete(context.Background(), ports.CompletionRequest{TenantID: "t1", Purpose: "onboarding"})
	require.Error(t, err)

	out := buf.String()
	require.Contains(t, out, "llm_call")
	require.Contains(t, out, "provider down")
	require.Contains(t, out, "WARN")
}

func TestAuditingProvider_NilLoggerFallsBackToDefault(t *testing.T) {
	inner := newMockProvider()
	a := NewAuditingProvider(inner, nil)
	_, err := a.Complete(context.Background(), ports.CompletionRequest{})
	require.NoError(t, err)
}
