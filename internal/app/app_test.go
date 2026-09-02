package app_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
)

// TestBuildWithZeroConfiguration is the composition root's own contract: the
// configuration a process gets when nobody has configured anything must
// produce a working platform.
//
// It is not a convenience test. "One command, no infrastructure" is a claim
// deployments/docker-compose.minimal.yml and `coptx demo` both rest on, and
// the way it breaks is not a compile error — it is a new required setting
// added to config with no default, or an adapter that quietly assumes a
// database. Both pass every other test in the repository and fail here.
func TestBuildWithZeroConfiguration(t *testing.T) {
	cfg := config.Defaults()
	// The one departure from the literal defaults: environment "test" rather
	// than "development", which only relaxes the database-password
	// requirement that the memory path does not use anyway.
	cfg.Environment = "test"
	require.NoError(t, cfg.Validate(), "the default configuration must be valid on its own")

	assert.Equal(t, config.StorageMemory, cfg.Storage)
	assert.Equal(t, config.CacheMemory, cfg.Cache)
	assert.Equal(t, config.EventsInProcess, cfg.Events.Kind)
	assert.Equal(t, config.AWSModeSimulated, cfg.AWS.Mode)
	assert.Equal(t, config.LLMProviderScripted, cfg.LLM.Provider)

	a, err := app.Build(context.Background(), cfg, quietLogger())
	require.NoError(t, err, "Build must succeed with no configuration and no infrastructure")
	t.Cleanup(func() { _ = a.Close() })

	assert.NotNil(t, a.Router)
	assert.NotNil(t, a.Health)
	assert.NotNil(t, a.Estate, "simulated mode must produce an estate")
	assert.NotNil(t, a.UnitOfWork)
	assert.Len(t, a.Workers, len(app.AllWorkers()), "every worker must be registered")

	// Every driving port must be populated. A nil service here is a wiring
	// omission that would surface as a nil-pointer panic on the first
	// request to whichever surface it serves.
	s := a.Services
	for name, svc := range map[string]any{
		"onboarding": s.Onboarding, "specs": s.Specs, "aws_accounts": s.AWSAccounts,
		"discovery": s.Discovery, "twin": s.Twin, "costs": s.Costs,
		"economics": s.Economics, "optimization": s.Optimization, "simulation": s.Simulation,
		"governance": s.Governance, "automation": s.Automation, "copilot": s.Copilot,
		"audit": s.Audit, "tenants": s.Tenants,
	} {
		assert.NotNil(t, svc, "ports.Services.%s is nil", name)
	}

	// Likewise every repository.
	r := a.Repositories
	for name, repo := range map[string]any{
		"tenants": r.Tenants, "users": r.Users, "specs": r.Specs, "aws_accounts": r.AWSAccounts,
		"resources": r.Resources, "applications": r.Applications, "costs": r.Costs,
		"metrics": r.Metrics, "recommendations": r.Recommendations, "policies": r.Policies,
		"approvals": r.Approvals, "executions": r.Executions, "savings": r.Savings,
		"economics": r.Economics, "simulations": r.Simulations, "audit": r.Audit,
		"discovery_runs": r.DiscoveryRuns, "conversations": r.Conversations,
		"notifications": r.Notifications,
	} {
		assert.NotNil(t, repo, "ports.Repositories.%s is nil", name)
	}
}

// TestSelectWorkers checks the flag parsing cmd/cloudoptix-worker relies on,
// including that an unknown name is refused rather than silently reducing
// the set of cycles a deployment runs.
func TestSelectWorkers(t *testing.T) {
	cfg := config.Defaults()
	cfg.Environment = "test"
	a, err := app.Build(context.Background(), cfg, quietLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })

	all, err := a.SelectWorkers("all")
	require.NoError(t, err)
	assert.Len(t, all, len(app.AllWorkers()))

	empty, err := a.SelectWorkers("")
	require.NoError(t, err, "an empty selection means all, matching the flag's default")
	assert.Len(t, empty, len(app.AllWorkers()))

	subset, err := a.SelectWorkers("discovery, cost ,optimization")
	require.NoError(t, err, "whitespace around a name must not make it unknown")
	require.Len(t, subset, 3)
	assert.Equal(t, app.WorkerDiscovery, subset[0].Name)

	_, err = a.SelectWorkers("discovery,typo")
	require.Error(t, err, "an unknown worker name must be refused, not skipped")
	assert.Contains(t, err.Error(), "typo")
	assert.Contains(t, err.Error(), app.WorkerNotification, "the error must list what is valid")
}

// TestSeedIsIdempotent runs the demo seed twice against one application.
func TestSeedIsIdempotent(t *testing.T) {
	cfg := config.Defaults()
	cfg.Environment = "test"
	a, err := app.Build(context.Background(), cfg, quietLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })

	first, err := app.Seed(context.Background(), a)
	require.NoError(t, err)
	assert.False(t, first.AlreadyRan)
	assert.Greater(t, first.ResourcesDiscovered, 0)
	assert.Greater(t, first.Recommendations, 0)

	second, err := app.Seed(context.Background(), a)
	require.NoError(t, err)
	assert.True(t, second.AlreadyRan)
	assert.Equal(t, first.TenantID, second.TenantID)
	assert.Equal(t, first.ResourcesDiscovered, second.ResourcesDiscovered,
		"the second seed must report the existing estate, not re-discover it into a doubled count")

	// The printed summary is the demo's actual deliverable, so it is
	// exercised rather than left to break silently at demo time.
	first.PrintSummary(os.Stdout)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
