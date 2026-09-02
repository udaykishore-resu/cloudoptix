package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Service implements ports.DiscoveryService.
type Service struct {
	Repos            ports.Repositories
	Broker           ports.AWSCredentialBroker
	Discoverers      []ports.ResourceDiscoverer
	MetricCollectors []ports.MetricCollector
	CostIngestors    []ports.CostIngestor
	Events           ports.EventPublisher
	Locker           ports.Locker
	Clock            core.Clock

	// MaxConcurrency bounds how many (service × region) jobs run at once
	// across every account a single Run call covers. Defaults to 8.
	MaxConcurrency int
	// MaxRetries bounds attempts per job before it is recorded as failed.
	// Defaults to 4.
	MaxRetries int
	// BaseBackoff and MaxBackoff bound the exponential-backoff-with-jitter
	// delay between retries. Defaults to 250ms and 15s.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// LockTTL bounds how long a per-account discovery lock is held, so a
	// crashed worker cannot wedge an account's scans forever. Defaults to 20
	// minutes.
	LockTTL time.Duration
	// MetricWindowDays and CostWindowDays bound the optional metrics/cost
	// collection a run performs when the request asks for it. Default to 14
	// and 7 — short windows, because discovery's job is inventory; the
	// utilization and costing packages own the deep historical analysis.
	MetricWindowDays int
	CostWindowDays   int
}

var _ ports.DiscoveryService = (*Service)(nil)

// NewService builds a Service with the platform default thresholds.
func NewService(repos ports.Repositories, broker ports.AWSCredentialBroker, discoverers []ports.ResourceDiscoverer, events ports.EventPublisher, locker ports.Locker) *Service {
	return &Service{
		Repos: repos, Broker: broker, Discoverers: discoverers, Events: events, Locker: locker,
		Clock: core.SystemClock{}, MaxConcurrency: 8, MaxRetries: 4,
		BaseBackoff: 250 * time.Millisecond, MaxBackoff: 15 * time.Second, LockTTL: 20 * time.Minute,
		MetricWindowDays: 14, CostWindowDays: 7,
	}
}

func (s *Service) clock() core.Clock {
	if s.Clock == nil {
		return core.SystemClock{}
	}
	return s.Clock
}

// Run scans one account (in.AccountID set) or every connected, read-scoped
// account the tenant has (in.AccountID zero), producing exactly one
// DiscoveryRun record — see the package doc for why a multi-account run
// still reports through a single record rather than orphaning one row per
// account. Errors reaching AWS (a denied assume-role, a throttled service, a
// missing permission) are captured on the run and never make Run itself
// return an error: a caller asking "did the estate get scanned" wants the
// run's State and Errors, not a Go error that discards everything else the
// run did succeed at.
func (s *Service) Run(ctx context.Context, tenant core.TenantID, in ports.DiscoveryRequest) (ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.DiscoveryRun{}, err
	}
	accounts, err := s.resolveAccounts(ctx, tenant, in.AccountID)
	if err != nil {
		return ports.DiscoveryRun{}, err
	}
	if len(accounts) == 0 {
		return ports.DiscoveryRun{}, core.Invalid("no connected AWS account with the read role granted is available to scan")
	}

	run := s.newRunRecord(tenant, accounts)
	run.Trigger = resolveTrigger(in.Trigger)
	if err := s.Repos.DiscoveryRuns.Create(ctx, run); err != nil {
		return ports.DiscoveryRun{}, err
	}
	s.publish(ctx, tenant, ports.EventDiscoveryStarted, run.ID, map[string]any{
		"account_id": string(run.AccountID), "accounts": len(accounts),
	})

	if in.Async {
		go func() {
			bg := core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "discovery"))
			final := run
			s.executeAccounts(bg, tenant, &final, accounts, in)
			s.finish(bg, tenant, &final)
		}()
		return run, nil
	}

	s.executeAccounts(ctx, tenant, &run, accounts, in)
	s.finish(ctx, tenant, &run)
	return run, nil
}

// Get delegates to the repository.
func (s *Service) Get(ctx context.Context, tenant core.TenantID, runID core.ID) (ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.DiscoveryRun{}, err
	}
	return s.Repos.DiscoveryRuns.Get(ctx, tenant, runID)
}

// ListRuns delegates to the repository.
func (s *Service) ListRuns(ctx context.Context, tenant core.TenantID, limit int) ([]ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	return s.Repos.DiscoveryRuns.ListRecent(ctx, tenant, limit)
}

// Status assembles the tenant-level discovery health summary: how much of
// the estate is covered, whether a scan is in flight, and — surfaced
// verbatim rather than buried in a log — every IAM action a recent run found
// itself missing, which is the actionable part of a failed connection.
func (s *Service) Status(ctx context.Context, tenant core.TenantID) (ports.DiscoveryStatus, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.DiscoveryStatus{}, err
	}
	runs, err := s.Repos.DiscoveryRuns.ListRecent(ctx, tenant, 10)
	if err != nil {
		return ports.DiscoveryStatus{}, err
	}
	accounts, err := s.Repos.AWSAccounts.List(ctx, tenant)
	if err != nil {
		accounts = nil
	}
	count, _ := s.Repos.Resources.Count(ctx, tenant, ports.ResourceFilter{})

	status := ports.DiscoveryStatus{ResourceCount: count, AccountsTotal: len(accounts), RecentRuns: runs}
	connected := map[core.AccountID]bool{}
	for _, a := range accounts {
		if a.State == cloud.ConnConnected || a.State == cloud.ConnDegraded {
			connected[a.AccountID] = true
		}
	}
	covered := map[core.AccountID]bool{}
	seenIssue := map[string]bool{}
	for _, r := range runs {
		if r.State == "running" {
			status.InProgress = true
		}
		if r.FinishedAt != nil && r.FinishedAt.After(status.LastRunAt) {
			status.LastRunAt = *r.FinishedAt
		}
		if (r.State == "completed" || r.State == "partial") && connected[r.AccountID] {
			covered[r.AccountID] = true
		}
		for _, sr := range r.ServiceResults {
			if sr.Succeeded || sr.MissingPermission == "" {
				continue
			}
			issue := fmt.Sprintf("%s/%s: missing %s", sr.Service, sr.Region, sr.MissingPermission)
			if !seenIssue[issue] {
				seenIssue[issue] = true
				status.PermissionIssues = append(status.PermissionIssues, issue)
			}
		}
	}
	status.AccountsCovered = len(covered)
	if status.AccountsTotal > 0 {
		status.Coverage = float64(status.AccountsCovered) / float64(status.AccountsTotal)
	}
	return status, nil
}

