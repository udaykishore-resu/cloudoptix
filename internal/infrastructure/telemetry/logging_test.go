package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := NewLogger(LogConfig{Level: "debug", Format: "json", Output: buf})
	return logger, buf
}

func TestRedaction_SensitiveKeyNames(t *testing.T) {
	cases := []struct {
		key   string
		value string
	}{
		{"password", "hunter2"},
		{"db_password", "hunter2"},
		{"api_key", "sk-abcdef1234567890"},
		{"apiKey", "sk-abcdef1234567890"},
		{"secret", "topsecretvalue"},
		{"client_secret", "topsecretvalue"},
		{"authorization", "Bearer eyJhbGciOiJI.something.here"},
		{"aws_secret_access_key", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"session_token", "AQoDYXdz...longtoken"},
		{"access_key", "AKIAIOSFODNN7EXAMPLE"},
		{"credentials", "some-credential-blob"},
		{"private_key", "-----BEGIN PRIVATE KEY-----"},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			logger, buf := newTestLogger(t)
			logger.Info("test event", slog.String(tc.key, tc.value))
			out := buf.String()
			assert.NotContains(t, out, tc.value, "value for key %q leaked into log output", tc.key)
			assert.Contains(t, out, redactedPlaceholder)
		})
	}
}

func TestRedaction_EmbeddedAWSAccessKeyInFreeText(t *testing.T) {
	logger, buf := newTestLogger(t)
	logger.Error("assume role failed",
		slog.String("error", "sts error for identity AKIAIOSFODNN7EXAMPLE: access denied"))
	out := buf.String()
	assert.NotContains(t, out, "AKIAIOSFODNN7EXAMPLE")
	assert.Contains(t, out, redactedPlaceholder)
}

func TestRedaction_EmbeddedBearerTokenInFreeText(t *testing.T) {
	logger, buf := newTestLogger(t)
	logger.Info("incoming request",
		slog.String("raw_header", "Authorization: Bearer sk-live-abcdefghijklmnopqrstuvwxyz"))
	out := buf.String()
	assert.NotContains(t, out, "sk-live-abcdefghijklmnopqrstuvwxyz")
	assert.Contains(t, out, "Bearer "+redactedPlaceholder)
}

func TestRedaction_OrdinaryFieldsPassThrough(t *testing.T) {
	logger, buf := newTestLogger(t)
	logger.Info("resource discovered",
		slog.String("resource_id", "res_01j9x2m4qk_8f21c0de"),
		slog.String("service", "ec2"),
		slog.Int("count", 42),
	)
	out := buf.String()
	assert.Contains(t, out, "res_01j9x2m4qk_8f21c0de")
	assert.Contains(t, out, "ec2")
	assert.Contains(t, out, "42")
	assert.NotContains(t, out, redactedPlaceholder)
}

func TestRedaction_NestedGroupKeysAreStillChecked(t *testing.T) {
	logger, buf := newTestLogger(t)
	logger.Info("db connect",
		slog.Group("database", slog.String("password", "hunter2"), slog.String("host", "db.internal")))
	out := buf.String()
	assert.NotContains(t, out, "hunter2")
	assert.Contains(t, out, "db.internal")
}

func TestLogger_JSONOutputIsWellFormed(t *testing.T) {
	logger, buf := newTestLogger(t)
	logger.Info("hello", slog.String("k", "v"))
	assert.True(t, bytes.HasPrefix(bytes.TrimSpace(buf.Bytes()), []byte("{")))
	assert.Contains(t, buf.String(), `"msg":"hello"`)
}

func TestLogger_TraceCorrelation_NoSpanIsSilent(t *testing.T) {
	logger, buf := newTestLogger(t)
	logger.InfoContext(context.Background(), "no span in context")
	out := buf.String()
	assert.NotContains(t, out, `"trace_id"`)
}

func TestParseLevel(t *testing.T) {
	require.Equal(t, slog.LevelDebug, parseLevel("debug"))
	require.Equal(t, slog.LevelWarn, parseLevel("warn"))
	require.Equal(t, slog.LevelError, parseLevel("error"))
	require.Equal(t, slog.LevelInfo, parseLevel("info"))
	require.Equal(t, slog.LevelInfo, parseLevel("nonsense"))
}
