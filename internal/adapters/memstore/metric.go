package memstore

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// metricRepo implements ports.MetricRepository.
type metricRepo struct{ s *Store }

func (r *metricRepo) SaveSummaries(ctx context.Context, tenant core.TenantID, summaries []ports.ResourceMetrics) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.metricMu.Lock()
	defer r.s.metricMu.Unlock()
	if r.s.data.MetricSummaries[tenant] == nil {
		r.s.data.MetricSummaries[tenant] = map[core.ID]ports.ResourceMetrics{}
	}
	for _, m := range summaries {
		if m.TenantID != tenant {
			return core.NewError(core.ErrTenantMismatch, "tenant_mismatch",
				"metric summary for resource %s belongs to tenant %s, not %s", m.ResourceID, m.TenantID, tenant)
		}
		r.s.data.MetricSummaries[tenant][m.ResourceID] = deepCopy(m)
	}
	return nil
}

func (r *metricRepo) GetSummary(ctx context.Context, tenant core.TenantID, resourceID core.ID) (ports.ResourceMetrics, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.ResourceMetrics{}, err
	}
	r.s.metricMu.RLock()
	defer r.s.metricMu.RUnlock()
	m, ok := r.s.data.MetricSummaries[tenant][resourceID]
	if !ok {
		return ports.ResourceMetrics{}, core.NotFound("resource_metrics", resourceID)
	}
	return deepCopy(m), nil
}

func (r *metricRepo) LoadSummaries(ctx context.Context, tenant core.TenantID, resourceIDs []core.ID) (map[core.ID]ports.ResourceMetrics, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.metricMu.RLock()
	defer r.s.metricMu.RUnlock()
	out := make(map[core.ID]ports.ResourceMetrics, len(resourceIDs))
	if len(resourceIDs) == 0 {
		for id, m := range r.s.data.MetricSummaries[tenant] {
			out[id] = deepCopy(m)
		}
		return out, nil
	}
	for _, id := range resourceIDs {
		if m, ok := r.s.data.MetricSummaries[tenant][id]; ok {
			out[id] = deepCopy(m)
		}
	}
	return out, nil
}

func (r *metricRepo) SaveSeries(ctx context.Context, tenant core.TenantID, series []ports.MetricSeries) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.metricMu.Lock()
	defer r.s.metricMu.Unlock()
	existing := r.s.data.MetricSeries[tenant]
	for _, s := range series {
		replaced := false
		for i, e := range existing {
			if e.ResourceID == s.ResourceID && e.MetricName == s.MetricName {
				existing[i] = deepCopy(s)
				replaced = true
				break
			}
		}
		if !replaced {
			existing = append(existing, deepCopy(s))
		}
	}
	r.s.data.MetricSeries[tenant] = existing
	return nil
}

func (r *metricRepo) GetSeries(ctx context.Context, tenant core.TenantID, q ports.MetricQuery) (ports.MetricSeries, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.MetricSeries{}, err
	}
	r.s.metricMu.RLock()
	defer r.s.metricMu.RUnlock()
	for _, s := range r.s.data.MetricSeries[tenant] {
		if s.ResourceID == q.ResourceID && s.MetricName == q.MetricName {
			return deepCopy(s), nil
		}
	}
	return ports.MetricSeries{}, core.NotFound("metric_series", q.ResourceID)
}
