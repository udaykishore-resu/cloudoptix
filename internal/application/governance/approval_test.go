package governance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
)

func mkApprovalRequest(tenant core.TenantID, requestedBy string, minApprovals int, requireDistinct bool) govern.Request {
	return govern.Request{
		TenantID: tenant, SubjectKind: govern.SubjectRecommendation, SubjectID: core.NewID("rec"),
		Title: "Resize web-1", RequestedBy: requestedBy,
		MinApprovals: minApprovals, RequireDistinctApprover: requireDistinct,
	}
}

func TestRequestApproval_AssignsDefaultsAndRoutesFromSpec(t *testing.T) {
	svc, repos := newTestService(t)
	sp := testSpec(true)
	sp.Governance.ApproverRoles = []string{"sre", "tenant_admin"}
	sp.Governance.MinApprovals = 2
	sp.Governance.SegregationOfDuties = true
	seedSpec(t, repos, testTenant, sp)

	r := govern.Request{TenantID: testTenant, SubjectKind: govern.SubjectRecommendation, SubjectID: core.NewID("rec"), RequestedBy: "dev@example.com"}
	created, err := svc.RequestApproval(ctxFor(testTenant), r)
	require.NoError(t, err)
	assert.False(t, created.ID.IsZero())
	assert.Equal(t, govern.ApprovalPending, created.State)
	assert.Equal(t, []string{"sre", "tenant_admin"}, created.RequiredRoles)
	assert.Equal(t, 2, created.MinApprovals)
	assert.True(t, created.RequireDistinctApprover)
	assert.False(t, created.ExpiresAt.IsZero())

	fetched, err := svc.GetApproval(ctxFor(testTenant), testTenant, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, fetched.ID)
}

// TestDecide_SegregationOfDutiesBlocksSelfApproval proves the requester
// cannot approve their own change when the request demands a distinct
// approver.
func TestDecide_SegregationOfDutiesBlocksSelfApproval(t *testing.T) {
	svc, repos := newTestService(t)
	seedSpec(t, repos, testTenant, testSpec(true))

	r := mkApprovalRequest(testTenant, "dev@example.com", 1, true)
	created, err := svc.RequestApproval(ctxFor(testTenant), r)
	require.NoError(t, err)

	_, err = svc.Decide(ctxFor(testTenant), testTenant, created.ID, govern.Response{
		Principal: "dev@example.com", Approved: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)

	// The request is still pending — a rejected self-approval attempt must
	// not silently consume the vote.
	fetched, err := svc.GetApproval(ctxFor(testTenant), testTenant, created.ID)
	require.NoError(t, err)
	assert.Equal(t, govern.ApprovalPending, fetched.State)
	assert.Empty(t, fetched.Responses)
}

// TestDecide_OneRejectionIsFinal proves a single rejection ends the request
// immediately, even when other approvers have already said yes and even when
// more approvers exist who might have said yes too.
func TestDecide_OneRejectionIsFinal(t *testing.T) {
	svc, repos := newTestService(t)
	seedSpec(t, repos, testTenant, testSpec(true))

	r := mkApprovalRequest(testTenant, "dev@example.com", 3, false)
	created, err := svc.RequestApproval(ctxFor(testTenant), r)
	require.NoError(t, err)

	_, err = svc.Decide(ctxFor(testTenant), testTenant, created.ID, govern.Response{Principal: "sre-1@example.com", Approved: true})
	require.NoError(t, err)

	rejected, err := svc.Decide(ctxFor(testTenant), testTenant, created.ID, govern.Response{Principal: "sre-2@example.com", Approved: false, Comment: "not safe right now"})
	require.NoError(t, err)
	assert.Equal(t, govern.ApprovalRejected, rejected.State)
	assert.NotNil(t, rejected.DecidedAt)

	// A third approver trying to vote afterward is rejected outright — the
	// request is no longer pending.
	_, err = svc.Decide(ctxFor(testTenant), testTenant, created.ID, govern.Response{Principal: "sre-3@example.com", Approved: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrConflict)
}

// TestDecide_MinApprovalsMustAllBeReached proves a single approval is not
// enough when MinApprovals demands more, and that the request reaches
// ApprovalApproved only once the threshold is met.
func TestDecide_MinApprovalsMustAllBeReached(t *testing.T) {
	svc, repos := newTestService(t)
	seedSpec(t, repos, testTenant, testSpec(true))

	r := mkApprovalRequest(testTenant, "dev@example.com", 2, false)
	created, err := svc.RequestApproval(ctxFor(testTenant), r)
	require.NoError(t, err)

	partial, err := svc.Decide(ctxFor(testTenant), testTenant, created.ID, govern.Response{Principal: "sre-1@example.com", Approved: true})
	require.NoError(t, err)
	assert.Equal(t, govern.ApprovalPending, partial.State)

	final, err := svc.Decide(ctxFor(testTenant), testTenant, created.ID, govern.Response{Principal: "sre-2@example.com", Approved: true})
	require.NoError(t, err)
	assert.Equal(t, govern.ApprovalApproved, final.State)
	assert.Equal(t, 2, final.ApprovalCount())
}

// TestDecide_ExpiredRequestFailsClosed proves a request past its deadline can
// no longer be decided, and that the expiry itself is persisted so future
// callers see it too.
func TestDecide_ExpiredRequestFailsClosed(t *testing.T) {
	svc, repos := newTestService(t)
	seedSpec(t, repos, testTenant, testSpec(true))

	// govern.Request.Decide checks expiry against the real wall clock (it
	// takes no injected time), so ExpiresAt must be in the past relative to
	// time.Now(), not this test's fixed testNow.
	r := mkApprovalRequest(testTenant, "dev@example.com", 1, false)
	r.ID = core.NewID("apr")
	r.State = govern.ApprovalPending
	r.RequestedAt = time.Now().UTC().Add(-2 * time.Hour)
	r.ExpiresAt = time.Now().UTC().Add(-time.Hour) // already overdue
	require.NoError(t, repos.Approvals.Create(ctxFor(testTenant), r))

	_, err := svc.Decide(ctxFor(testTenant), testTenant, r.ID, govern.Response{Principal: "sre-1@example.com", Approved: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrPreconditionOff)

	fetched, err := svc.GetApproval(ctxFor(testTenant), testTenant, r.ID)
	require.NoError(t, err)
	assert.Equal(t, govern.ApprovalExpired, fetched.State)
}

func TestExpireOverdueApprovals(t *testing.T) {
	svc, repos := newTestService(t)
	require.NoError(t, repos.Approvals.Create(ctxFor(testTenant), govern.Request{
		ID: core.NewID("apr"), TenantID: testTenant, SubjectKind: govern.SubjectRecommendation, SubjectID: core.NewID("rec"),
		State: govern.ApprovalPending, RequestedAt: testNow.Add(-3 * time.Hour), ExpiresAt: testNow.Add(-time.Hour), MinApprovals: 1,
	}))
	n, err := svc.ExpireOverdueApprovals(ctxFor(testTenant))
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}
