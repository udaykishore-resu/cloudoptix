package costing

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Service implements ports.CostService.
type Service struct {
	Repos     ports.Repositories
	Broker    ports.AWSCredentialBroker
	Ingestors []ports.CostIngestor
	Events    ports.EventPublisher
	Clock     core.Clock

	// ForecastLookbackDays bounds how much daily history Forecast pulls
	// before selecting a method. Defaults to 60.
	ForecastLookbackDays int
	// AnomalyWindowDays bounds the trailing baseline window DetectAnomalies
	// builds its median/MAD estimator from. Defaults to 14.
	AnomalyWindowDays int
	// AnomalyZThreshold is the robust z-score magnitude that marks a point
	// anomalous. 3.5 is the threshold Iglewicz & Hoaglin recommend for the
	// median/MAD estimator; it is deliberately higher than the "3" a
	// mean-based z-score commonly uses because MAD is a tighter, less
	// forgiving measure of spread.
	AnomalyZThreshold float64
}

var _ ports.CostService = (*Service)(nil)

// NewService builds a Service with the platform default thresholds.
func NewService(repos ports.Repositories, broker ports.AWSCredentialBroker, ingestors []ports.CostIngestor, events ports.EventPublisher) *Service {
	return &Service{
		Repos: repos, Broker: broker, Ingestors: ingestors, Events: events,
		Clock: core.SystemClock{}, ForecastLookbackDays: 60, AnomalyWindowDays: 14, AnomalyZThreshold: 3.5,
	}
}

func (s *Service) clock() core.Clock {
	if s.Clock == nil {
		return core.SystemClock{}
	}
	return s.Clock
}

// Ingest pulls billed cost for one account and period, preferring the Cost &
// Usage Report over Cost Explorer, and joins each record to a discovered
// resource where an ARN allows it.
func (s *Service) Ingest(ctx context.Context, tenant core.TenantID, accountID core.ID, period core.Period) (ports.IngestResult, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.IngestResult{}, err
	}
	account, err := s.Repos.AWSAccounts.Get(ctx, tenant, accountID)
	if err != nil {
		return ports.IngestResult{}, err
	}
	session, err := s.Broker.Assume(ctx, account, cloud.ScopeAnalyze)
	if err != nil {
		return ports.IngestResult{}, core.NewError(core.ErrUnavailable, "assume_role_failed",
			"could not assume the analyze role for account %s", account.AccountID).Wrap(err)
	}
	return s.IngestWithSession(ctx, tenant, account, session, period)
}

// IngestWithSession runs the same ingestion pipeline as Ingest but takes an
// already-assumed session, so a caller that already has one open (the
// discovery orchestrator, running a combined inventory-and-cost scan) does
// not pay for a second AssumeRole call.
func (s *Service) IngestWithSession(ctx context.Context, tenant core.TenantID, account cloud.AWSAccount, session ports.AWSSession, period core.Period) (ports.IngestResult, error) {
	began := time.Now()
	ingestor := s.selectIngestor(ctx, session, account)
	if ingestor == nil {
		return ports.IngestResult{}, core.NewError(core.ErrUnavailable, "no_cost_source",
			"neither the Cost & Usage Report nor Cost Explorer is available for account %s", account.AccountID)
	}
	resourceLevel := ingestor.Source() == "cur"
	records, err := ingestor.Fetch(ctx, ports.CostIngestInput{
		TenantID: tenant, Session: session, Account: account, Period: period,
		Granularity: cost.GranularityDaily, Basis: cost.BasisAmortized, ResourceLevel: resourceLevel,
	})
	if err != nil {
		return ports.IngestResult{}, core.NewError(core.ErrUnavailable, "cost_fetch_failed",
			"fetching cost from %s failed", ingestor.Source()).Wrap(err)
	}

	resolved, coverage := s.resolveResourceIDs(ctx, tenant, account.AccountID, records)
	n, err := s.Repos.Costs.UpsertBatch(ctx, tenant, resolved)
	if err != nil {
		return ports.IngestResult{}, err
	}

	total := core.ZeroUSD()
	for _, r := range resolved {
		total = total.MustAdd(r.Amount)
	}

	return ports.IngestResult{
		RecordsIngested: n, Period: period, Source: ingestor.Source(),
		TotalCost: total, ResourceCoverage: coverage, DurationMS: time.Since(began).Milliseconds(),
	}, nil
}

// selectIngestor prefers CUR (resource-level, hourly) and falls back to Cost
// Explorer (service-level, daily) — see the package doc for why the
// preference exists and how the fallback is surfaced rather than hidden.
func (s *Service) selectIngestor(ctx context.Context, session ports.AWSSession, account cloud.AWSAccount) ports.CostIngestor {
	var cur, explorer, other ports.CostIngestor
	for _, ig := range s.Ingestors {
		if !ig.Available(ctx, session, account) {
			continue
		}
		switch ig.Source() {
		case "cur":
			if cur == nil {
				cur = ig
			}
		case "cost_explorer":
			if explorer == nil {
				explorer = ig
			}
		default:
			if other == nil {
				other = ig
			}
		}
	}
	switch {
	case cur != nil:
		return cur
	case explorer != nil:
		return explorer
	default:
		return other
	}
}

