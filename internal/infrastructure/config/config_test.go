package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func minimalEnviron() []string {
	return []string{
		"CLOUDOPTIX_DATABASE_PASSWORD=devpassword",
		"CLOUDOPTIX_AUTH_DEV_STATIC_TOKEN_ENABLED=true",
		"CLOUDOPTIX_AUTH_DEV_STATIC_TOKEN=devtoken",
	}
}

func TestLoad_DefaultsAreValid(t *testing.T) {
	cfg, err := Load(LoadOptions{Environ: minimalEnviron()})
	require.NoError(t, err)
	assert.Equal(t, "development", cfg.Environment)
	assert.Equal(t, 8080, cfg.Server.Port)
}

func TestLoad_EnvOverridesDefaults(t *testing.T) {
	env := append(minimalEnviron(),
		"CLOUDOPTIX_SERVER_PORT=9999",
		"CLOUDOPTIX_TELEMETRY_LOG_LEVEL=debug",
	)
	cfg, err := Load(LoadOptions{Environ: env})
	require.NoError(t, err)
	assert.Equal(t, 9999, cfg.Server.Port)
	assert.Equal(t, "debug", cfg.Telemetry.LogLevel)
}

func TestLoad_FlagsOverrideEnv(t *testing.T) {
	env := append(minimalEnviron(), "CLOUDOPTIX_SERVER_PORT=9999")
	cfg, err := Load(LoadOptions{Environ: env, Args: []string{"-server-port=7000"}})
	require.NoError(t, err)
	assert.Equal(t, 7000, cfg.Server.Port)
}

func TestLoad_YAMLLayerBelowEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  port: 6000\ntelemetry:\n  log_level: warn\n"), 0o600))

	env := append(minimalEnviron(), "CLOUDOPTIX_SERVER_PORT=7000") // env should win over file
	cfg, err := Load(LoadOptions{FilePath: path, Environ: env})
	require.NoError(t, err)
	assert.Equal(t, 7000, cfg.Server.Port, "env must override the file")
	assert.Equal(t, "warn", cfg.Telemetry.LogLevel, "file must override the default")
}

func TestLoad_SecretLiteralInYAMLFileIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database:\n  password: hunter2\n"), 0o600))

	_, err := Load(LoadOptions{FilePath: path, Environ: minimalEnviron()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "may not hold a literal value")
}

func TestLoad_SecretReferenceInYAMLFileIsAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database:\n  password: \"env:MY_DB_PASSWORD\"\n"), 0o600))

	env := []string{
		"CLOUDOPTIX_ENVIRONMENT=test",
		"CLOUDOPTIX_AUTH_DEV_STATIC_TOKEN_ENABLED=true",
		"CLOUDOPTIX_AUTH_DEV_STATIC_TOKEN=devtoken",
		"MY_DB_PASSWORD=supersecret",
	}
	cfg, err := Load(LoadOptions{FilePath: path, Environ: env})
	require.NoError(t, err)

	require.NoError(t, ResolveSecrets(context.Background(), cfg, func(k string) (string, bool) {
		for _, e := range env {
			if kk, v, ok := splitOnce(e); ok && kk == k {
				return v, true
			}
		}
		return "", false
	}, nil))
	assert.Equal(t, "supersecret", cfg.Database.Password.Value())
}

func splitOnce(s string) (k, v string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

func TestValidate_ProductionRejectsDevStaticToken(t *testing.T) {
	env := []string{
		"CLOUDOPTIX_ENVIRONMENT=production",
		"CLOUDOPTIX_DATABASE_PASSWORD=x",
		"CLOUDOPTIX_AUTH_OIDC_ISSUER_URL=https://issuer.example.com",
		"CLOUDOPTIX_AUTH_DEV_STATIC_TOKEN_ENABLED=true",
		"CLOUDOPTIX_SERVER_TLS_ENABLED=true",
		"CLOUDOPTIX_SERVER_TLS_CERT_FILE=/etc/tls/cert.pem",
		"CLOUDOPTIX_SERVER_TLS_KEY_FILE=/etc/tls/key.pem",
		"CLOUDOPTIX_LLM_PROVIDER=anthropic",
		"CLOUDOPTIX_LLM_API_KEY=x",
	}
	_, err := Load(LoadOptions{Environ: env})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dev_static_token_enabled")
}

func TestValidate_ProductionRequiresTLS(t *testing.T) {
	env := []string{
		"CLOUDOPTIX_ENVIRONMENT=production",
		"CLOUDOPTIX_DATABASE_PASSWORD=x",
		"CLOUDOPTIX_AUTH_OIDC_ISSUER_URL=https://issuer.example.com",
		"CLOUDOPTIX_LLM_PROVIDER=anthropic",
		"CLOUDOPTIX_LLM_API_KEY=x",
	}
	_, err := Load(LoadOptions{Environ: env})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tls_enabled")
}

func TestValidate_RejectsNoneAlgorithm(t *testing.T) {
	cfg := Defaults()
	cfg.Environment = "test"
	cfg.Database.Password = NewLiteralSecret("x")
	cfg.Auth.OIDCIssuerURL = "https://issuer.example.com"
	cfg.Auth.AllowedAlgorithms = []string{"RS256", "none"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "none")
}

func TestValidate_EmptyAlgorithmAllowlistRejected(t *testing.T) {
	cfg := Defaults()
	cfg.Environment = "test"
	cfg.Database.Password = NewLiteralSecret("x")
	cfg.Auth.OIDCIssuerURL = "https://issuer.example.com"
	cfg.Auth.AllowedAlgorithms = nil
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed_algorithms")
}

func TestEnvVarDocs_CoversSecretsAndIsNonEmpty(t *testing.T) {
	docs := EnvVarDocs()
	require.NotEmpty(t, docs)
	var sawSecret bool
	for _, d := range docs {
		assert.NotEmpty(t, d.Description, "%s must document its purpose", d.Name)
		if d.Name == "CLOUDOPTIX_DATABASE_PASSWORD" {
			sawSecret = true
			assert.True(t, d.Secret)
		}
	}
	assert.True(t, sawSecret)
}

func TestSecret_RedactsInLogsAndJSON(t *testing.T) {
	s := NewLiteralSecret("supersecretvalue")
	assert.NotContains(t, s.String(), "supersecretvalue")
	b, err := s.MarshalJSON()
	require.NoError(t, err)
	assert.NotContains(t, string(b), "supersecretvalue")
}
