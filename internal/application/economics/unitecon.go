package economics

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// UnitEconomics prices one business transaction for a period: its footprint
// divided by its measured volume, with the movement against the prior period
// decomposed into a volume effect and a unit-cost effect.
func (s *Service) UnitEconomics(ctx context.Context, tenant core.TenantID, transactionID core.ID, period core.Period) (econ.UnitEconomics, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.UnitEconomics{}, err
	}
	if period.IsZero() {
		period = s.defaultPeriod()
	}
	tx, err := s.Repos.Economics.GetTransaction(ctx, tenant, transactionID)
	if err != nil {
		return econ.UnitEconomics{}, err
	}
	fp, err := s.computeFootprintCore(ctx, tenant, econ.ScopeTransaction, transactionID, period, false)
	if err != nil {
		return econ.UnitEconomics{}, err
	}
	volume, volumeProv := s.resolveVolume(ctx, tenant, tx, period)
	ue := econ.ComputeUnitEconomics(tx, fp, volume, volumeProv)

	priorPeriod := core.Period{Start: period.Start.Add(-period.Duration()), End: period.Start}
	if priorFp, perr := s.computeFootprintCore(ctx, tenant, econ.ScopeTransaction, transactionID, priorPeriod, false); perr == nil {
		priorVolume, priorProv := s.resolveVolume(ctx, tenant, tx, priorPeriod)
		prior := econ.ComputeUnitEconomics(tx, priorFp, priorVolume, priorProv)
		if !prior.CostPerUnit.IsZero() {
			ue.PriorCostPerUnit = prior.CostPerUnit
			ue.ChangePct = ue.CostPerUnit.MustSub(prior.CostPerUnit).Ratio(prior.CostPerUnit) * 100
		}
		ue.Drivers = econ.DecomposeChange(prior, ue)
	}

	if err := s.Repos.Economics.SaveUnitEconomics(ctx, tenant, []econ.UnitEconomics{ue}); err != nil {
		return econ.UnitEconomics{}, err
	}
	return ue, nil
}

// UnitEconomicsHistory delegates to the repository, returning whatever
// UnitEconomics has previously computed and saved in the window.
func (s *Service) UnitEconomicsHistory(ctx context.Context, tenant core.TenantID, transactionID core.ID, from, to time.Time) ([]econ.UnitEconomics, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	return s.Repos.Economics.ListUnitEconomics(ctx, tenant, transactionID, from, to)
}

// resolveVolume determines a transaction's volume for a period according to
// its declared VolumeSource.
//
// A measured source (cloudwatch/prometheus/alb_requests) names the
// discovered resource carrying the signal via a "resource_id" dimension —
// ports.MetricQuery is keyed on a single core.ID resource, and a business
// transaction has no resource of its own, so the VolumeSource must point at
// the one resource (an API Gateway, an ALB) whose metric series holds the
// count. This is a real constraint of the metrics port, not an oversight:
// documented here and in the final report as the workaround. When no
// resource is named, or the named series has no data in the period, this
// falls back to the declared figure with a correspondingly weaker
// provenance — a stated volume with no telemetry to back it is still usable
// for a first cost-per-transaction estimate, just not one confident enough
// to drive automation.
func (s *Service) resolveVolume(ctx context.Context, tenant core.TenantID, tx econ.BusinessTransaction, period core.Period) (float64, core.Provenance) {
	vs := tx.VolumeSource
	if vs.Kind == "cloudwatch" || vs.Kind == "prometheus" || vs.Kind == "alb_requests" {
		if rid, ok := vs.Dimensions["resource_id"]; ok && rid != "" {
			series, err := s.Repos.Metrics.GetSeries(ctx, tenant, ports.MetricQuery{
				ResourceID: core.ID(rid), Namespace: vs.Namespace, MetricName: vs.MetricName,
				Statistic: "Sum", Period: period,
			})
			if err == nil {
				sum := 0.0
				for _, p := range series.Points {
					if period.Contains(p.At) {
						sum += p.Value
					}
				}
				if sum > 0 {
					return sum, core.ProvenanceConfirmed
				}
			}
		}
	}
	if vs.DeclaredMonthly > 0 {
		days := period.Days()
		if days <= 0 {
			days = core.AverageDaysPerMonth
		}
		scaled := vs.DeclaredMonthly * (days / core.AverageDaysPerMonth)
		prov := core.ProvenanceRequiresConfirmation
		if vs.Kind != "declared" && vs.Kind != "" {
			// A measured source was requested but produced nothing; the
			// declared figure is a fallback estimate, weaker than a tenant
			// deliberately choosing "declared" as its volume source.
			prov = core.ProvenanceUnknown
		}
		return scaled, prov
	}
	return 0, core.ProvenanceUnknown
}
