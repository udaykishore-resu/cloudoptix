package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TenantRepository is the pgx-backed ports.TenantRepository.
type TenantRepository struct{ db *DB }

// NewTenantRepository builds a TenantRepository over db.
func NewTenantRepository(db *DB) *TenantRepository { return &TenantRepository{db: db} }

var _ ports.TenantRepository = (*TenantRepository)(nil)

// Create inserts a new tenant. It runs under system scope, not WithTenant,
// for the obvious reason that a tenant being created has no row yet for
// RLS to scope a session to — GuardTenant is skipped here for the same
// reason every other method calls it: the caller's principal has no
// tenant of its own to check this write against (tenant creation is a
// platform-admin operation, authorized upstream by core.PermTenantAdmin /
// PermPlatformAdmin, not by tenant match).
func (r *TenantRepository) Create(ctx context.Context, t tenancy.Tenant) error {
	if err := t.Validate(); err != nil {
		return err
	}
	return r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		_, err := q.Exec(ctx, `
			INSERT INTO tenants (id, slug, name, plan, quotas, state, spec_id, active_spec_version,
				active_policy_id, demo, data_region, encryption_key_arn, primary_contact,
				created_at, activated_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,NULLIF($9,''),$10,$11,$12,$13,$14,$15,$14)
		`,
			string(t.ID), t.Slug, t.Name, string(t.Plan), toJSON(t.Quotas), string(t.State),
			string(t.SpecID), t.ActiveSpecVersion, string(t.ActivePolicyID), t.Demo, t.DataRegion,
			string(t.EncryptionKeyARN), t.PrimaryContact, orNow(t.CreatedAt), t.ActivatedAt,
		)
		return mapErr(err)
	})
}

// Get fetches one tenant by its own id, which is also its RLS scope.
func (r *TenantRepository) Get(ctx context.Context, id core.TenantID) (tenancy.Tenant, error) {
	var out tenancy.Tenant
	err := r.db.WithTenant(ctx, id, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, tenantSelectSQL+` WHERE id = $1`, string(id))
		t, err := scanTenant(row)
		if err != nil {
			return mapErr(err)
		}
		out = t
		return nil
	})
	return out, err
}

// GetBySlug fetches by the tenant's slug. Slugs are globally unique but the
// caller does not know the tenant id yet to scope WithTenant with — so this
// runs under system scope, then GuardTenant on the caller's principal (done
// upstream, in the application service, per the ports package doc's "the
// redundancy is deliberate" note) is what actually authorizes the read.
func (r *TenantRepository) GetBySlug(ctx context.Context, slug string) (tenancy.Tenant, error) {
	var out tenancy.Tenant
	err := r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, tenantSelectSQL+` WHERE slug = $1`, slug)
		t, err := scanTenant(row)
		if err != nil {
			return mapErr(err)
		}
		out = t
		return nil
	})
	return out, err
}

