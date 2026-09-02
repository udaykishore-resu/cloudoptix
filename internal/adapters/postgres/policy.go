package postgres

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// PolicyRepository is the pgx-backed ports.PolicyRepository.
type PolicyRepository struct{ db *DB }

// NewPolicyRepository builds a PolicyRepository over db.
func NewPolicyRepository(db *DB) *PolicyRepository { return &PolicyRepository{db: db} }

var _ ports.PolicyRepository = (*PolicyRepository)(nil)

func (r *PolicyRepository) Save(ctx context.Context, p govern.Policy) error {
	if err := core.GuardTenant(ctx, p.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO policies (id, tenant_id, name, description, version, rules, default_effect,
				enabled, created_by, created_at, activated_at, checksum)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (id) DO UPDATE SET
				description = EXCLUDED.description, rules = EXCLUDED.rules,
				default_effect = EXCLUDED.default_effect, checksum = EXCLUDED.checksum
		`, string(p.ID), string(p.TenantID), p.Name, p.Description, p.Version, toJSON(p.Rules),
			string(p.DefaultEffect), p.Enabled, p.CreatedBy, orNow(p.CreatedAt), zeroToNil(p.ActivatedAt),
			p.Checksum)
		return mapErr(err)
	})
}

func (r *PolicyRepository) Get(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Policy, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return govern.Policy{}, err
	}
	var out govern.Policy
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, policySelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		p, err := scanPolicy(row)
		if err != nil {
			return mapErr(err)
		}
		out = p
		return nil
	})
	return out, err
}

// GetActive relies on uq_policies_one_active (migrations/0009_govern.up.sql)
// to guarantee at most one row can match; a lookup finding two would be a
// constraint violation the database itself refused to allow, not something
// this method needs to defend against.
func (r *PolicyRepository) GetActive(ctx context.Context, tenant core.TenantID) (govern.Policy, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return govern.Policy{}, err
	}
	var out govern.Policy
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, policySelectSQL+` WHERE tenant_id = $1 AND enabled = true`,
			string(tenant))
		p, err := scanPolicy(row)
		if err != nil {
			return mapErr(err)
		}
		out = p
		return nil
	})
	return out, err
}

func (r *PolicyRepository) ListVersions(ctx context.Context, tenant core.TenantID, name string) ([]govern.Policy, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []govern.Policy
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx,
			policySelectSQL+` WHERE tenant_id = $1 AND name = $2 ORDER BY version DESC`, string(tenant), name)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPolicy(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, p)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

// Activate flips the target policy enabled and deactivates whatever was
// previously active, in one transaction — exactly the two-step Approve does
// for spec_versions, and for the same reason: uq_policies_one_active would
// reject the new row's INSERT/UPDATE if the old active row's flip hadn't
// already committed, so the deactivation must happen first, in the same
// transaction, or a concurrent reader could briefly see zero active
// policies (an even worse race than seeing two).
func (r *PolicyRepository) Activate(ctx context.Context, tenant core.TenantID, id core.ID, by string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	_ = by // audited by the calling service
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		if _, err := q.Exec(ctx, `UPDATE policies SET enabled = false WHERE tenant_id = $1 AND enabled = true`,
			string(tenant)); err != nil {
			return mapErr(err)
		}
		tag, err := q.Exec(ctx, `UPDATE policies SET enabled = true, activated_at = now() WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("policy", id)
		}
		return nil
	})
}

func (r *PolicyRepository) SaveDecision(ctx context.Context, d govern.Decision) error {
	if err := core.GuardTenant(ctx, d.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, d.TenantID, func(ctx context.Context) error {
		id := d.ID
		if id.IsZero() {
			id = core.NewID("pd")
		}
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO policy_decisions (id, tenant_id, recommendation_id, policy_id, policy_version,
				policy_checksum, effect, matched_rules, deciding_rule, reason, explanation,
				requires_approval, approvers, min_approvals, require_distinct_approver,
				maintenance_windows, input_digest, decided_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (id) DO NOTHING
		`, string(id), string(d.TenantID), string(d.RecommendationID), string(d.PolicyID), d.PolicyVersion,
			d.PolicyChecksum, string(d.Effect), toJSON(d.MatchedRules), d.DecidingRule, d.Reason,
			toJSON(d.Explanation), d.RequiresApproval, toJSON(d.Approvers), d.MinApprovals,
			d.RequireDistinctApprover, toJSON(d.MaintenanceWindows), d.InputDigest, orNow(d.DecidedAt))
		return mapErr(err)
	})
}

func (r *PolicyRepository) GetDecision(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Decision, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return govern.Decision{}, err
	}
	var out govern.Decision
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, decisionSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		d, err := scanDecision(row)
		if err != nil {
			return mapErr(err)
		}
		out = d
		return nil
	})
	return out, err
}

func (r *PolicyRepository) ListDecisions(ctx context.Context, tenant core.TenantID, recommendationID core.ID) ([]govern.Decision, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []govern.Decision
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx,
			decisionSelectSQL+` WHERE tenant_id = $1 AND recommendation_id = $2 ORDER BY decided_at`,
			string(tenant), string(recommendationID))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDecision(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, d)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

const policySelectSQL = `
	SELECT id, tenant_id, name, description, version, rules, default_effect, enabled, created_by,
		created_at, activated_at, checksum
	FROM policies`

func scanPolicy(row rowScanner) (govern.Policy, error) {
	var p govern.Policy
	var rules []byte
	var activatedAt *time.Time
	if err := row.Scan(&p.ID, &p.TenantID, &p.Name, &p.Description, &p.Version, &rules, &p.DefaultEffect,
		&p.Enabled, &p.CreatedBy, &p.CreatedAt, &activatedAt, &p.Checksum); err != nil {
		return govern.Policy{}, err
	}
	p.ActivatedAt = nilToZero(activatedAt)
	if err := fromJSON(rules, &p.Rules); err != nil {
		return govern.Policy{}, err
	}
	return p, nil
}

const decisionSelectSQL = `
	SELECT id, tenant_id, recommendation_id, policy_id, policy_version, policy_checksum, effect,
		matched_rules, deciding_rule, reason, explanation, requires_approval, approvers, min_approvals,
		require_distinct_approver, maintenance_windows, input_digest, decided_at
	FROM policy_decisions`

func scanDecision(row rowScanner) (govern.Decision, error) {
	var d govern.Decision
	var matchedRules, explanation, approvers, maintenanceWindows []byte
	if err := row.Scan(&d.ID, &d.TenantID, &d.RecommendationID, &d.PolicyID, &d.PolicyVersion,
		&d.PolicyChecksum, &d.Effect, &matchedRules, &d.DecidingRule, &d.Reason, &explanation,
		&d.RequiresApproval, &approvers, &d.MinApprovals, &d.RequireDistinctApprover, &maintenanceWindows,
		&d.InputDigest, &d.DecidedAt); err != nil {
		return govern.Decision{}, err
	}
	if err := fromJSON(matchedRules, &d.MatchedRules); err != nil {
		return govern.Decision{}, err
	}
	if err := fromJSON(explanation, &d.Explanation); err != nil {
		return govern.Decision{}, err
	}
	if err := fromJSON(approvers, &d.Approvers); err != nil {
		return govern.Decision{}, err
	}
	if err := fromJSON(maintenanceWindows, &d.MaintenanceWindows); err != nil {
		return govern.Decision{}, err
	}
	return d, nil
}
