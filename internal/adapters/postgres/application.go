package postgres

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ApplicationRepository is the pgx-backed ports.ApplicationRepository.
type ApplicationRepository struct{ db *DB }

// NewApplicationRepository builds an ApplicationRepository over db.
func NewApplicationRepository(db *DB) *ApplicationRepository { return &ApplicationRepository{db: db} }

var _ ports.ApplicationRepository = (*ApplicationRepository)(nil)

func (r *ApplicationRepository) UpsertApplication(ctx context.Context, a cloud.Application) error {
	if err := core.GuardTenant(ctx, a.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, a.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO applications (id, tenant_id, name, slug, description, business_unit, domain,
				criticality, owner, environments, match_rules, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name, slug = EXCLUDED.slug, description = EXCLUDED.description,
				business_unit = EXCLUDED.business_unit, domain = EXCLUDED.domain,
				criticality = EXCLUDED.criticality, owner = EXCLUDED.owner,
				environments = EXCLUDED.environments, match_rules = EXCLUDED.match_rules
		`, string(a.ID), string(a.TenantID), a.Name, a.Slug, a.Description, a.BusinessUnit, a.Domain,
			criticalityOrUnset(a.Criticality), a.Owner, toJSON(a.Environments), toJSON(a.MatchRules),
			orNow(a.CreatedAt))
		return mapErr(err)
	})
}

func (r *ApplicationRepository) GetApplication(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Application, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.Application{}, err
	}
	var out cloud.Application
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, applicationSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		a, err := scanApplication(row)
		if err != nil {
			return mapErr(err)
		}
		out = a
		return nil
	})
	return out, err
}

func (r *ApplicationRepository) ListApplications(ctx context.Context, tenant core.TenantID) ([]cloud.Application, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []cloud.Application
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, applicationSelectSQL+` WHERE tenant_id = $1 ORDER BY name`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanApplication(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, a)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *ApplicationRepository) UpsertWorkload(ctx context.Context, w cloud.Workload) error {
	if err := core.GuardTenant(ctx, w.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, w.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO workloads (id, tenant_id, application_id, name, type, platform, environment,
				criticality, owner, team, cluster, namespace, match_rules, slo, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)
			ON CONFLICT (id) DO UPDATE SET
				application_id = EXCLUDED.application_id, name = EXCLUDED.name, type = EXCLUDED.type,
				platform = EXCLUDED.platform, environment = EXCLUDED.environment,
				criticality = EXCLUDED.criticality, owner = EXCLUDED.owner, team = EXCLUDED.team,
				cluster = EXCLUDED.cluster, namespace = EXCLUDED.namespace,
				match_rules = EXCLUDED.match_rules, slo = EXCLUDED.slo
		`, string(w.ID), string(w.TenantID), string(w.ApplicationID), w.Name, string(w.Type),
			string(w.Platform), string(w.Environment), criticalityOrUnset(w.Criticality), w.Owner,
			w.Team, w.Cluster, w.Namespace, toJSON(w.MatchRules), toJSON(w.SLO), orNow(w.CreatedAt))
		return mapErr(err)
	})
}

func (r *ApplicationRepository) GetWorkload(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Workload, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.Workload{}, err
	}
	var out cloud.Workload
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, workloadSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		w, err := scanWorkload(row)
		if err != nil {
			return mapErr(err)
		}
		out = w
		return nil
	})
	return out, err
}

func (r *ApplicationRepository) ListWorkloads(ctx context.Context, tenant core.TenantID, applicationID core.ID) ([]cloud.Workload, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []cloud.Workload
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, workloadSelectSQL+` WHERE tenant_id = $1 AND application_id = $2 ORDER BY name`,
			string(tenant), string(applicationID))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			w, err := scanWorkload(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, w)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

// criticalityOrUnset defaults an empty criticality to UNSET so the
// CHECK constraint on the column (which does not include the empty string)
// never rejects a zero-valued struct.
func criticalityOrUnset(c core.Criticality) string {
	if c == "" {
		return string(core.CriticalityUnset)
	}
	return string(c)
}

const applicationSelectSQL = `
	SELECT id, tenant_id, name, slug, description, business_unit, domain, criticality, owner,
		environments, match_rules, created_at, updated_at
	FROM applications`

func scanApplication(row rowScanner) (cloud.Application, error) {
	var a cloud.Application
	var environments, matchRules []byte
	if err := row.Scan(&a.ID, &a.TenantID, &a.Name, &a.Slug, &a.Description, &a.BusinessUnit, &a.Domain,
		&a.Criticality, &a.Owner, &environments, &matchRules, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return cloud.Application{}, err
	}
	if err := fromJSON(environments, &a.Environments); err != nil {
		return cloud.Application{}, err
	}
	if err := fromJSON(matchRules, &a.MatchRules); err != nil {
		return cloud.Application{}, err
	}
	return a, nil
}

const workloadSelectSQL = `
	SELECT id, tenant_id, application_id, name, type, platform, environment, criticality, owner, team,
		cluster, namespace, match_rules, slo, created_at, updated_at
	FROM workloads`

func scanWorkload(row rowScanner) (cloud.Workload, error) {
	var w cloud.Workload
	var matchRules, slo []byte
	if err := row.Scan(&w.ID, &w.TenantID, &w.ApplicationID, &w.Name, &w.Type, &w.Platform,
		&w.Environment, &w.Criticality, &w.Owner, &w.Team, &w.Cluster, &w.Namespace, &matchRules, &slo,
		&w.CreatedAt, &w.UpdatedAt); err != nil {
		return cloud.Workload{}, err
	}
	if err := fromJSON(matchRules, &w.MatchRules); err != nil {
		return cloud.Workload{}, err
	}
	if err := fromJSON(slo, &w.SLO); err != nil {
		return cloud.Workload{}, err
	}
	return w, nil
}
