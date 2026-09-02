package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/application/auditsvc"
	"github.com/udaykishore-resu/cloudoptix/internal/application/automation"
	"github.com/udaykishore-resu/cloudoptix/internal/application/awsaccounts"
	"github.com/udaykishore-resu/cloudoptix/internal/application/copilot"
	"github.com/udaykishore-resu/cloudoptix/internal/application/costing"
	"github.com/udaykishore-resu/cloudoptix/internal/application/discovery"
	"github.com/udaykishore-resu/cloudoptix/internal/application/economics"
	"github.com/udaykishore-resu/cloudoptix/internal/application/governance"
	"github.com/udaykishore-resu/cloudoptix/internal/application/learning"
	"github.com/udaykishore-resu/cloudoptix/internal/application/onboarding"
	"github.com/udaykishore-resu/cloudoptix/internal/application/optimization"
	"github.com/udaykishore-resu/cloudoptix/internal/application/simulation"
	"github.com/udaykishore-resu/cloudoptix/internal/application/specsvc"
	"github.com/udaykishore-resu/cloudoptix/internal/application/tenants"
	"github.com/udaykishore-resu/cloudoptix/internal/application/twin"
	"github.com/udaykishore-resu/cloudoptix/internal/application/utilization"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// serviceSet carries the concrete service values alongside the ports.Services
// bundle. The workers need concrete types for a few methods the driving
// ports deliberately do not expose — governance.Service.ExpireOverdueApprovals
// and learning.Service.Recalibrate among them, which are worker entry points,
// not API operations — so the composition root keeps both views rather than
// widening the ports to suit one caller.
type serviceSet struct {
	onboarding   *onboarding.Service
	specs        *specsvc.Service
	awsAccounts  *awsaccounts.Service
	discovery    *discovery.Service
	twin         *twin.Service
	costs        *costing.Service
	economics    *economics.Service
	optimization *optimization.Service
	simulation   *simulation.Service
	governance   *governance.Service
	automation   *automation.Service
	learning     *learning.Service
	copilot      *copilot.Service
	audit        *auditsvc.Service
	tenants      *tenants.Service
	utilization  *utilization.Collector
}

// services is stashed on App so workers.go and seed.go can reach the
// concrete values without the ports bundle growing worker-only methods.
var _ = (*serviceSet)(nil)

