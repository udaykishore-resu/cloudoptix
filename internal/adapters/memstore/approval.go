package memstore

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// approvalRepo implements ports.ApprovalRepository.
type approvalRepo struct{ s *Store }

func (r *approvalRepo) Create(ctx context.Context, req govern.Request) error {
	if err := core.GuardTenant(ctx, req.TenantID); err != nil {
		return err
	}
	r.s.approvalMu.Lock()
	defer r.s.approvalMu.Unlock()
	if r.s.data.Approvals[req.TenantID] == nil {
		r.s.data.Approvals[req.TenantID] = map[core.ID]govern.Request{}
	}
	if _, exists := r.s.data.Approvals[req.TenantID][req.ID]; exists {
		return core.NewError(core.ErrAlreadyExists, "approval_exists", "approval request %s already exists", req.ID)
	}
	r.s.data.Approvals[req.TenantID][req.ID] = deepCopy(req)
	return nil
}

func (r *approvalRepo) Get(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Request, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return govern.Request{}, err
	}
	r.s.approvalMu.RLock()
	defer r.s.approvalMu.RUnlock()
	req, ok := r.s.data.Approvals[tenant][id]
	if !ok {
		return govern.Request{}, core.NotFound("approval_request", id)
	}
	return deepCopy(req), nil
}

func (r *approvalRepo) Update(ctx context.Context, req govern.Request) error {
	if err := core.GuardTenant(ctx, req.TenantID); err != nil {
		return err
	}
	r.s.approvalMu.Lock()
	defer r.s.approvalMu.Unlock()
	if _, ok := r.s.data.Approvals[req.TenantID][req.ID]; !ok {
		return core.NotFound("approval_request", req.ID)
	}
	r.s.data.Approvals[req.TenantID][req.ID] = deepCopy(req)
	return nil
}

func (r *approvalRepo) ListPending(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[govern.Request], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[govern.Request]{}, err
	}
	r.s.approvalMu.RLock()
	items := make([]govern.Request, 0)
	for _, req := range r.s.data.Approvals[tenant] {
		if req.State == govern.ApprovalPending {
			items = append(items, deepCopy(req))
		}
	}
	r.s.approvalMu.RUnlock()

	keyOf := func(req govern.Request) (string, string) {
		return req.RequestedAt.Format(sortTimeLayout), req.ID.String()
	}
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}

func (r *approvalRepo) ListBySubject(ctx context.Context, tenant core.TenantID, kind govern.SubjectKind, subjectID core.ID) ([]govern.Request, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.approvalMu.RLock()
	defer r.s.approvalMu.RUnlock()
	out := make([]govern.Request, 0)
	for _, req := range r.s.data.Approvals[tenant] {
		if req.SubjectKind == kind && req.SubjectID == subjectID {
			out = append(out, deepCopy(req))
		}
	}
	sortByCreatedThenID(out, func(req govern.Request) (string, string) {
		return req.RequestedAt.Format(sortTimeLayout), req.ID.String()
	})
	return out, nil
}

// ExpireOverdue has no tenant argument: it is the background sweep that walks
// every tenant's pending approvals looking for ones past their deadline, the
// same shape a scheduled job against a "WHERE expires_at < now()" query would
// have against Postgres.
func (r *approvalRepo) ExpireOverdue(ctx context.Context, now time.Time) (int, error) {
	r.s.approvalMu.Lock()
	defer r.s.approvalMu.Unlock()
	n := 0
	for tenant, reqs := range r.s.data.Approvals {
		for id, req := range reqs {
			if req.State != govern.ApprovalPending || req.ExpiresAt.IsZero() || now.Before(req.ExpiresAt) {
				continue
			}
			req.State = govern.ApprovalExpired
			r.s.data.Approvals[tenant][id] = req
			n++
		}
	}
	return n, nil
}
