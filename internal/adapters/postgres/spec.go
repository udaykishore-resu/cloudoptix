package postgres

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// SpecRepository is the pgx-backed ports.SpecRepository.
type SpecRepository struct{ db *DB }

// NewSpecRepository builds a SpecRepository over db.
func NewSpecRepository(db *DB) *SpecRepository { return &SpecRepository{db: db} }

var _ ports.SpecRepository = (*SpecRepository)(nil)

// SaveDraft upserts the grouping `specs` row (idempotent: a spec_id seen
// before is left alone) and then replaces the version wholesale, matching
// the ports.SpecRepository doc comment: "a draft is replaced wholesale by
// SaveDraft" — there is no partial field update path for a draft.
func (r *SpecRepository) SaveDraft(ctx context.Context, v spec.Version) error {
	if err := core.GuardTenant(ctx, v.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, v.TenantID, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		if _, err := q.Exec(ctx, `
			INSERT INTO specs (id, tenant_id, created_at, updated_at)
			VALUES ($1,$2,now(),now())
			ON CONFLICT (id) DO NOTHING
		`, string(v.SpecID), string(v.TenantID)); err != nil {
			return mapErr(err)
		}
		_, err := q.Exec(ctx, `
			INSERT INTO spec_versions (id, tenant_id, spec_id, version, status, spec_document, checksum,
				parent_id, diff, validation, completeness, created_by, created_at, approved_by, approved_at,
				approval_id, rejected_reason, conversation_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''),$17,NULLIF($18,''))
			ON CONFLICT (tenant_id, spec_id, version) DO UPDATE SET
				status = EXCLUDED.status, spec_document = EXCLUDED.spec_document, checksum = EXCLUDED.checksum,
				parent_id = EXCLUDED.parent_id, diff = EXCLUDED.diff, validation = EXCLUDED.validation,
				completeness = EXCLUDED.completeness, created_by = EXCLUDED.created_by,
				conversation_id = EXCLUDED.conversation_id
		`,
			string(v.ID), string(v.TenantID), string(v.SpecID), v.Version, string(v.Status),
			toJSON(v.Spec), v.Checksum, string(v.ParentID), toJSON(v.Diff), toJSON(v.Validation),
			toJSON(v.Completeness), v.CreatedBy, orNow(v.CreatedAt), v.ApprovedBy, v.ApprovedAt,
			string(v.ApprovalID), v.RejectedReason, string(v.ConversationID),
		)
		return mapErr(err)
	})
}

// Get fetches a version by its own row id.
func (r *SpecRepository) Get(ctx context.Context, tenant core.TenantID, id core.ID) (spec.Version, error) {
	return r.getOne(ctx, tenant, `id = $2`, string(id))
}

// GetVersion fetches one numbered version of a spec.
func (r *SpecRepository) GetVersion(ctx context.Context, tenant core.TenantID, specID core.ID, version int) (spec.Version, error) {
	return r.getOne(ctx, tenant, `spec_id = $2 AND version = $3`, string(specID), version)
}

// GetActive fetches the tenant's single approved-and-not-superseded version.
// See migrations/0003_spec.up.sql's partial unique index: at most one row
// per (tenant, spec_id) can be status='approved' at a time, and a tenant
// has (in the current product) exactly one spec family, so this is simply
// "the approved row for this tenant", without needing to know a spec_id
// first.
func (r *SpecRepository) GetActive(ctx context.Context, tenant core.TenantID) (spec.Version, error) {
	return r.getOne(ctx, tenant, `status = 'approved'`)
}

// GetLatest fetches the highest-numbered version of a spec, draft or not.
func (r *SpecRepository) GetLatest(ctx context.Context, tenant core.TenantID, specID core.ID) (spec.Version, error) {
	return r.getOneOrdered(ctx, tenant, `spec_id = $2`, `version DESC LIMIT 1`, string(specID))
}

// ListVersions returns every version of a spec, oldest first, so a reviewer
// reads the history in the order it happened.
func (r *SpecRepository) ListVersions(ctx context.Context, tenant core.TenantID, specID core.ID) ([]spec.Version, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []spec.Version
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, specVersionSelectSQL+` WHERE tenant_id = $1 AND spec_id = $2 ORDER BY version`,
			string(tenant), string(specID))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanSpecVersion(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, v)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