// buildServices assembles every application service.
//
// Construction order follows the dependency graph exactly once:
// governance is built before automation because automation takes a
// ports.GovernanceService; learning before automation because automation
// takes a Learner. Nothing else here depends on anything else here, which is
// why the rest is a flat sequence rather than a graph a reader has to hold
// in their head.
func buildServices(cfg *config.Config, app *App, aws *awsSet, logger *slog.Logger) (ports.Services, error) {
	repos := app.Repositories
	clock := core.SystemClock{}

	registry, err := optimization.NewDefaultRegistry(logger)
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: loading the optimization rule pack: %w", err)
	}

	disc := discovery.NewService(repos, aws.broker, aws.discoverers, app.Events, app.Locker)
	disc.MetricCollectors = aws.collectors
	disc.CostIngestors = aws.ingestors
	disc.MaxConcurrency = cfg.Worker.DiscoveryConcurrency

	costs := costing.NewService(repos, aws.broker, aws.ingestors, app.Events)
	econ := economics.NewService(repos)
	tw := twin.NewService(repos, app.Cache)

	opt, err := optimization.NewService(optimization.Deps{
		Resources:       repos.Resources,
		Metrics:         repos.Metrics,
		Costs:           repos.Costs,
		Recommendations: repos.Recommendations,
		Specs:           repos.Specs,
		Savings:         repos.Savings,
		Policies:        repos.Policies,
		Pricing:         app.Pricing,
		Registry:        registry,
		Events:          app.Events,
		Clock:           clock,
		Logger:          logger,
	})
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: building the optimization service: %w", err)
	}

	gov, err := governance.NewService(governance.Deps{
		Policies:        repos.Policies,
		Approvals:       repos.Approvals,
		Recommendations: repos.Recommendations,
		Resources:       repos.Resources,
		Specs:           repos.Specs,
		Audit:           repos.Audit,
		Economics:       repos.Economics,
		Events:          app.Events,
		Clock:           clock,
		Logger:          logger,
	})
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: building the governance service: %w", err)
	}

	learn, err := learning.NewService(learning.Deps{
		Savings:   repos.Savings,
		Knowledge: app.Knowledge,
		Clock:     clock,
		Logger:    logger,
	})
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: building the learning service: %w", err)
	}

	auto, err := automation.NewService(automation.Deps{
		Executions:      repos.Executions,
		Recommendations: repos.Recommendations,
		Resources:       repos.Resources,
		AWSAccounts:     repos.AWSAccounts,
		Policies:        repos.Policies,
		Approvals:       repos.Approvals,
		Savings:         repos.Savings,
		Specs:           repos.Specs,
		Audit:           repos.Audit,
		Metrics:         repos.Metrics,
		Costs:           repos.Costs,
		Credentials:     aws.broker,
		Executors:       aws.executors,
		Locker:          app.Locker,
		Governance:      gov,
		Events:          app.Events,
		Learner:         learn,
		Clock:           clock,
		Logger:          logger,
	})
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: building the automation service: %w", err)
	}

	specs, err := specsvc.NewService(specsvc.Deps{
		Specs: repos.Specs, Tenants: repos.Tenants, Audit: repos.Audit,
		UoW: app.UnitOfWork, Events: app.Events, Clock: clock, Logger: logger,
	})
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: building the spec service: %w", err)
	}

	accounts, err := awsaccounts.NewService(awsaccounts.Deps{
		Accounts: repos.AWSAccounts, Tenants: repos.Tenants, Specs: repos.Specs,
		Executions: repos.Executions, Audit: repos.Audit, Broker: aws.broker,
		Events: app.Events, Clock: clock, Logger: logger,
	})
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: building the AWS account service: %w", err)
	}

	aud, err := auditsvc.NewService(auditsvc.Deps{Audit: repos.Audit, Clock: clock, Logger: logger})
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: building the audit service: %w", err)
	}

	tnt, err := tenants.NewService(tenants.Deps{
		Tenants: repos.Tenants, Users: repos.Users, Audit: repos.Audit,
		Events: app.Events, Clock: clock, Logger: logger,
	})
	if err != nil {
		return ports.Services{}, fmt.Errorf("app: building the tenant service: %w", err)
	}

	sim := simulation.New(app.Pricing, repos.Resources, repos.Simulations)
	sim.Logger = logger

	onb := onboarding.New(app.UnitOfWork, app.LLM, app.Events)
	cop := copilot.New(app.UnitOfWork, app.LLM, app.Knowledge)

	app.services = &serviceSet{
		onboarding: onb, specs: specs, awsAccounts: accounts, discovery: disc,
		twin: tw, costs: costs, economics: econ, optimization: opt, simulation: sim,
		governance: gov, automation: auto, learning: learn, copilot: cop,
		audit: aud, tenants: tnt,
		utilization: utilization.NewCollector(aws.collectors, repos.Metrics),
	}

	return ports.Services{
		Onboarding:   onb,
		Specs:        specs,
		AWSAccounts:  accounts,
		Discovery:    disc,
		Twin:         tw,
		Costs:        costs,
		Economics:    econ,
		Optimization: opt,
		Simulation:   sim,
		Governance:   gov,
		Automation:   auto,
		Copilot:      cop,
		Audit:        aud,
		Tenants:      tnt,
	}, nil
}

// systemClock satisfies core.Clock for the infrastructure types that take
// one. It exists rather than core.SystemClock{} being passed inline only so
// this file names the choice once.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// userLookup adapts ports.UserRepository to auth.UserLookup. The two have an
// identical GetBySubject, so this is structurally unnecessary — it exists to
// give the auth package a value it can hold without the composition root
// handing it the whole repository, which would let a future auth change
// start writing users.
type userLookup struct{ users ports.UserRepository }

func (l userLookup) GetBySubject(ctx context.Context, subject string) (tenancy.User, error) {
	return l.users.GetBySubject(ctx, subject)
}

// devTokenRoles is the role set the local development static token carries.
// Tenant admin rather than platform admin: the dev token is a stand-in for a
// signed-in customer user, and giving it cross-tenant powers would make
// local development a worse rehearsal of production than it needs to be.
func devTokenRoles() []core.Role {
	return []core.Role{core.RoleTenantAdmin}
}
