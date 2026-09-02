package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/notify"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Worker names of the seven background cycles. They are the values
// cmd/cloudoptix-worker's --workers flag accepts.
const (
	WorkerDiscovery    = "discovery"
	WorkerCost         = "cost"
	WorkerOptimization = "optimization"
	WorkerAutomation   = "automation"
	WorkerValidation   = "validation"
	WorkerNotification = "notification"
	WorkerLearning     = "learning"
)

// AllWorkers is every worker name, in the order they appear in a default
// run's logs.
func AllWorkers() []string {
	return []string{
		WorkerDiscovery, WorkerCost, WorkerOptimization,
		WorkerAutomation, WorkerValidation, WorkerNotification, WorkerLearning,
	}
}

// Worker is one background cycle: a ticker loop that leases work, iterates
// the tenants it is responsible for, and does one unit of work per tenant.
//
// KEY DESIGN DECISION — the lease is taken per tenant per cycle, not per
// process. A process-wide lease would serialise every tenant behind one
// worker replica, which defeats the point of running several; a per-tenant
// lease lets replica A work tenant 1 while replica B works tenant 2, and
// still guarantees no tenant is ever worked twice concurrently. The lease
// TTL is deliberately longer than the cycle interval so a slow cycle keeps
// its lease rather than having a second replica start the same work
// alongside it.
type Worker struct {
	Name string
	// Interval is the base period between cycles. Actual sleeps are
	// jittered (see jitter) so N replicas starting together do not
	// synchronise into a thundering herd against the database and AWS.
	Interval time.Duration
	// LeaseTTL bounds how long the per-tenant lock is held. A crashed
	// replica's lease expires rather than wedging that tenant forever.
	LeaseTTL time.Duration
	// PerTenant does one tenant's work for one cycle.
	PerTenant func(ctx context.Context, tenant core.TenantID) error
	// Global, when set, runs once per cycle instead of per tenant — the
	// notification dispatcher and approval expiry are process-wide claims
	// against a shared queue, not per-tenant scans.
	Global func(ctx context.Context) error

	app    *App
	logger *slog.Logger
	// runs and failures are exposed for tests and for the /metrics gauge.
	mu       sync.Mutex
	runs     int
	failures int
}

// Stats reports how many cycles this worker completed and how many raised an
// error, for tests and diagnostics.
func (w *Worker) Stats() (runs, failures int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.runs, w.failures
}

// RunOnce executes exactly one cycle synchronously. Run is a loop around it;
// tests call it directly so a worker's behaviour can be asserted without
// waiting on a ticker.
func (w *Worker) RunOnce(ctx context.Context) error {
	defer w.recoverPanic()

	if w.Global != nil {
		return w.leased(ctx, "worker:"+w.Name+":global", func(ctx context.Context) error {
			return w.Global(ctx)
		})
	}

	tenants, err := w.app.activeTenants(ctx)
	if err != nil {
		return fmt.Errorf("worker %s: listing tenants: %w", w.Name, err)
	}
	var errs []error
	for _, t := range tenants {
		// One tenant's failure must not stop the rest: a worker that
		// abandons the cycle on the first error would let one broken tenant
		// starve every other tenant of discovery, costing, and optimization
		// indefinitely.
		if err := w.runTenant(ctx, t); err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: %w", t, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) runTenant(ctx context.Context, tenant core.TenantID) (err error) {
	// Per-tenant panic recovery, inside the per-tenant loop, so a rule that
	// panics on one tenant's data does not skip every tenant after it.
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("worker panic recovered",
				slog.String("worker", w.Name), slog.String("tenant", string(tenant)),
				slog.Any("panic", r), slog.String("stack", string(debug.Stack())))
			err = fmt.Errorf("worker %s panicked on tenant %s: %v", w.Name, tenant, r)
		}
	}()

	tctx := core.WithPrincipal(ctx, core.SystemPrincipal(tenant, "worker/"+w.Name))
	return w.leased(tctx, fmt.Sprintf("worker:%s:%s", w.Name, tenant), func(ctx context.Context) error {
		return w.PerTenant(ctx, tenant)
	})
}

