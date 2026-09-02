// Package e2e_test drives CloudOptix end to end through its real
// composition root.
//
// These tests build the same *app.App the cloudoptix-api binary builds, with
// the same wiring, and then call the same driving ports the HTTP handlers
// call. Nothing here constructs a service by hand or substitutes a fake for
// a component the production path uses: the point of an end-to-end test is
// to catch what unit tests structurally cannot — a mis-wired dependency, a
// contract two packages disagree about, an ordering assumption that only
// breaks once everything is connected. A harness that assembles its own
// object graph would test the harness.
//
// The adapters are the memory and simulated ones, which is not a compromise
// here: they are first-class runtime modes (docs/adr/0007), and the
// simulated estate is the only one that lets a test assert that executing a
// change actually reduced the estate's cost.
//
// Traceability: REQ-TEST-004, SPEC-TEST-002.
package e2e_test

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

// newApp builds a zero-infrastructure application: memory storage, the
// simulated estate, the deterministic model provider, an in-process bus.
func newApp(t *testing.T) *app.App {
	t.Helper()

	cfg := config.Defaults()
	cfg.Environment = "test"
	cfg.Storage = config.StorageMemory
	cfg.Cache = config.CacheMemory
	cfg.Events.Kind = config.EventsInProcess
	cfg.AWS.Mode = config.AWSModeSimulated
	cfg.LLM.Provider = config.LLMProviderScripted
	cfg.Features.AutonomousExecution = true
	// Metrics off: telemetry.NewMetrics registers collectors on a fresh
	// registry per call, which is fine, but nothing here scrapes them and a
	// test that builds several apps would pay for the registration each time.
	cfg.Telemetry.MetricsEnabled = false
	require.NoError(t, cfg.Validate())

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	a, err := app.Build(context.Background(), cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// tenantCtx returns a context carrying a tenant-admin principal for tenant.
// Every service call in these tests goes through it, so the tenant guard is
// exercised on the same path a real request takes rather than bypassed by a
// system principal.
func tenantCtx(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject:  "e2e@shopfleet.example",
		TenantID: tenant,
		Email:    "e2e@shopfleet.example",
		Roles:    []core.Role{core.RoleTenantAdmin},
		IssuedAt: time.Now().UTC(),
	})
}

// seed builds an application and runs the demo seed, returning both.
func seed(t *testing.T) (*app.App, *app.SeedResult) {
	t.Helper()
	a := newApp(t)
	result, err := app.Seed(context.Background(), a)
	require.NoError(t, err)
	require.False(t, result.AlreadyRan, "a freshly built app must seed rather than report an existing tenant")
	return a, result
}
