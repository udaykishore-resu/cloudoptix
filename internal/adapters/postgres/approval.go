package postgres

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ApprovalRepository is the pgx-backed ports.ApprovalRepository.
type ApprovalRepository struct{ db *DB }

// NewApprovalRepository builds an ApprovalRepository over db.
func NewApprovalRepository(db *DB) *ApprovalRepository { return &ApprovalRepository{db: db} }

var _ ports.ApprovalRepository = (*ApprovalRepository)(nil)

// Create persists the request row and, if it already arrives with responses
// (the rare replay/import path — a fresh request never does), its response
// rows in the same transaction, matching the memstore reference's
// already-exists check on the id.
func (r *ApprovalRepository) Create(ctx context.Context, req govern.Request) error {
	if err := core.GuardTenant(ctx, req.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, req.TenantID, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		if _, err := q.Exec(ctx, `
			INSERT INTO approvals (id, tenant_id, subject_kind, subject_id, title, summary, context,
				policy_decision_id, required_roles, min_approvals, require_distinct_approver, state,
				requested_by, requested_at, expires_at, decided_at, execute_after)
			VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15,$16,$17)
		`, string(req.ID), string(req.TenantID), string(req.SubjectKind), string(req.SubjectID),
			req.Title, req.Summary, toJSON(req.Context), string(req.PolicyDecisionID),
			toJSON(req.RequiredRoles), req.MinApprovals, req.RequireDistinctApprover, string(req.State),
			req.RequestedBy, orNow(req.RequestedAt), req.ExpiresAt, nilableTime(req.DecidedAt),
			req.ExecuteAfter); err != nil {
			return mapErr(err)
		}
		for _, resp := range req.Responses {
			if err := insertApprovalResponse(ctx, q, req.TenantID, req.ID, resp); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertApprovalResponse(ctx context.Context, q Querier, tenant core.TenantID, approvalID core.ID, resp govern.Response) error {
	_, err := q.Exec(ctx, `
		INSERT INTO approval_responses (id, tenant_id, approval_id, principal, role, approved, comment,
			ip_address, user_agent, at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (approval_id, principal) DO UPDATE SET
			approved = EXCLUDED.approved, comment = EXCLUDED.comment, at = EXCLUDED.at
	`, string(core.NewID("apr")), string(tenant), string(approvalID), resp.Principal, string(resp.Role),
		resp.Approved, resp.Comment, resp.IPAddress, resp.UserAgent, orNow(resp.At))
	return mapErr(err)
}

func (r *ApprovalRepository) Get(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Request, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return govern.Request{}, err
	}
	var out govern.Request
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, approvalSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		req, err := scanApproval(row)
		if err != nil {
			return mapErr(err)
		}
		responses, err := loadApprovalResponses(ctx, r.db.querier(ctx), id)
		if err != nil {
			return err
		}
		req.Responses = responses
		out = req
		return nil
	})
	return out, err
}

// Update rewrites the request's mutable fields and, since Request.Decide
// appends to Responses in place, reconciles the response set by upserting
// whatever Responses now holds — a principal that already voted updates
// their row (ON CONFLICT in insertApprovalResponse), a new vote inserts one.
func (r *ApprovalRepository) Update(ctx context.Context, req govern.Request) error {
	if err := core.GuardTenant(ctx, req.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, req.TenantID, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		tag, err := q.Exec(ctx, `
			UPDATE approvals SET state = $3, decided_at = $4 WHERE tenant_id = $1 AND id = $2
		`, string(req.TenantID), string(req.ID), string(req.State), nilableTime(req.DecidedAt))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("approval_request", req.ID)
		}
		for _, resp := range req.Responses {
			if err := insertApprovalResponse(ctx, q, req.TenantID, req.ID, resp); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ApprovalRepository) ListPending(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[govern.Request], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[govern.Request]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[govern.Request]{}, err
	}
	var page ports.Page[govern.Request]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where := `tenant_id = $1 AND state = 'pending'`
		args := []any{string(tenant)}
		if after != nil {
			args = append(args, after[0])
			where += " AND id > $2"
		}
		sql := approvalSelectSQL + " WHERE " + where + " ORDER BY id LIMIT " + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []govern.Request
		for rows.Next() {
			req, err := scanApproval(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, req)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		if len(items) > opts.Limit {
			items = items[:opts.Limit]
			page.NextCursor = encodeCursor(string(items[len(items)-1].ID))
		}
		// ListPending intentionally does not populate Responses: a pending
		// request has none yet by definition (Decide transitions state away
		// from pending on the first vote that closes it), so the N+1 lookup
		// Get pays is not worth paying here for a dashboard list.
		page.Items = items
		return nil
	})
	return page, err
}

func (r *ApprovalRepository) ListBySubject(ctx context.Context, tenant core.TenantID, kind govern.SubjectKind, subjectID core.ID) ([]govern.Request, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []govern.Request
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx,
			approvalSelectSQL+` WHERE tenant_id = $1 AND subject_kind = $2 AND subject_id = $3 ORDER BY requested_at`,
			string(tenant), string(kind), string(subjectID))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			req, err := scanApproval(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, req)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

// ExpireOverdue sweeps every tenant, which is why it runs under
// WithSystemScope rather than WithTenant: it is the same background-job
// shape the ports.ApprovalRepository doc comment describes, and it has no
// single tenant to scope to.
func (r *ApprovalRepository) ExpireOverdue(ctx context.Context, now time.Time) (int, error) {
	marked := 0
	err := r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE approvals SET state = 'expired'
			WHERE state = 'pending' AND expires_at <> '-infinity'::timestamptz AND expires_at < $1
		`, now)
		if err != nil {
			return mapErr(err)
		}
		marked = int(tag.RowsAffected())
		return nil
	})
	return marked, err
}

func loadApprovalResponses(ctx context.Context, q Querier, approvalID core.ID) ([]govern.Response, error) {
	rows, err := q.Query(ctx, `
		SELECT principal, role, approved, comment, ip_address, user_agent, at
		FROM approval_responses WHERE approval_id = $1 ORDER BY at
	`, string(approvalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []govern.Response
	for rows.Next() {
		var resp govern.Response
		if err := rows.Scan(&resp.Principal, &resp.Role, &resp.Approved, &resp.Comment, &resp.IPAddress,
			&resp.UserAgent, &resp.At); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, resp)
	}
	return out, mapErr(rows.Err())
}

const approvalSelectSQL = `
	SELECT id, tenant_id, subject_kind, subject_id, title, summary, context, COALESCE(policy_decision_id,''),
		required_roles, min_approvals, require_distinct_approver, state, requested_by, requested_at,
		expires_at, decided_at, execute_after
	FROM approvals`

func scanApproval(row rowScanner) (govern.Request, error) {
	var req govern.Request
	var contextJSON, requiredRoles []byte
	var decidedAt, executeAfter *time.Time
	if err := row.Scan(&req.ID, &req.TenantID, &req.SubjectKind, &req.SubjectID, &req.Title, &req.Summary,
		&contextJSON, &req.PolicyDecisionID, &requiredRoles, &req.MinApprovals, &req.RequireDistinctApprover,
		&req.State, &req.RequestedBy, &req.RequestedAt, &req.ExpiresAt, &decidedAt, &executeAfter); err != nil {
		return govern.Request{}, err
	}
	req.DecidedAt = decidedAt
	req.ExecuteAfter = executeAfter
	if err := fromJSON(contextJSON, &req.Context); err != nil {
		return govern.Request{}, err
	}
	if err := fromJSON(requiredRoles, &req.RequiredRoles); err != nil {
		return govern.Request{}, err
	}
	return req, nil
}