// leased runs fn under the distributed lock at key, treating a held lock as
// success-with-nothing-to-do rather than an error: another replica is
// already working it, which is the system behaving correctly.
func (w *Worker) leased(ctx context.Context, key string, fn func(context.Context) error) error {
	release, err := w.app.Locker.Acquire(ctx, key, w.LeaseTTL)
	if err != nil {
		if errors.Is(err, core.ErrConflict) {
			w.logger.Debug("lease held elsewhere, skipping",
				slog.String("worker", w.Name), slog.String("key", key))
			return nil
		}
		return fmt.Errorf("acquiring lease %q: %w", key, err)
	}
	defer release()
	return fn(ctx)
}

func (w *Worker) recoverPanic() {
	if r := recover(); r != nil {
		w.logger.Error("worker cycle panic recovered",
			slog.String("worker", w.Name), slog.Any("panic", r),
			slog.String("stack", string(debug.Stack())))
	}
}

// Run loops until ctx is cancelled, then returns. It never returns an error:
// a background worker's job is to keep working, and a cycle failure is a
// logged, counted event rather than a reason to stop the process.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("worker started",
		slog.String("worker", w.Name), slog.Duration("interval", w.Interval))

	// A jittered first sleep, not an immediate first cycle: N replicas
	// rolled out together would otherwise all run their first discovery scan
	// in the same second against the same AWS account.
	timer := time.NewTimer(jitter(w.Interval))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("worker draining", slog.String("worker", w.Name))
			return
		case <-timer.C:
		}

		started := time.Now()
		err := w.RunOnce(ctx)

		w.mu.Lock()
		w.runs++
		if err != nil {
			w.failures++
		}
		runs, failures := w.runs, w.failures
		w.mu.Unlock()

		if err != nil && ctx.Err() == nil {
			w.logger.Error("worker cycle failed",
				slog.String("worker", w.Name), slog.String("error", err.Error()),
				slog.Int("runs", runs), slog.Int("failures", failures))
		} else if err == nil {
			w.logger.Debug("worker cycle complete",
				slog.String("worker", w.Name), slog.Duration("took", time.Since(started)),
				slog.Int("runs", runs))
		}
		if w.app.Metrics != nil {
			w.app.Metrics.QueueDepth.WithLabelValues(w.Name).Set(float64(failures))
		}

		timer.Reset(jitter(w.Interval))
	}
}

// jitter spreads a base interval over ±25%, which is enough to decorrelate
// replicas without making the effective cadence unpredictable to an
// operator reading the logs.
func jitter(base time.Duration) time.Duration {
	if base <= 0 {
		base = time.Minute
	}
	spread := float64(base) * 0.25
	return time.Duration(float64(base) - spread + rand.Float64()*2*spread)
}

