package app

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/server"
)

// probeTimeout bounds every readiness probe. It is deliberately shorter than
// a typical Kubernetes readinessProbe timeoutSeconds (1s vs the usual 3-5s)
// so that the endpoint answers within the orchestrator's budget even when
// every dependency is hanging — a readiness endpoint that itself times out
// tells the orchestrator nothing.
const probeTimeout = time.Second

// buildHealth assembles the liveness and readiness check sets.
//
// The split is the important part and is easy to get wrong: liveness answers
// "should this pod be restarted", readiness answers "should it receive
// traffic". Putting a database ping in liveness — the common mistake — means
// a database outage restarts every API pod, turning a recoverable dependency
// failure into a cluster-wide crash loop that also destroys the in-flight
// work that might have survived it. So liveness here checks only things
// internal to the process, and every external dependency is readiness.
func buildHealth(cfg *config.Config, app *App, storage *storageSet, cache *cacheSet) *server.Health {
	version := cfg.Telemetry.ServiceVersion
	if version == "" {
		version = "dev"
	}
	serviceName := cfg.Telemetry.ServiceName
	if serviceName == "" {
		serviceName = "cloudoptix"
	}

	liveness := []server.NamedCheck{
		{Name: "process", Check: func(context.Context) error { return nil }},
		// The rule pack is embedded and parsed at startup; a process whose
		// registry came up empty would answer every optimization request
		// with "no findings" rather than an error, which is the worst
		// possible failure mode — silently correct-looking.
		{Name: "rule_registry", Check: func(ctx context.Context) error {
			rules, err := app.Services.Optimization.ListRules(ctx, DemoTenantID)
			if err == nil && len(rules) == 0 {
				return fmt.Errorf("optimization rule registry is empty")
			}
			return nil
		}},
	}

	readiness := []server.NamedCheck{
		server.CheckWithTimeout("storage", probeTimeout, storage.probe),
		server.CheckWithTimeout("cache", probeTimeout, cache.probe),
		server.CheckWithTimeout("llm", probeTimeout, func(ctx context.Context) error {
			// The provider is the fallback-wrapped chain, so "unhealthy"
			// here means even the deterministic path is gone, which would be
			// a programming error rather than a provider outage — a real
			// outage degrades to deterministic answers and stays ready.
			if !app.LLM.Healthy(ctx) {
				return fmt.Errorf("llm provider %s reports unhealthy", app.LLM.Name())
			}
			return nil
		}),
	}

	if app.Estate != nil {
		readiness = append(readiness, server.CheckWithTimeout("aws_simulator", probeTimeout,
			func(context.Context) error {
				if app.Estate.TotalMonthlyCost().IsZero() {
					return fmt.Errorf("simulated estate is empty")
				}
				return nil
			}))
	}

	return server.NewHealth(serviceName, version, liveness, readiness)
}
