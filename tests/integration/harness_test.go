// Package integration_test exercises cross-cutting invariants that no
// single package can assert alone: tenant isolation across every read path,
// the AI safety boundary across the copilot and the policy engine, and the
// cost compiler against real fixture plans.
//
// Like tests/e2e, these build the real composition root rather than an
// object graph of their own. An isolation test that constructed its own
// repositories would be testing the repositories; the question here is
// whether the assembled system leaks, which only the assembled system can
// answer.
//
// Traceability: REQ-SEC-003, REQ-AI-006, REQ-COMP-006, SPEC-SEC-003, SPEC-AI-002.
package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
)

func newApp(t *testing.T) *app.App {
	t.Helper()

	cfg := config.Defaults()
	cfg.Environment = "test"
	cfg.Storage = config.StorageMemory
	cfg.Cache = config.CacheMemory
	cfg.Events.Kind = config.EventsInProcess
	cfg.AWS.Mode = config.AWSModeSimulated
	cfg.LLM.Provider = config.LLMProviderScripted
	cfg.Telemetry.MetricsEnabled = false
	// The dev static-token issuer mints a principal scoped to
	// app.DemoTenantID, which is what lets the HTTP assertions present a
	// genuinely valid credential for one tenant and ask for another's rows
	// with it. auth.NewDevIssuer refuses to construct under
	// environment=="production", so this cannot follow the config anywhere
	// it should not go.
	cfg.Auth.DevStaticTokenEnabled = true
	cfg.Auth.DevStaticToken = config.NewLiteralSecret(devToken)
	require.NoError(t, cfg.Validate())

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a, err := app.Build(context.Background(), cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func adminCtx(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject:  "admin@" + string(tenant),
		TenantID: tenant,
		Email:    "admin@" + string(tenant) + ".example",
		Roles:    []core.Role{core.RoleTenantAdmin},
		IssuedAt: time.Now().UTC(),
	})
}