// activeTenants lists the tenants this process is responsible for. A worker
// iterating tenants rather than a queue is the right shape here because
// every cycle is a full-tenant scan (discover the estate, reprice it,
// re-analyze it), not a stream of discrete jobs.
func (a *App) activeTenants(ctx context.Context) ([]core.TenantID, error) {
	// Enumerating every tenant is a cross-tenant read, which
	// TenantRepository.List (correctly) restricts to a platform admin. This
	// is the one operation a worker performs outside any tenant's scope, so
	// it is the one place that role is granted — the per-tenant work below
	// runs under core.SystemPrincipal scoped to a single tenant, and
	// core.Principal.Can deliberately refuses PermExecutionStart to a
	// platform admin, so this wider identity cannot be reused to execute
	// anything.
	enumerator := core.SystemPrincipal("", "worker")
	enumerator.Roles = append(enumerator.Roles, core.RolePlatformAdmin)
	sysCtx := core.WithPrincipal(ctx, enumerator)
	page, err := a.Repositories.Tenants.List(sysCtx, ports.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	out := make([]core.TenantID, 0, len(page.Items))
	for _, t := range page.Items {
		if t.State == tenancy.StateActive {
			out = append(out, t.ID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// buildWorkers registers every background cycle. Registration is
// unconditional; selection is cmd/cloudoptix-worker's job, so a worker that
// a given deployment does not run still exists and is still testable.
func buildWorkers(cfg *config.Config, app *App, logger *slog.Logger) map[string]*Worker {
	svcs := app.services
	lease := cfg.Worker.LockTTL
	if lease <= 0 {
		lease = 5 * time.Minute
	}

	mk := func(name string, interval time.Duration, per func(context.Context, core.TenantID) error) *Worker {
		return &Worker{
			Name: name, Interval: interval, LeaseTTL: lease, PerTenant: per,
			app: app, logger: logger.With(slog.String("component", "worker")),
		}
	}

	workers := map[string]*Worker{}

	// Discovery: a full estate scan, inventory plus metrics. Six hours is
	// the cadence a real estate justifies — resources appear and disappear
	// on a scale of hours, and a scan is the most expensive thing CloudOptix
	// does to a customer's AWS API quota.
	workers[WorkerDiscovery] = mk(WorkerDiscovery, 6*time.Hour,
		func(ctx context.Context, tenant core.TenantID) error {
			run, err := app.Services.Discovery.Run(ctx, tenant, ports.DiscoveryRequest{
				Trigger: "scheduled", IncludeMetrics: true,
			})
			if err != nil {
				return err
			}
			// Discovered/updated/removed are logged separately because on
			// every scan after the first, "discovered" is legitimately zero
			// — the upsert is idempotent on Resource.Key() — and a single
			// "resources=0" line reads like a failed scan when it is a
			// steady-state one.
			logger.Info("scheduled discovery complete",
				slog.String("tenant", string(tenant)), slog.String("state", run.State),
				slog.Int("discovered", run.ResourcesDiscovered),
				slog.Int("updated", run.ResourcesUpdated),
				slog.Int("removed", run.ResourcesRemoved),
				slog.Float64("coverage", run.Coverage))
			// The twin is a projection of the inventory; rebuilding it here
			// rather than lazily on the next Graph request means the first
			// user to open the dashboard after a scan does not pay for it.
			if _, err := app.Services.Twin.Rebuild(ctx, tenant); err != nil {
				return fmt.Errorf("rebuilding the twin after discovery: %w", err)
			}
			return nil
		})

	// Cost: AWS publishes billing data with hours of lag and revises it, so
	// re-ingesting a trailing window several times a day is what keeps the
	// figures current without pretending they are real-time.
	workers[WorkerCost] = mk(WorkerCost, 4*time.Hour,
		func(ctx context.Context, tenant core.TenantID) error {
			accounts, err := app.Repositories.AWSAccounts.List(ctx, tenant)
			if err != nil {
				return err
			}
			period := core.PeriodOfDays(time.Now().UTC(), 7)
			var errs []error
			for _, acct := range accounts {
				res, err := app.Services.Costs.Ingest(ctx, tenant, acct.ID, period)
				if err != nil {
					errs = append(errs, fmt.Errorf("account %s: %w", acct.AccountID, err))
					continue
				}
				logger.Debug("cost ingested", slog.String("tenant", string(tenant)),
					slog.String("account", string(acct.AccountID)), slog.Int("records", res.RecordsIngested))
			}
			if _, err := app.Services.Costs.DetectAnomalies(ctx, tenant, core.PeriodOfDays(time.Now().UTC(), 30)); err != nil {
				errs = append(errs, fmt.Errorf("anomaly detection: %w", err))
			}
			if _, err := app.Services.Economics.Compute(ctx, tenant, period); err != nil {
				errs = append(errs, fmt.Errorf("economics: %w", err))
			}
			return errors.Join(errs...)
		})

	// Optimization: re-run the rule set and re-evaluate cost SLOs. Hourly,
	// because a recommendation set that lags a change by six hours is one
	// people stop trusting.
	workers[WorkerOptimization] = mk(WorkerOptimization, time.Hour,
		func(ctx context.Context, tenant core.TenantID) error {
			res, err := app.Services.Optimization.Analyze(ctx, tenant, ports.AnalyzeRequest{})
			if err != nil {
				return err
			}
			logger.Info("optimization analysis complete",
				slog.String("tenant", string(tenant)),
				slog.Int("recommendations", res.RecommendationsCreated),
				slog.String("monthly_saving", res.TotalMonthlySaving.String()))
			if _, err := app.Services.Economics.EvaluateSLOs(ctx, tenant); err != nil {
				return fmt.Errorf("evaluating cost SLOs: %w", err)
			}
			return nil
		})

	// Automation: the autonomous cycle. It is gated on the feature flag as
	// well as on policy and the tenant's own specification, so disabling the
	// flag stops the loop entirely rather than relying on every policy
	// evaluation to say no.
	workers[WorkerAutomation] = mk(WorkerAutomation, 15*time.Minute,
		func(ctx context.Context, tenant core.TenantID) error {
			if !cfg.Features.AutonomousExecution {
				return nil
			}
			res, err := app.Services.Automation.ProcessAutonomous(ctx, tenant)
			if err != nil {
				return err
			}
			if res.Executed > 0 || res.Failed > 0 || res.RolledBack > 0 {
				logger.Info("autonomous cycle complete",
					slog.String("tenant", string(tenant)),
					slog.Int("considered", res.Considered), slog.Int("executed", res.Executed),
					slog.Int("failed", res.Failed), slog.Int("rolled_back", res.RolledBack),
					slog.String("monthly_saving", res.MonthlySaving.String()))
			}
			return nil
		})

	// Validation: plans whose observation window has closed.
	// ExecutionRepository.ClaimPlansAwaitingValidation is deliberately
	// cross-tenant — it models one sweeper draining a shared table under
	// SKIP LOCKED — so this worker is Global rather than per-tenant. Wrapping
	// a cross-tenant claim in a per-tenant loop would claim other tenants'
	// plans while holding tenant A's lease and then validate them under
	// tenant A's principal, which the tenant guard would (correctly) reject
	// halfway through.
	validationWorker := &Worker{
		Name: WorkerValidation, Interval: 10 * time.Minute, LeaseTTL: lease,
		app: app, logger: logger.With(slog.String("component", "worker")),
		Global: func(ctx context.Context) error {
			return app.validateDuePlans(ctx, cfg.Worker.ExecutionConcurrency)
		},
	}
	workers[WorkerValidation] = validationWorker

	// Notification: a process-wide queue drain plus approval expiry. Both
	// claim from shared tables rather than scanning per tenant, so this
	// worker is Global.
	dispatcher := notify.NewDispatcher(notify.Deps{
		Specs:         app.Repositories.Specs,
		Notifications: app.Repositories.Notifications,
		Notifiers:     map[string]ports.Notifier{},
		Clock:         core.SystemClock{},
		Logger:        logger,
	})
	notifWorker := &Worker{
		Name: WorkerNotification, Interval: cfg.Worker.PollInterval, LeaseTTL: lease,
		app: app, logger: logger.With(slog.String("component", "worker")),
		Global: func(ctx context.Context) error {
			sysCtx := core.WithPrincipal(ctx, core.SystemPrincipal("", "worker/notification"))
			var errs []error
			batch := cfg.Worker.NotificationBatchSize
			if batch <= 0 {
				batch = 50
			}
			sent, failed, err := dispatcher.SendPending(sysCtx, "worker/notification", batch)
			if err != nil {
				errs = append(errs, fmt.Errorf("draining the notification queue: %w", err))
			} else if sent > 0 || failed > 0 {
				logger.Info("notifications dispatched",
					slog.Int("sent", sent), slog.Int("failed", failed))
			}
			if n, err := svcs.governance.ExpireOverdueApprovals(sysCtx); err != nil {
				errs = append(errs, fmt.Errorf("expiring overdue approvals: %w", err))
			} else if n > 0 {
				logger.Info("expired overdue approval requests", slog.Int("count", n))
			}
			return errors.Join(errs...)
		},
	}
	if notifWorker.Interval <= 0 {
		notifWorker.Interval = 30 * time.Second
	}
	workers[WorkerNotification] = notifWorker

	// Learning: recompute rule calibrations from observed outcomes. Daily,
	// because a calibration recomputed hourly would swing on a handful of
	// new outcomes and make confidence scores look noisy rather than
	// improving.
	workers[WorkerLearning] = mk(WorkerLearning, 24*time.Hour,
		func(ctx context.Context, tenant core.TenantID) error {
			if !cfg.Features.SavingsLearning {
				return nil
			}
			res, err := svcs.learning.Recalibrate(ctx, tenant)
			if err != nil {
				return err
			}
			if res.RulesCalibrated > 0 {
				logger.Info("rule calibration updated",
					slog.String("tenant", string(tenant)),
					slog.Int("rules", res.RulesCalibrated),
					slog.Int("outcomes", res.OutcomesConsidered),
					slog.Float64("mean_accuracy", res.MeanAccuracy))
			}
			return nil
		})

	return workers
}

// validateDuePlans validates every executed plan whose observation window has
// closed, across every tenant.
//
// The claim is what makes this safe to run on several replicas: the
// repository moves each claimed plan to PlanValidating inside the same
// atomic step that selects it, so a second replica's claim set is disjoint
// from the first's. Each plan is then validated under a principal scoped to
// that plan's own tenant, minted per plan rather than once for the batch —
// a single system principal spanning tenants would be exactly the
// cross-tenant authority core.GuardTenant exists to make impossible.
func (a *App) validateDuePlans(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 4
	}
	sysCtx := core.WithPrincipal(ctx, core.SystemPrincipal("", "worker/validation"))
	plans, err := a.Repositories.Executions.ClaimPlansAwaitingValidation(
		sysCtx, time.Now().UTC(), "worker/validation", limit)
	if err != nil {
		return fmt.Errorf("claiming plans awaiting validation: %w", err)
	}
	var errs []error
	for _, plan := range plans {
		tctx := core.WithPrincipal(ctx, core.SystemPrincipal(plan.TenantID, "worker/validation"))
		result, err := a.Services.Automation.Validate(tctx, plan.TenantID, plan.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("plan %s: %w", plan.ID, err))
			continue
		}
		a.Logger.Info("post-execution validation complete",
			slog.String("tenant", string(plan.TenantID)), slog.String("plan", string(plan.ID)),
			slog.String("verdict", string(result.Verdict)),
			slog.Bool("rolled_back", result.RollbackTriggered))
	}
	return errors.Join(errs...)
}

// SelectWorkers resolves a comma-separated worker list to the workers to
// run, rejecting an unknown name rather than silently running fewer workers
// than the operator asked for.
func (a *App) SelectWorkers(names string) ([]*Worker, error) {
	requested := AllWorkers()
	if trimmed := strings.TrimSpace(names); trimmed != "" && trimmed != "all" {
		requested = nil
		for _, n := range strings.Split(trimmed, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			requested = append(requested, n)
		}
	}
	out := make([]*Worker, 0, len(requested))
	for _, n := range requested {
		w, ok := a.Workers[n]
		if !ok {
			return nil, fmt.Errorf("unknown worker %q (known: %s)", n, strings.Join(AllWorkers(), ", "))
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no workers selected")
	}
	return out, nil
}

// RunWorkers runs every selected worker until ctx is cancelled, then waits
// for all of them to drain. Draining rather than exiting immediately is what
// makes a rolling deployment safe: a worker killed mid-execution would leave
// a plan in the executing state with a lease nobody will release until it
// expires.
func RunWorkers(ctx context.Context, workers []*Worker) {
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w *Worker) {
			defer wg.Done()
			w.Run(ctx)
		}(w)
	}
	wg.Wait()
}