// Approve freezes v (already transitioned to StatusApproved by
// spec.Version.Approve in the application layer) and, in the same
// transaction, supersedes whichever version was previously active — the
// "atomically" the ports doc comment requires: without both writes in one
// transaction, a crash between them would leave either zero or two active
// specifications, and every engine downstream reads "the" active spec
// assuming there is exactly one.
func (r *SpecRepository) Approve(ctx context.Context, tenant core.TenantID, v spec.Version) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	if v.Status != spec.StatusApproved {
		return core.Invalid("spec version %s must be approved before SpecRepository.Approve persists it", v.ID)
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		if _, err := q.Exec(ctx, `
			UPDATE spec_versions SET status = 'superseded'
			WHERE tenant_id = $1 AND spec_id = $2 AND status = 'approved' AND id <> $3
		`, string(tenant), string(v.SpecID), string(v.ID)); err != nil {
			return mapErr(err)
		}
		tag, err := q.Exec(ctx, `
			UPDATE spec_versions SET status = 'approved', approved_by = $4, approved_at = $5,
				approval_id = NULLIF($6,''), validation = $7
			WHERE tenant_id = $1 AND spec_id = $2 AND id = $3
		`, string(tenant), string(v.SpecID), string(v.ID), v.ApprovedBy, v.ApprovedAt,
			string(v.ApprovalID), toJSON(v.Validation))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("spec version", v.ID)
		}
		return nil
	})
}

// Reject marks a pending version rejected. `by` is recorded on the audit
// trail by the calling application service (every consequential decision is
// audited there, see internal/domain/audit); spec.Version itself carries no
// RejectedBy field, so this table does not either — duplicating it here
// would be a second, driftable copy of the same fact.
func (r *SpecRepository) Reject(ctx context.Context, tenant core.TenantID, id core.ID, reason, by string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	_ = by
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE spec_versions SET status = 'rejected', rejected_reason = $3
			WHERE tenant_id = $1 AND id = $2
		`, string(tenant), string(id), reason)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("spec version", id)
		}
		return nil
	})
}

func (r *SpecRepository) getOne(ctx context.Context, tenant core.TenantID, where string, args ...any) (spec.Version, error) {
	return r.getOneOrdered(ctx, tenant, where, "", args...)
}

func (r *SpecRepository) getOneOrdered(ctx context.Context, tenant core.TenantID, where, orderLimit string, args ...any) (spec.Version, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return spec.Version{}, err
	}
	var out spec.Version
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		sql := specVersionSelectSQL + ` WHERE tenant_id = $1 AND ` + where
		if orderLimit != "" {
			sql += ` ORDER BY ` + orderLimit
		}
		row := r.db.querier(ctx).QueryRow(ctx, sql, append([]any{string(tenant)}, args...)...)
		v, err := scanSpecVersion(row)
		if err != nil {
			return mapErr(err)
		}
		out = v
		return nil
	})
	return out, err
}

const specVersionSelectSQL = `
	SELECT id, tenant_id, spec_id, version, status, spec_document, checksum, COALESCE(parent_id,''),
		diff, validation, completeness, created_by, created_at, approved_by, approved_at,
		COALESCE(approval_id,''), rejected_reason, COALESCE(conversation_id,'')
	FROM spec_versions`

func scanSpecVersion(row rowScanner) (spec.Version, error) {
	var v spec.Version
	var specDoc, diff, validation, completeness []byte
	if err := row.Scan(&v.ID, &v.TenantID, &v.SpecID, &v.Version, &v.Status, &specDoc, &v.Checksum,
		&v.ParentID, &diff, &validation, &completeness, &v.CreatedBy, &v.CreatedAt, &v.ApprovedBy,
		&v.ApprovedAt, &v.ApprovalID, &v.RejectedReason, &v.ConversationID); err != nil {
		return spec.Version{}, err
	}
	if err := fromJSON(specDoc, &v.Spec); err != nil {
		return spec.Version{}, err
	}
	if err := fromJSON(diff, &v.Diff); err != nil {
		return spec.Version{}, err
	}
	if err := fromJSON(validation, &v.Validation); err != nil {
		return spec.Version{}, err
	}
	if err := fromJSON(completeness, &v.Completeness); err != nil {
		return spec.Version{}, err
	}
	return v, nil
}
