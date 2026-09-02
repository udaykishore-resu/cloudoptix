package postgres

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// MetricRepository is the pgx-backed ports.MetricRepository.
type MetricRepository struct{ db *DB }

// NewMetricRepository builds a MetricRepository over db.
func NewMetricRepository(db *DB) *MetricRepository { return &MetricRepository{db: db} }

var _ ports.MetricRepository = (*MetricRepository)(nil)

// SaveSummaries upserts one row per resource (kind='summary'): the whole
// ResourceMetrics struct is marshalled into the summary JSONB column, per
// migrations/0005_resources.up.sql's comment on why — it is a bag of a
// dozen optional named Percentiles that every consumer reads as a unit, so
// there is no query pattern that benefits from exploding it into columns.
func (r *MetricRepository) SaveSummaries(ctx context.Context, tenant core.TenantID, summaries []ports.ResourceMetrics) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	if len(summaries) == 0 {
		return nil
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		for _, m := range summaries {
			if m.TenantID != tenant {
				return core.NewError(core.ErrTenantMismatch, "tenant_mismatch",
					"metric summary for resource %s belongs to tenant %s, not %s", m.ResourceID, m.TenantID, tenant)
			}
			if _, err := q.Exec(ctx, `
				INSERT INTO resource_metrics (id, tenant_id, resource_id, kind, window_start, window_end,
					summary, coverage, source, collected_at)
				VALUES ($1,$2,$3,'summary',$4,$5,$6,$7,$8,$9)
				ON CONFLICT (tenant_id, resource_id) WHERE kind = 'summary' DO UPDATE SET
					window_start = EXCLUDED.window_start, window_end = EXCLUDED.window_end,
					summary = EXCLUDED.summary, coverage = EXCLUDED.coverage, source = EXCLUDED.source,
					collected_at = EXCLUDED.collected_at
			`, string(core.NewID("rmt")), string(tenant), string(m.ResourceID), zeroToNil(m.Window.Start),
				zeroToNil(m.Window.End), toJSON(m), m.Coverage, m.Source, orNow(m.CollectedAt)); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func (r *MetricRepository) GetSummary(ctx context.Context, tenant core.TenantID, resourceID core.ID) (ports.ResourceMetrics, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.ResourceMetrics{}, err
	}
	var out ports.ResourceMetrics
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, `
			SELECT summary FROM resource_metrics WHERE tenant_id = $1 AND resource_id = $2 AND kind = 'summary'
		`, string(tenant), string(resourceID))
		var raw []byte
		if err := row.Scan(&raw); err != nil {
			return mapErr(err)
		}
		var m ports.ResourceMetrics
		if err := fromJSON(raw, &m); err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// LoadSummaries batch-loads every summary for the given resources, or every
// summary the tenant has when resourceIDs is empty — matching the memstore
// reference's "empty means all" behaviour, which the rule engine relies on
// when it evaluates the whole inventory at once.
func (r *MetricRepository) LoadSummaries(ctx context.Context, tenant core.TenantID, resourceIDs []core.ID) (map[core.ID]ports.ResourceMetrics, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	out := map[core.ID]ports.ResourceMetrics{}
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		sql := `SELECT resource_id, summary FROM resource_metrics WHERE tenant_id = $1 AND kind = 'summary'`
		args := []any{string(tenant)}
		if len(resourceIDs) > 0 {
			args = append(args, toStringSlice(resourceIDs))
			sql += ` AND resource_id = ANY($2::text[])`
		}
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				return mapErr(err)
			}
			var m ports.ResourceMetrics
			if err := fromJSON(raw, &m); err != nil {
				return err
			}
			out[core.ID(id)] = m
		}
		return mapErr(rows.Err())
	})
	return out, err
}

// SaveSeries upserts one row per (resource, metric, namespace) pair
// (kind='series'). points is bounded by the caller to the short raw-
// retention window ports.MetricRepository's own doc comment describes; this
// method just stores whatever it is given.
func (r *MetricRepository) SaveSeries(ctx context.Context, tenant core.TenantID, series []ports.MetricSeries) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	if len(series) == 0 {
		return nil
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		for _, s := range series {
			var windowStart, windowEnd any
			if len(s.Points) > 0 {
				windowStart, windowEnd = s.Points[0].At, s.Points[len(s.Points)-1].At
			}
			if _, err := q.Exec(ctx, `
				INSERT INTO resource_metrics (id, tenant_id, resource_id, kind, metric_name, namespace,
					dimensions, unit, window_start, window_end, summary, points, source, collected_at)
				VALUES ($1,$2,$3,'series',$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
				ON CONFLICT (tenant_id, resource_id, metric_name, namespace) WHERE kind = 'series' DO UPDATE SET
					dimensions = EXCLUDED.dimensions, unit = EXCLUDED.unit, window_start = EXCLUDED.window_start,
					window_end = EXCLUDED.window_end, summary = EXCLUDED.summary, points = EXCLUDED.points,
					source = EXCLUDED.source, collected_at = now()
			`, string(core.NewID("rmt")), string(tenant), string(s.ResourceID), s.MetricName, "",
				toJSON(s.Dimensions), s.Unit, windowStart, windowEnd, toJSON(s.Summary), toJSON(s.Points),
				s.Source); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func (r *MetricRepository) GetSeries(ctx context.Context, tenant core.TenantID, q ports.MetricQuery) (ports.MetricSeries, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.MetricSeries{}, err
	}
	var out ports.MetricSeries
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, `
			SELECT resource_id, metric_name, unit, points, dimensions, summary, source
			FROM resource_metrics
			WHERE tenant_id = $1 AND resource_id = $2 AND metric_name = $3 AND kind = 'series'
		`, string(tenant), string(q.ResourceID), q.MetricName)
		var s ports.MetricSeries
		var points, dimensions, summary []byte
		if err := row.Scan(&s.ResourceID, &s.MetricName, &s.Unit, &points, &dimensions, &summary, &s.Source); err != nil {
			return mapErr(err)
		}
		if err := fromJSON(points, &s.Points); err != nil {
			return err
		}
		if err := fromJSON(dimensions, &s.Dimensions); err != nil {
			return err
		}
		if err := fromJSON(summary, &s.Summary); err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
}
