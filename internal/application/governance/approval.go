package governance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// defaultApprovalTTL is how long a request waits for a human before it is
// eligible to be swept into ApprovalExpired by ExpireOverdueApprovals. It is
// used only when the caller did not set Request.ExpiresAt explicitly.
const defaultApprovalTTL = 72 * time.Hour

// RequestApproval creates a pending approval request, routing it to the
// tenant's configured approver roles and segregation-of-duties setting when
// the caller did not already set them explicitly, and publishes
// EventApprovalRequested so the notification dispatcher can reach the right
// people without this package knowing how notifications are delivered.
func (s *Service) RequestApproval(ctx context.Context, r govern.Request) (govern.Request, error) {
	if r.TenantID.IsZero() {
		return govern.Request{}, core.Invalid("approval request must be tenant-scoped")
	}
	if r.SubjectID.IsZero() {
		return govern.Request{}, core.Invalid("approval request must name a subject")
	}
	sp, err := s.loadActiveSpec(ctx, r.TenantID)
	if err != nil {
		s.d.Logger.Warn("governance: loading spec for approval routing failed; using request-supplied routing only", "error", err)
	}

	now := s.d.Clock.Now()
	if r.ID.IsZero() {
		r.ID = core.NewID("apr")
	}
	if r.State == "" {
		r.State = govern.ApprovalPending
	}
	if r.RequestedAt.IsZero() {
		r.RequestedAt = now
	}
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = now.Add(defaultApprovalTTL)
	}
	if len(r.RequiredRoles) == 0 && len(sp.Governance.ApproverRoles) > 0 {
		r.RequiredRoles = sp.Governance.ApproverRoles
	}
	if r.MinApprovals <= 0 {
		if sp.Governance.MinApprovals > 0 {
			r.MinApprovals = sp.Governance.MinApprovals
		} else {
			r.MinApprovals = 1
		}
	}
	if !r.RequireDistinctApprover {
		r.RequireDistinctApprover = sp.Governance.SegregationOfDuties
	}

	if err := s.d.Approvals.Create(ctx, r); err != nil {
		return govern.Request{}, fmt.Errorf("governance: creating approval request: %w", err)
	}
	s.writeAudit(ctx, r.TenantID, audit.ActionApprovalRequested, audit.OutcomeSuccess, actorLabel(r.RequestedBy), string(r.SubjectKind), r.SubjectID,
		fmt.Sprintf("approval requested for %s %s: %s", r.SubjectKind, r.SubjectID, r.Title),
		map[string]any{"approval_id": string(r.ID), "min_approvals": r.MinApprovals})
	s.publish(ctx, ports.Event{
		Type: ports.EventApprovalRequested, TenantID: r.TenantID, SubjectID: r.ID,
		Actor: r.RequestedBy,
		Payload: map[string]any{
			"subject_kind": string(r.SubjectKind), "subject_id": string(r.SubjectID),
			"title": r.Title, "required_roles": r.RequiredRoles, "min_approvals": r.MinApprovals,
		},
	})
	return r, nil
}

// GetApproval returns one approval request by ID.
func (s *Service) GetApproval(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Request, error) {
	return s.d.Approvals.Get(ctx, tenant, id)
}

// ListApprovals returns the tenant's pending approval requests. The
// ApprovalRepository port exposes only a pending-scoped list method — a
// deliberate narrowing, since "every approval ever decided" is an audit-log
// query (ports.AuditService.Query), not a workflow-queue one — so this is
// what backs the interface's more general-sounding ListApprovals today.
func (s *Service) ListApprovals(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[govern.Request], error) {
	return s.d.Approvals.ListPending(ctx, tenant, opts)
}