func (s *Service) newRunRecord(tenant core.TenantID, accounts []cloud.AWSAccount) ports.DiscoveryRun {
	run := ports.DiscoveryRun{
		ID: core.NewID("dscv"), TenantID: tenant, State: "running", StartedAt: s.clock().Now(),
	}
	if len(accounts) == 1 {
		run.AccountID = accounts[0].AccountID
	}
	return run
}

// resolveAccounts returns the one named account, or every account the
// tenant has that is connected (or degraded — missing an optional
// permission is not a reason to stop scanning what it does grant) and holds
// the read role, when accountID is zero.
func (s *Service) resolveAccounts(ctx context.Context, tenant core.TenantID, accountID core.ID) ([]cloud.AWSAccount, error) {
	if !accountID.IsZero() {
		a, err := s.Repos.AWSAccounts.Get(ctx, tenant, accountID)
		if err != nil {
			return nil, err
		}
		return []cloud.AWSAccount{a}, nil
	}
	all, err := s.Repos.AWSAccounts.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.AWSAccount, 0, len(all))
	for _, a := range all {
		if a.State != cloud.ConnConnected && a.State != cloud.ConnDegraded {
			continue
		}
		if !a.HasScope(cloud.ScopeRead) {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func resolveTrigger(t string) string {
	if t == "" {
		return "manual"
	}
	return t
}

// selectedDiscoverers returns every registered discoverer whose Service()
// code is named in services, or every discoverer when services is empty.
func (s *Service) selectedDiscoverers(services []string) []ports.ResourceDiscoverer {
	if len(services) == 0 {
		return s.Discoverers
	}
	want := make(map[string]bool, len(services))
	for _, svc := range services {
		want[svc] = true
	}
	out := make([]ports.ResourceDiscoverer, 0, len(s.Discoverers))
	for _, d := range s.Discoverers {
		if want[d.Service()] {
			out = append(out, d)
		}
	}
	return out
}

func (s *Service) acquireLock(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (func(), error) {
	if s.Locker == nil {
		return func() {}, nil
	}
	ttl := s.LockTTL
	if ttl <= 0 {
		ttl = 20 * time.Minute
	}
	key := fmt.Sprintf("discovery:%s:%s", tenant, accountID)
	release, err := s.Locker.Acquire(ctx, key, ttl)
	if err != nil {
		return nil, core.NewError(core.ErrConflict, "discovery_in_progress",
			"a discovery scan for account %s is already running", accountID).Wrap(err)
	}
	return release, nil
}

func (s *Service) publish(ctx context.Context, tenant core.TenantID, t ports.EventType, subject core.ID, payload map[string]any) {
	if s.Events == nil {
		return
	}
	_ = s.Events.Publish(ctx, ports.Event{
		ID: string(core.NewID("evt")), Type: t, TenantID: tenant, SubjectID: subject,
		OccurredAt: s.clock().Now(), Actor: "cloudoptix/discovery", Payload: payload,
	})
}

// finish seals a run's timing, persists its final state, appends the audit
// record, and publishes completion — the three consequences every run must
// have regardless of whether it succeeded, partially succeeded, or failed
// outright.
func (s *Service) finish(ctx context.Context, tenant core.TenantID, run *ports.DiscoveryRun) {
	now := s.clock().Now()
	run.FinishedAt = &now
	run.DurationMS = now.Sub(run.StartedAt).Milliseconds()
	_ = s.Repos.DiscoveryRuns.Update(ctx, *run)

	outcome, action := audit.OutcomeSuccess, audit.ActionDiscoveryCompleted
	switch run.State {
	case "failed":
		outcome, action = audit.OutcomeFailure, audit.ActionDiscoveryFailed
	case "partial":
		outcome = audit.OutcomePartial
	}
	_, _ = s.Repos.Audit.Append(ctx, audit.Record{
		TenantID: tenant, Action: action, Outcome: outcome, Actor: "cloudoptix/discovery", ActorMachine: true,
		SubjectKind: "discovery_run", SubjectID: run.ID, AWSAccountID: run.AccountID,
		Message: fmt.Sprintf("discovery run %s finished %s: %d discovered, %d updated, %d removed, coverage %.0f%%",
			run.ID, run.State, run.ResourcesDiscovered, run.ResourcesUpdated, run.ResourcesRemoved, run.Coverage*100),
		At: now,
	})
	s.publish(ctx, tenant, ports.EventDiscoveryCompleted, run.ID, map[string]any{
		"state": run.State, "account_id": string(run.AccountID), "coverage": run.Coverage,
	})
}