// Update replaces a tenant's mutable fields.
func (r *TenantRepository) Update(ctx context.Context, t tenancy.Tenant) error {
	if err := t.Validate(); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, t.ID, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE tenants SET
				slug = $2, name = $3, plan = $4, quotas = $5, state = $6,
				spec_id = NULLIF($7,''), active_spec_version = $8, active_policy_id = NULLIF($9,''),
				demo = $10, data_region = $11, encryption_key_arn = $12, primary_contact = $13,
				activated_at = $14
			WHERE id = $1
		`,
			string(t.ID), t.Slug, t.Name, string(t.Plan), toJSON(t.Quotas), string(t.State),
			string(t.SpecID), t.ActiveSpecVersion, string(t.ActivePolicyID), t.Demo, t.DataRegion,
			string(t.EncryptionKeyARN), t.PrimaryContact, t.ActivatedAt,
		)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("tenant", t.ID)
		}
		return nil
	})
}

// List enumerates tenants platform-wide (system scope: there is no single
// tenant a cross-tenant listing could be scoped to), keyset-paginated on id
// — tenant ids are ULID-like and therefore already creation-ordered, so no
// separate created_at tiebreaker column is needed.
func (r *TenantRepository) List(ctx context.Context, opts ports.ListOptions) (ports.Page[tenancy.Tenant], error) {
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[tenancy.Tenant]{}, err
	}
	var page ports.Page[tenancy.Tenant]
	err = r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		sql := tenantSelectSQL
		var args []any
		if after != nil {
			sql += ` WHERE id > $1`
			args = append(args, after[0])
		}
		sql += ` ORDER BY id LIMIT ` + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []tenancy.Tenant
		for rows.Next() {
			t, err := scanTenant(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, t)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		if len(items) > opts.Limit {
			items = items[:opts.Limit]
			page.NextCursor = encodeCursor(string(items[len(items)-1].ID))
		}
		page.Items = items
		return nil
	})
	return page, err
}

// CreateOrganization inserts an organization row under the owning tenant's
// scope.
func (r *TenantRepository) CreateOrganization(ctx context.Context, o tenancy.Organization) error {
	if err := core.GuardTenant(ctx, o.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, o.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO organizations (id, tenant_id, name, industry, size, business_regions, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
		`, string(o.ID), string(o.TenantID), o.Name, o.Industry, o.Size, toJSON(o.BusinessRegions), orNow(o.CreatedAt))
		return mapErr(err)
	})
}

// ListOrganizations lists a tenant's organizations, ordered by id (small,
// unpaged list — a tenant realistically has a handful, per the tenancy
// package doc's "normally has one, but an MSP tenant can hold several").
func (r *TenantRepository) ListOrganizations(ctx context.Context, tenant core.TenantID) ([]tenancy.Organization, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []tenancy.Organization
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, `
			SELECT id, tenant_id, name, industry, size, business_regions, created_at, updated_at
			FROM organizations WHERE tenant_id = $1 ORDER BY id
		`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var o tenancy.Organization
			var id, tid string
			var regions []byte
			if err := rows.Scan(&id, &tid, &o.Name, &o.Industry, &o.Size, &regions, &o.CreatedAt, &o.UpdatedAt); err != nil {
				return mapErr(err)
			}
			o.ID, o.TenantID = core.ID(id), core.TenantID(tid)
			if err := fromJSON(regions, &o.BusinessRegions); err != nil {
				return err
			}
			out = append(out, o)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

const tenantSelectSQL = `
	SELECT id, slug, name, plan, quotas, state, COALESCE(spec_id,''), active_spec_version,
		COALESCE(active_policy_id,''), demo, data_region, encryption_key_arn, primary_contact,
		created_at, activated_at, updated_at
	FROM tenants`

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query),
// which is what lets scanTenant (and its siblings across this package) back
// both a single-row Get and a multi-row List without duplicating the column
// list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanTenant(row rowScanner) (tenancy.Tenant, error) {
	var t tenancy.Tenant
	var id, plan, state, specID, policyID string
	var quotas []byte
	if err := row.Scan(&id, &t.Slug, &t.Name, &plan, &quotas, &state, &specID, &t.ActiveSpecVersion,
		&policyID, &t.Demo, &t.DataRegion, &t.EncryptionKeyARN, &t.PrimaryContact,
		&t.CreatedAt, &t.ActivatedAt, &t.UpdatedAt); err != nil {
		return tenancy.Tenant{}, err
	}
	t.ID, t.Plan, t.State = core.TenantID(id), tenancy.Plan(plan), tenancy.State(state)
	t.SpecID, t.ActivePolicyID = core.ID(specID), core.ID(policyID)
	if err := fromJSON(quotas, &t.Quotas); err != nil {
		return tenancy.Tenant{}, err
	}
	return t, nil
}

// orNow substitutes the current instant for a zero time.Time, used for
// created_at columns on insert so a caller need not stamp one itself.
func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// limitPlaceholder returns "$n" for the LIMIT clause's positional
// parameter, computed from how many WHERE-clause arguments already precede
// it. Kept as a tiny named helper rather than inline fmt.Sprintf at every
// call site so every List method spells "the next placeholder" the same
// way.
func limitPlaceholder(n int) string {
	return "$" + strconv.Itoa(n)
}