// Decide records one approver's vote through govern.Request.Decide — which
// owns every segregation-of-duties rule: one vote per principal, a rejection
// is immediately final, and the requester cannot approve their own change
// when the policy demanded a distinct approver — and persists whatever state
// that produced, including the request having just expired out from under
// the caller.
func (s *Service) Decide(ctx context.Context, tenant core.TenantID, id core.ID, resp govern.Response) (govern.Request, error) {
	req, err := s.d.Approvals.Get(ctx, tenant, id)
	if err != nil {
		return govern.Request{}, err
	}

	decideErr := req.Decide(resp)
	// req.Decide mutates req.State to ApprovalExpired even when it returns an
	// error for that case, so the expiry must still be persisted — otherwise
	// the request would sit forever reporting itself pending to every future
	// caller despite having already missed its deadline.
	if updateErr := s.d.Approvals.Update(ctx, req); updateErr != nil {
		return govern.Request{}, fmt.Errorf("governance: persisting approval decision: %w", updateErr)
	}
	if decideErr != nil {
		if errors.Is(decideErr, core.ErrPreconditionOff) {
			s.writeAudit(ctx, tenant, audit.ActionApprovalExpired, audit.OutcomeDenied, actorLabel(resp.Principal), string(req.SubjectKind), req.SubjectID,
				fmt.Sprintf("approval %s expired before %s could respond", req.ID, resp.Principal), nil)
		}
		return govern.Request{}, decideErr
	}

	switch req.State {
	case govern.ApprovalApproved:
		s.writeAudit(ctx, tenant, audit.ActionApprovalGranted, audit.OutcomeSuccess, actorLabel(resp.Principal), string(req.SubjectKind), req.SubjectID,
			fmt.Sprintf("approval %s granted by %s (%d/%d)", req.ID, resp.Principal, req.ApprovalCount(), req.MinApprovals), nil)
		s.publish(ctx, ports.Event{
			Type: ports.EventApprovalGranted, TenantID: tenant, SubjectID: req.ID, Actor: resp.Principal,
			Payload: map[string]any{"subject_kind": string(req.SubjectKind), "subject_id": string(req.SubjectID)},
		})
	case govern.ApprovalRejected:
		s.writeAudit(ctx, tenant, audit.ActionApprovalRejected, audit.OutcomeDenied, actorLabel(resp.Principal), string(req.SubjectKind), req.SubjectID,
			fmt.Sprintf("approval %s rejected by %s: %s", req.ID, resp.Principal, resp.Comment), nil)
		s.publish(ctx, ports.Event{
			Type: ports.EventApprovalRejected, TenantID: tenant, SubjectID: req.ID, Actor: resp.Principal,
			Payload: map[string]any{"subject_kind": string(req.SubjectKind), "subject_id": string(req.SubjectID), "comment": resp.Comment},
		})
	default:
		// Still pending: a vote was recorded but MinApprovals has not been
		// reached yet. Recorded as a partial outcome so the audit trail shows
		// every individual vote, not only the one that tipped the decision.
		s.writeAudit(ctx, tenant, audit.ActionApprovalGranted, audit.OutcomePartial, actorLabel(resp.Principal), string(req.SubjectKind), req.SubjectID,
			fmt.Sprintf("approval %s recorded a vote from %s (%d/%d so far)", req.ID, resp.Principal, req.ApprovalCount(), req.MinApprovals), nil)
	}
	return req, nil
}

// ExpireOverdueApprovals sweeps every tenant's pending approvals for ones
// past their deadline, exactly like the background worker
// ports.ExecutionRepository.ClaimDuePlans is designed for. It is not part of
// ports.GovernanceService — it has no tenant scope, by nature of being a
// cross-tenant sweep — and is meant to be called on a schedule by whatever
// process runs CloudOptix's background workers.
//
// ApprovalRepository.ExpireOverdue's bulk contract reports only a count, not
// which requests it touched, so this method cannot write a per-request audit
// record or publish a per-request event the way Decide does; a repository
// enhancement returning the expired requests themselves would be needed for
// that, and is the honest limitation to note rather than paper over with
// audit records this method cannot actually attribute correctly.
func (s *Service) ExpireOverdueApprovals(ctx context.Context) (int, error) {
	return s.d.Approvals.ExpireOverdue(ctx, s.d.Clock.Now())
}
