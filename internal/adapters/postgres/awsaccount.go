package postgres

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// AWSAccountRepository is the pgx-backed ports.AWSAccountRepository.
type AWSAccountRepository struct{ db *DB }

// NewAWSAccountRepository builds an AWSAccountRepository over db.
func NewAWSAccountRepository(db *DB) *AWSAccountRepository { return &AWSAccountRepository{db: db} }

var _ ports.AWSAccountRepository = (*AWSAccountRepository)(nil)

func (r *AWSAccountRepository) Create(ctx context.Context, a cloud.AWSAccount) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if err := core.GuardTenant(ctx, a.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, a.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO aws_accounts (id, tenant_id, account_id, alias, environment, regions, access_mode,
				role_arns, external_id, session_prefix, state, state_reason, granted_scopes, missing_actions,
				is_payer, cur_bucket, cur_prefix, connected_at, last_verified_at, last_discovery_at,
				created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$21)
		`, string(a.ID), string(a.TenantID), string(a.AccountID), a.Alias, string(a.Environment),
			toJSON(a.Regions), string(a.AccessMode), toJSON(a.RoleARNs), a.ExternalID, a.SessionPrefix,
			string(a.State), a.StateReason, toJSON(a.GrantedScopes), toJSON(a.MissingActions), a.IsPayer,
			a.CURBucket, a.CURPrefix, zeroToNil(a.ConnectedAt), zeroToNil(a.LastVerifiedAt),
			zeroToNil(a.LastDiscoveryAt), orNow(a.CreatedAt))
		return mapErr(err)
	})
}

func (r *AWSAccountRepository) Get(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.AWSAccount, error) {
	return r.getOne(ctx, tenant, `id = $2`, string(id))
}

func (r *AWSAccountRepository) GetByAccountID(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (cloud.AWSAccount, error) {
	return r.getOne(ctx, tenant, `account_id = $2`, string(accountID))
}

func (r *AWSAccountRepository) Update(ctx context.Context, a cloud.AWSAccount) error {
	if err := core.GuardTenant(ctx, a.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, a.TenantID, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE aws_accounts SET alias=$3, environment=$4, regions=$5, access_mode=$6, role_arns=$7,
				external_id=$8, session_prefix=$9, state=$10, state_reason=$11, granted_scopes=$12,
				missing_actions=$13, is_payer=$14, cur_bucket=$15, cur_prefix=$16, connected_at=$17,
				last_verified_at=$18, last_discovery_at=$19
			WHERE tenant_id = $1 AND id = $2
		`, string(a.TenantID), string(a.ID), a.Alias, string(a.Environment), toJSON(a.Regions),
			string(a.AccessMode), toJSON(a.RoleARNs), a.ExternalID, a.SessionPrefix, string(a.State),
			a.StateReason, toJSON(a.GrantedScopes), toJSON(a.MissingActions), a.IsPayer, a.CURBucket,
			a.CURPrefix, zeroToNil(a.ConnectedAt), zeroToNil(a.LastVerifiedAt), zeroToNil(a.LastDiscoveryAt))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("aws account", a.ID)
		}
		return nil
	})
}

func (r *AWSAccountRepository) List(ctx context.Context, tenant core.TenantID) ([]cloud.AWSAccount, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []cloud.AWSAccount
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, awsAccountSelectSQL+` WHERE tenant_id = $1 ORDER BY created_at`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAWSAccount(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, a)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *AWSAccountRepository) Delete(ctx context.Context, tenant core.TenantID, id core.ID) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `DELETE FROM aws_accounts WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("aws account", id)
		}
		return nil
	})
}

func (r *AWSAccountRepository) getOne(ctx context.Context, tenant core.TenantID, where string, args ...any) (cloud.AWSAccount, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.AWSAccount{}, err
	}
	var out cloud.AWSAccount
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, awsAccountSelectSQL+` WHERE tenant_id = $1 AND `+where,
			append([]any{string(tenant)}, args...)...)
		a, err := scanAWSAccount(row)
		if err != nil {
			return mapErr(err)
		}
		out = a
		return nil
	})
	return out, err
}

const awsAccountSelectSQL = `
	SELECT id, tenant_id, account_id, alias, environment, regions, access_mode, role_arns, external_id,
		session_prefix, state, state_reason, granted_scopes, missing_actions, is_payer, cur_bucket,
		cur_prefix, connected_at, last_verified_at, last_discovery_at, created_at, updated_at
	FROM aws_accounts`

func scanAWSAccount(row rowScanner) (cloud.AWSAccount, error) {
	var a cloud.AWSAccount
	var regions, roleARNs, grantedScopes, missingActions []byte
	var connectedAt, lastVerifiedAt, lastDiscoveryAt *time.Time
	if err := row.Scan(&a.ID, &a.TenantID, &a.AccountID, &a.Alias, &a.Environment, &regions,
		&a.AccessMode, &roleARNs, &a.ExternalID, &a.SessionPrefix, &a.State, &a.StateReason,
		&grantedScopes, &missingActions, &a.IsPayer, &a.CURBucket, &a.CURPrefix, &connectedAt,
		&lastVerifiedAt, &lastDiscoveryAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return cloud.AWSAccount{}, err
	}
	a.ConnectedAt, a.LastVerifiedAt, a.LastDiscoveryAt = nilToZero(connectedAt), nilToZero(lastVerifiedAt), nilToZero(lastDiscoveryAt)
	if err := fromJSON(regions, &a.Regions); err != nil {
		return cloud.AWSAccount{}, err
	}
	if err := fromJSON(roleARNs, &a.RoleARNs); err != nil {
		return cloud.AWSAccount{}, err
	}
	if err := fromJSON(grantedScopes, &a.GrantedScopes); err != nil {
		return cloud.AWSAccount{}, err
	}
	if err := fromJSON(missingActions, &a.MissingActions); err != nil {
		return cloud.AWSAccount{}, err
	}
	return a, nil
}
