package auditsvc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestRecord_AppendsAndIsQueryable(t *testing.T) {
	svc, repos := newTestService(t)
	subjectID := core.NewID("rec")

	err := svc.Record(ctxFor(testTenant), ports.AuditEntry{
		Action: string(audit.ActionRecommendationCreated), Outcome: string(audit.OutcomeSuccess),
		Actor: "engine", Machine: true, Subject: "recommendation", SubjectID: subjectID,
		Message: "recommendation created",
	})
	require.NoError(t, err)

	page, err := svc.Query(ctxFor(testTenant), testTenant, ports.AuditQuery{SubjectID: subjectID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, string(audit.ActionRecommendationCreated), page.Items[0].Action)
	assert.Equal(t, "recommendation created", page.Items[0].Message)
	assert.NotEmpty(t, page.Items[0].Hash, "an appended record must come back sealed")

	// Record derives the tenant from the caller's principal, never from a
	// field on the entry — GuardTenant must refuse a mismatched scope.
	_, repoErr := repos.Audit.Query(ctxFor(testTenant), audit.Query{TenantID: "some-other-tenant"})
	assert.Error(t, repoErr)
}

func TestVerify_DelegatesToRepository(t *testing.T) {
	svc, _ := newTestService(t)
	require.NoError(t, svc.Record(ctxFor(testTenant), ports.AuditEntry{
		Action: string(audit.ActionSpecApproved), Outcome: string(audit.OutcomeSuccess), Actor: "admin",
	}))

	result, err := svc.Verify(ctxFor(testTenant), testTenant, time.Time{}, time.Time{})
	require.NoError(t, err)
	verification, ok := result.(audit.ChainVerification)
	require.True(t, ok, "Verify must return the concrete audit.ChainVerification the repository produced")
	assert.True(t, verification.Valid)
	assert.Equal(t, 1, verification.RecordsChecked)
}

func TestTimeline_AssemblesOneChangeInCausalOrder(t *testing.T) {
	svc, repos := newTestService(t)
	ctx := ctxFor(testTenant)
	recA := core.NewID("rec")
	recB := core.NewID("rec")

	append_ := func(action audit.Action, subjectKind string, recID core.ID) {
		_, err := repos.Audit.Append(ctx, audit.Record{
			TenantID: testTenant, Action: action, Outcome: audit.OutcomeSuccess,
			Actor: "engine", SubjectKind: subjectKind, RecommendationID: recID,
			Message: string(action), At: testNow,
		})
		require.NoError(t, err)
	}

	// Interleaved with an unrelated recommendation B, and — because the
	// underlying repository returns records newest-first — deliberately
	// exercising that Timeline must re-sort into oldest-first causal order
	// rather than pass the repository's own order through.
	append_(audit.ActionRecommendationCreated, "recommendation", recA)
	append_(audit.ActionRecommendationCreated, "recommendation", recB)
	append_(audit.ActionPolicyEvaluated, "policy_decision", recA)
	append_(audit.ActionApprovalRequested, "approval", recA)
	append_(audit.ActionApprovalGranted, "approval", recB)
	append_(audit.ActionApprovalGranted, "approval", recA)
	append_(audit.ActionPlanCreated, "execution_plan", recA)
	append_(audit.ActionExecutionStep, "execution_plan", recA)
	append_(audit.ActionValidationResult, "execution_plan", recA)
	append_(audit.ActionCostIngested, "cost", recB) // unrelated noise between recA's remaining records
	append_(audit.ActionRollbackCompleted, "execution_plan", recA)

	timeline, err := svc.Timeline(ctx, testTenant, recA)
	require.NoError(t, err)

	wantActions := []string{
		string(audit.ActionRecommendationCreated),
		string(audit.ActionPolicyEvaluated),
		string(audit.ActionApprovalRequested),
		string(audit.ActionApprovalGranted),
		string(audit.ActionPlanCreated),
		string(audit.ActionExecutionStep),
		string(audit.ActionValidationResult),
		string(audit.ActionRollbackCompleted),
	}
	gotActions := make([]string, len(timeline))
	for i, e := range timeline {
		gotActions[i] = e.Action
	}
	assert.Equal(t, wantActions, gotActions, "the timeline must contain only recA's records, in the order they actually happened")

	for i := 1; i < len(timeline); i++ {
		assert.Less(t, timeline[i-1].Sequence, timeline[i].Sequence, "sequence must be strictly increasing")
	}
}
