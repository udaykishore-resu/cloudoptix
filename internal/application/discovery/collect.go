package discovery

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// collectMetrics gathers a short utilisation window for this run's freshly
// discovered resources when the request asked for it, reusing the
// already-assumed session rather than a second AssumeRole call. It is
// deliberately shallow — a bounded, recent window saved as summaries, not
// the trend/seasonality analysis package utilization owns — because
// discovery's job is inventory currency, not deep telemetry analysis; a
// caller that wants the latter runs utilization.Collector next with this
// same session.
func (s *Service) collectMetrics(ctx context.Context, tenant core.TenantID, session ports.AWSSession, resources []cloud.Resource) int {
	if len(s.MetricCollectors) == 0 || len(resources) == 0 {
		return 0
	}
	collector := s.selectMetricCollector(ctx, session)
	if collector == nil {
		return 0
	}
	byRegion := map[core.Region][]cloud.Resource{}
	for _, r := range resources {
		byRegion[r.Region] = append(byRegion[r.Region], r)
	}
	days := s.MetricWindowDays
	if days <= 0 {
		days = 14
	}
	window := core.PeriodOfDays(s.clock().Now(), days)

	total := 0
	for region, regionResources := range byRegion {
		summaries, err := collector.Collect(ctx, ports.MetricCollectInput{
			TenantID: tenant, Session: session, Region: region, Resources: regionResources,
			Window: window, StepSeconds: 3600,
		})
		if err != nil || len(summaries) == 0 {
			continue
		}
		if err := s.Repos.Metrics.SaveSummaries(ctx, tenant, summaries); err != nil {
			continue
		}
		total += len(summaries)
	}
	return total
}

func (s *Service) selectMetricCollector(ctx context.Context, session ports.AWSSession) ports.MetricCollector {
	for _, c := range s.MetricCollectors {
		if c.Available(ctx, session) {
			return c
		}
	}
	return nil
}

// collectCost ingests a short trailing window of billed cost for this
// account when the request asked for it, preferring the Cost & Usage Report
// over Cost Explorer exactly as package costing's own Ingest does (see its
// doc comment for the reasoning) — duplicated here in miniature rather than
// imported, so this package's optional cost pass has no compile-time
// dependency on a sibling application package's ingestion pipeline, and can
// evolve or be omitted from a deployment independently of it.
func (s *Service) collectCost(ctx context.Context, tenant core.TenantID, account cloud.AWSAccount, session ports.AWSSession) {
	if len(s.CostIngestors) == 0 {
		return
	}
	ingestor := s.selectCostIngestor(ctx, session, account)
	if ingestor == nil {
		return
	}
	days := s.CostWindowDays
	if days <= 0 {
		days = 7
	}
	period := core.PeriodOfDays(s.clock().Now(), days)
	records, err := ingestor.Fetch(ctx, ports.CostIngestInput{
		TenantID: tenant, Session: session, Account: account, Period: period,
		Granularity: cost.GranularityDaily, Basis: cost.BasisAmortized, ResourceLevel: ingestor.Source() == "cur",
	})
	if err != nil || len(records) == 0 {
		return
	}
	for i := range records {
		records[i].TenantID = tenant
	}
	_, _ = s.Repos.Costs.UpsertBatch(ctx, tenant, records)
}

func (s *Service) selectCostIngestor(ctx context.Context, session ports.AWSSession, account cloud.AWSAccount) ports.CostIngestor {
	var cur, explorer, other ports.CostIngestor
	for _, ig := range s.CostIngestors {
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