// resolveResourceIDs joins billing rows to discovered resources by ARN. Rows
// that cannot be joined are kept, not dropped — an unjoined charge is still
// real spend — and the returned coverage figure is the honest fraction of
// *attributable* charge types (see cost.ChargeType.Attributable) that
// resolved to a resource, which is what the ingestion result reports rather
// than silently assuming full attribution.
func (s *Service) resolveResourceIDs(ctx context.Context, tenant core.TenantID, accountID core.AccountID, records []cost.Record) ([]cost.Record, float64) {
	inv, err := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{AccountIDs: []core.AccountID{accountID}})
	out := make([]cost.Record, len(records))
	var attributable, joined int
	for i, r := range records {
		r.TenantID = tenant
		if r.ResourceID.IsZero() && r.ResourceARN != "" && inv != nil {
			if res, ok := inv.ByARN(r.ResourceARN); ok {
				r.ResourceID = res.ID
			}
		}
		out[i] = r
		if r.ChargeType.Attributable() {
			attributable++
			if !r.ResourceID.IsZero() {
				joined++
			}
		}
	}
	_ = err // a missing inventory (fresh account, no discovery yet) is not an ingestion failure
	coverage := 1.0
	if attributable > 0 {
		coverage = float64(joined) / float64(attributable)
	}
	return out, coverage
}

// Summary assembles the cost-intelligence headline: totals, month-to-date,
// prior-month comparison, forecast, the standard breakdowns and freshness.
func (s *Service) Summary(ctx context.Context, tenant core.TenantID, period core.Period) (ports.CostSummary, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.CostSummary{}, err
	}
	total, err := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: period})
	if err != nil {
		return ports.CostSummary{}, err
	}
	daily := core.ZeroUSD()
	if days := period.Days(); days > 0 {
		daily = total.Div(days)
	}

	now := s.clock().Now()
	month := core.MonthOf(now)
	monthToDate, err := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: core.Period{Start: month.Start, End: now}})
	if err != nil {
		return ports.CostSummary{}, err
	}
	priorMonth, err := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{
		Period: core.Period{Start: month.Start.AddDate(0, -1, 0), End: month.Start},
	})
	if err != nil {
		return ports.CostSummary{}, err
	}
	changePct := 0.0
	if !priorMonth.IsZero() {
		changePct = total.MustSub(priorMonth).Ratio(priorMonth) * 100
	}

	forecast, ferr := s.Forecast(ctx, tenant, ports.CostFilter{}, core.Period{Start: now, End: month.End})
	if ferr != nil {
		forecast = cost.Forecast{Method: cost.ForecastInsufficient, Note: ferr.Error()}
	}

	byService, _ := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: period}, "service")
	byAccount, _ := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: period}, "account")
	byEnv, _ := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: period}, "environment")
	byApp, _ := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: period}, "application")
	trend, _ := s.Repos.Costs.Series(ctx, tenant, ports.CostFilter{Period: period, Granularity: cost.GranularityDaily})

	openAnomalies := 0
	if page, aerr := s.Repos.Costs.ListAnomalies(ctx, tenant, period.Start, period.End, ports.ListOptions{Limit: 500}); aerr == nil {
		for _, a := range page.Items {
			if !a.Acknowledged {
				openAnomalies++
			}
		}
	}

	lastIngested, _ := s.Repos.Costs.LatestIngestedAt(ctx, tenant)
	freshness := "unknown"
	if !lastIngested.IsZero() {
		switch age := now.Sub(lastIngested); {
		case age < 24*time.Hour:
			freshness = "current"
		case age < 72*time.Hour:
			freshness = "recent"
		default:
			freshness = "stale"
		}
	}

	return ports.CostSummary{
		Period: period, Total: total, DailyAverage: daily, MonthToDate: monthToDate,
		PriorMonth: priorMonth, ChangePct: changePct, Forecast: forecast,
		ByService: byService.TopN(10), ByAccount: byAccount.TopN(10),
		ByEnvironment: byEnv.TopN(10), ByApplication: byApp.TopN(10),
		Trend: trend, OpenAnomalies: openAnomalies, LastIngestedAt: lastIngested, Freshness: freshness,
	}, nil
}

// Series delegates to the repository, adding the tenant guard every service
// entry point owns even when the repository would also enforce it.
func (s *Service) Series(ctx context.Context, tenant core.TenantID, f ports.CostFilter) (cost.Series, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cost.Series{}, err
	}
	return s.Repos.Costs.Series(ctx, tenant, f)
}

// Breakdown delegates to the repository.
func (s *Service) Breakdown(ctx context.Context, tenant core.TenantID, f ports.CostFilter, dimension string) (cost.Breakdown, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cost.Breakdown{}, err
	}
	return s.Repos.Costs.Breakdown(ctx, tenant, f, dimension)
}

// ListAnomalies delegates to the repository.
func (s *Service) ListAnomalies(ctx context.Context, tenant core.TenantID, from, to time.Time) ([]cost.Anomaly, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	page, err := s.Repos.Costs.ListAnomalies(ctx, tenant, from, to, ports.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}
