package postgres

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// UserRepository is the pgx-backed ports.UserRepository. Users themselves
// are not tenant-scoped (one OIDC identity, many memberships — see
// migrations/0002_tenancy.up.sql); memberships are, and every method that
// touches them scopes accordingly.
type UserRepository struct{ db *DB }

// NewUserRepository builds a UserRepository over db.
func NewUserRepository(db *DB) *UserRepository { return &UserRepository{db: db} }

var _ ports.UserRepository = (*UserRepository)(nil)

// Upsert writes the user row and, if the caller populated u.Memberships (an
// IdP-driven sync bringing a fully-formed user), each of those membership
// rows too. It runs the whole thing under system scope: a user upsert is
// not an operation any single tenant's session should be trusted to
// perform (it can plant a membership in a tenant the caller never proved it
// belongs to), so callers reach this only from the platform's own identity
// sync, never from a tenant-scoped request handler.
func (r *UserRepository) Upsert(ctx context.Context, u tenancy.User) error {
	return r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO users (id, subject, email, name, last_login_at, disabled, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
			ON CONFLICT (subject) DO UPDATE SET
				email = EXCLUDED.email, name = EXCLUDED.name,
				last_login_at = EXCLUDED.last_login_at, disabled = EXCLUDED.disabled
		`, string(u.ID), u.Subject, u.Email, u.Name, u.LastLoginAt, u.Disabled, orNow(u.CreatedAt))
		if err != nil {
			return mapErr(err)
		}
		for _, m := range u.Memberships {
			if err := upsertMembership(ctx, r.db, string(u.ID), m); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetBySubject loads a user and every tenant membership they hold. Login
// necessarily spans tenants (the whole point is discovering which tenants a
// subject may enter), so this runs under system scope like Upsert.
func (r *UserRepository) GetBySubject(ctx context.Context, subject string) (tenancy.User, error) {
	return r.getUser(ctx, `subject = $1`, subject)
}

// GetByEmail is GetBySubject's sibling for the email-based lookup path
// (invitations, admin search).
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (tenancy.User, error) {
	return r.getUser(ctx, `lower(email) = lower($1)`, email)
}

func (r *UserRepository) getUser(ctx context.Context, where, arg string) (tenancy.User, error) {
	var out tenancy.User
	err := r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, `
			SELECT id, subject, email, name, last_login_at, disabled, created_at, updated_at
			FROM users WHERE `+where, arg)
		if err := row.Scan(&out.ID, &out.Subject, &out.Email, &out.Name, &out.LastLoginAt,
			&out.Disabled, &out.CreatedAt, &out.UpdatedAt); err != nil {
			return mapErr(err)
		}
		ms, err := loadMemberships(ctx, r.db, string(out.ID))
		if err != nil {
			return err
		}
		out.Memberships = ms
		return nil
	})
	return out, err
}

// ListByTenant lists users holding a membership in tenant, keyset-paginated
// on the user id.
func (r *UserRepository) ListByTenant(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[tenancy.User], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[tenancy.User]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[tenancy.User]{}, err
	}
	var page ports.Page[tenancy.User]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		sql := `
			SELECT u.id, u.subject, u.email, u.name, u.last_login_at, u.disabled, u.created_at, u.updated_at
			FROM users u
			JOIN memberships m ON m.user_id = u.id AND m.tenant_id = $1`
		args := []any{string(tenant)}
		if after != nil {
			sql += ` WHERE u.id > $2`
			args = append(args, after[0])
		}
		sql += ` ORDER BY u.id LIMIT ` + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)

		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		var items []tenancy.User
		for rows.Next() {
			var u tenancy.User
			if err := rows.Scan(&u.ID, &u.Subject, &u.Email, &u.Name, &u.LastLoginAt,
				&u.Disabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
				rows.Close()
				return mapErr(err)
			}
			items = append(items, u)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		for i := range items {
			ms, err := loadMemberships(ctx, r.db, string(items[i].ID))
			if err != nil {
				return err
			}
			items[i].Memberships = ms
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

// AddMembership grants roles within one tenant.
func (r *UserRepository) AddMembership(ctx context.Context, userID core.ID, m tenancy.Membership) error {
	if err := core.GuardTenant(ctx, m.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, m.TenantID, func(ctx context.Context) error {
		return upsertMembership(ctx, r.db, string(userID), m)
	})
}

// RemoveMembership revokes a user's access to one tenant.
func (r *UserRepository) RemoveMembership(ctx context.Context, userID core.ID, tenant core.TenantID) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx,
			`DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, string(tenant), string(userID))
		return mapErr(err)
	})
}

func upsertMembership(ctx context.Context, db *DB, userID string, m tenancy.Membership) error {
	_, err := db.querier(ctx).Exec(ctx, `
		INSERT INTO memberships (id, tenant_id, user_id, roles, team, granted_by, granted_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, user_id) DO UPDATE SET
			roles = EXCLUDED.roles, team = EXCLUDED.team, granted_by = EXCLUDED.granted_by,
			granted_at = EXCLUDED.granted_at, expires_at = EXCLUDED.expires_at
	`, string(core.NewID("mem")), string(m.TenantID), userID, toJSON(m.Roles), m.Team, m.GrantedBy,
		orNow(m.GrantedAt), m.ExpiresAt)
	return mapErr(err)
}

func loadMemberships(ctx context.Context, db *DB, userID string) ([]tenancy.Membership, error) {
	rows, err := db.querier(ctx).Query(ctx, `
		SELECT tenant_id, roles, team, granted_by, granted_at, expires_at
		FROM memberships WHERE user_id = $1 ORDER BY tenant_id
	`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []tenancy.Membership
	for rows.Next() {
		var m tenancy.Membership
		var tid string
		var roles []byte
		if err := rows.Scan(&tid, &roles, &m.Team, &m.GrantedBy, &m.GrantedAt, &m.ExpiresAt); err != nil {
			return nil, mapErr(err)
		}
		m.TenantID = core.TenantID(tid)
		if err := fromJSON(roles, &m.Roles); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, mapErr(rows.Err())
}
