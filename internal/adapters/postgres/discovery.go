package postgres

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// DiscoveryRunRepository is the pgx-backed ports.DiscoveryRunRepository.
type DiscoveryRunRepository struct{ db *DB }

// NewDiscoveryRunRepository builds a DiscoveryRunRepository over db.
func NewDiscoveryRunRepository(db *DB) *DiscoveryRunRepository {
	return &DiscoveryRunRepository{db: db}
}

var _ ports.DiscoveryRunRepository = (*DiscoveryRunRepository)(nil)

func (r *DiscoveryRunRepository) Create(ctx context.Context, run ports.DiscoveryRun) error {
	if err := core.GuardTenant(ctx, run.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, run.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO discovery_runs (id, tenant_id, account_id, regions, trigger, state, started_at,
				finished_at, resources_discovered, resources_updated, resources_removed,
				relationships_found, metrics_collected, service_results, errors, coverage, duration_ms)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		`, string(run.ID), string(run.TenantID), string(run.AccountID), toJSON(run.Regions), run.Trigger,
			run.State, orNow(run.StartedAt), nilableTime(run.FinishedAt), run.ResourcesDiscovered,
			run.ResourcesUpdated, run.ResourcesRemoved, run.RelationshipsFound, run.MetricsCollected,
			toJSON(run.ServiceResults), toJSON(run.Errors), run.Coverage, run.DurationMS)
		return mapErr(err)
	})
}

func (r *DiscoveryRunRepository) Get(ctx context.Context, tenant core.TenantID, id core.ID) (ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.DiscoveryRun{}, err
	}
	var out ports.DiscoveryRun
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, discoveryRunSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		run, err := scanDiscoveryRun(row)
		if err != nil {
			return mapErr(err)
		}
		out = run
		return nil
	})
	return out, err
}

func (r *DiscoveryRunRepository) Update(ctx context.Context, run ports.DiscoveryRun) error {
	if err := core.GuardTenant(ctx, run.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, run.TenantID, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE discovery_runs SET account_id = $3, regions = $4, trigger = $5, state = $6,
				started_at = $7, finished_at = $8, resources_discovered = $9, resources_updated = $10,
				resources_removed = $11, relationships_found = $12, metrics_collected = $13,
				service_results = $14, errors = $15, coverage = $16, duration_ms = $17
			WHERE tenant_id = $1 AND id = $2
		`, string(run.TenantID), string(run.ID), string(run.AccountID), toJSON(run.Regions), run.Trigger,
			run.State, orNow(run.StartedAt), nilableTime(run.FinishedAt), run.ResourcesDiscovered,
			run.ResourcesUpdated, run.ResourcesRemoved, run.RelationshipsFound, run.MetricsCollected,
			toJSON(run.ServiceResults), toJSON(run.Errors), run.Coverage, run.DurationMS)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("discovery_run", run.ID)
		}
		return nil
	})
}

// ListRecent returns the tenant's most recently started runs, newest first —
// matching memstore's discoveryRunRepo.ListRecent, which sorts on
// (StartedAt DESC, ID ASC) and then truncates to limit. limit <= 0 means "no
// limit", also matching memstore.
func (r *DiscoveryRunRepository) ListRecent(ctx context.Context, tenant core.TenantID, limit int) ([]ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []ports.DiscoveryRun
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		sql := discoveryRunSelectSQL + ` WHERE tenant_id = $1 ORDER BY started_at DESC, id`
		args := []any{string(tenant)}
		if limit > 0 {
			sql += ` LIMIT ` + limitPlaceholder(2)
			args = append(args, limit)
		}
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanDiscoveryRun(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, run)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

// Latest returns the most recently STARTED run for accountID, matching
// memstore's Latest exactly — it compares StartedAt, not FinishedAt, so an
// in-flight run started after an older completed one is still "latest".
func (r *DiscoveryRunRepository) Latest(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.DiscoveryRun{}, err
	}
	var out ports.DiscoveryRun
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx,
			discoveryRunSelectSQL+` WHERE tenant_id = $1 AND account_id = $2 ORDER BY started_at DESC LIMIT 1`,
			string(tenant), string(accountID))
		run, err := scanDiscoveryRun(row)
		if err != nil {
			if isNoRows(err) {
				return core.NotFound("discovery_run", accountID)
			}
			return mapErr(err)
		}
		out = run
		return nil
	})
	return out, err
}

const discoveryRunSelectSQL = `
	SELECT id, tenant_id, account_id, regions, trigger, state, started_at, finished_at,
		resources_discovered, resources_updated, resources_removed, relationships_found,
		metrics_collected, service_results, errors, coverage, duration_ms
	FROM discovery_runs`

func scanDiscoveryRun(row rowScanner) (ports.DiscoveryRun, error) {
	var run ports.DiscoveryRun
	var regions, serviceResults, errs []byte
	if err := row.Scan(&run.ID, &run.TenantID, &run.AccountID, &regions, &run.Trigger, &run.State,
		&run.StartedAt, &run.FinishedAt, &run.ResourcesDiscovered, &run.ResourcesUpdated,
		&run.ResourcesRemoved, &run.RelationshipsFound, &run.MetricsCollected, &serviceResults, &errs,
		&run.Coverage, &run.DurationMS); err != nil {
		return ports.DiscoveryRun{}, err
	}
	if err := fromJSON(regions, &run.Regions); err != nil {
		return ports.DiscoveryRun{}, err
	}
	if err := fromJSON(serviceResults, &run.ServiceResults); err != nil {
		return ports.DiscoveryRun{}, err
	}
	if err := fromJSON(errs, &run.Errors); err != nil {
		return ports.DiscoveryRun{}, err
	}
	return run, nil
}
