package automation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestPlanExecution_NoActivePolicyFailsClosedToApproval proves that, with
// nothing else configured, planning a real recommendation produces a plan
// awaiting human approval rather than one ready to run unattended —
// governance's own fail-closed default (see governance's evaluate_test.go)
// propagating all the way through to what PlanExecution actually persists.
func TestPlanExecution_NoActivePolicyFailsClosedToApproval(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	plan, err := h.svc.PlanExecution(ctxFor(testTenant), testTenant, rec.ID, ports.PlanOptions{RequestedBy: "dev@example.com"})
	require.NoError(t, err)
	assert.Equal(t, execute.PlanAwaitingApproval, plan.State)
	assert.False(t, plan.ApprovalID.IsZero(), "a plan requiring approval must carry an approval id")
	require.NotNil(t, plan.Rollback)
	assert.True(t, plan.Rollback.Feasible)
	assert.NotEmpty(t, plan.Validation.Checks, "PlanExecution must build a validation plan before anything runs")
	assert.Len(t, plan.Steps, 4)

	appr, err := h.repos.Approvals.Get(ctxFor(testTenant), testTenant, plan.ApprovalID)
	require.NoError(t, err)
	assert.Equal(t, govern.SubjectExecutionPlan, appr.SubjectKind)
	assert.Equal(t, plan.ID, appr.SubjectID)
	assert.Equal(t, plan.Rollback.Summary, appr.Context.RollbackPlan)

	// The recommendation must reflect that it is now under review, not left
	// looking like an untouched open item.
	updated, err := h.repos.Recommendations.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, optimize.StatusUnderReview, updated.Status)

	sav, err := h.repos.Savings.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.StagePlanned, sav.Stage)
}

// TestPlanExecution_ProhibitedRecommendationRefusesToPlan proves an
// explicitly prohibited action never even gets a plan built for it — the
// refusal happens before any AWS session is assumed.
func TestPlanExecution_ProhibitedRecommendationRefusesToPlan(t *testing.T) {
	h := newHarness(t)
	sp := defaultSpec()
	sp.Governance.ChangeFreezeWindows = nil
	h.seedSpec(t, sp)
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	// A policy whose only rule prohibits resize_instance outright.
	pol := govern.Policy{
		TenantID: testTenant, Name: "prohibit-resize", DefaultEffect: govern.EffectRequireApproval,
		Rules: []govern.Rule{{
			ID: "no-resize", Effect: govern.EffectProhibit,
			Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionResizeInstance}},
		}},
	}
	saved, err := h.gov.SavePolicy(ctxFor(testTenant), testTenant, pol, "admin@example.com")
	require.NoError(t, err)
	require.NoError(t, h.gov.ActivatePolicy(ctxFor(testTenant), testTenant, saved.ID, "admin@example.com"))

	_, err = h.svc.PlanExecution(ctxFor(testTenant), testTenant, rec.ID, ports.PlanOptions{RequestedBy: "dev@example.com"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
}

// TestPlanExecution_TerminalRecommendationRefused proves a recommendation
// that has already run its course (e.g. already validated) cannot be
// planned again through this call.
func TestPlanExecution_TerminalRecommendationRefused(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)
	rec.Status = optimize.StatusValidated
	require.NoError(t, h.repos.Recommendations.Update(ctxFor(testTenant), rec))

	_, err := h.svc.PlanExecution(ctxFor(testTenant), testTenant, rec.ID, ports.PlanOptions{RequestedBy: "dev@example.com"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrPreconditionOff)
}

// fakeExecutor is a minimal ports.Executor double used to exercise
// automation's own defense-in-depth checks and its retry/rollback machinery
// without depending on awssim's specific action semantics. planFn, applyFn
// and rollbackFn are nil-checked so a test only needs to supply the
// behaviour it cares about.
type fakeExecutor struct {
	action     optimize.ActionType
	planFn     func(ports.ExecutionPlanInput) (execute.Plan, error)
	applyFn    func(step execute.Step) (map[string]any, error)
	rollbackFn func(step execute.Step) error
	applyCalls []string // step names, in call order, including retries
}

func (f *fakeExecutor) Action() optimize.ActionType { return f.action }
func (f *fakeExecutor) RequiredActions() []string   { return nil }
func (f *fakeExecutor) Plan(_ context.Context, in ports.ExecutionPlanInput) (execute.Plan, error) {
	return f.planFn(in)
}
func (f *fakeExecutor) Preflight(context.Context, ports.AWSSession, execute.Plan) error { return nil }
func (f *fakeExecutor) Apply(_ context.Context, _ ports.AWSSession, _ execute.Plan, step execute.Step) (map[string]any, error) {
	f.applyCalls = append(f.applyCalls, step.Name)
	if f.applyFn == nil {
		return map[string]any{}, nil
	}
	return f.applyFn(step)
}
func (f *fakeExecutor) Rollback(_ context.Context, _ ports.AWSSession, _ execute.Plan, step execute.Step) error {
	if f.rollbackFn == nil {
		return nil
	}
	return f.rollbackFn(step)
}

var _ ports.Executor = (*fakeExecutor)(nil)

// TestPlanExecution_RefusesAPlanThatMutatesWithoutASnapshot proves the
// orchestration-layer defense-in-depth check: even though every real
// executor in this codebase is required to build a snapshot before any
// mutate step, PlanExecution independently verifies that invariant itself
// and refuses to persist a plan that violates it, rather than trusting the
// executor unconditionally.
func TestPlanExecution_RefusesAPlanThatMutatesWithoutASnapshot(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)
	rec.Action = "fake.no_snapshot"
	require.NoError(t, h.repos.Recommendations.Update(ctxFor(testTenant), rec))

	badExecutor := &fakeExecutor{
		action: "fake.no_snapshot",
		planFn: func(in ports.ExecutionPlanInput) (execute.Plan, error) {
			return execute.Plan{
				ID: core.NewID("plan"), TenantID: in.TenantID, Action: "fake.no_snapshot",
				Steps:    []execute.Step{{ID: core.NewID("step"), Kind: execute.StepMutate, Name: "mutate", AbortOnFailure: true}},
				Rollback: &execute.RollbackPlan{ID: core.NewID("rbplan"), Feasible: true},
				State:    execute.PlanDraft,
			}, nil
		},
	}
	h.svc.d.Executors["fake.no_snapshot"] = badExecutor

	_, err := h.svc.PlanExecution(ctxFor(testTenant), testTenant, rec.ID, ports.PlanOptions{RequestedBy: "dev@example.com"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
	assert.Contains(t, err.Error(), "snapshot")
}

// TestPlanExecution_RefusesAPlanWithNoRollback proves the same defense in
// depth for a missing rollback plan entirely.
func TestPlanExecution_RefusesAPlanWithNoRollback(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)
	rec.Action = "fake.no_rollback"
	require.NoError(t, h.repos.Recommendations.Update(ctxFor(testTenant), rec))

	badExecutor := &fakeExecutor{
		action: "fake.no_rollback",
		planFn: func(in ports.ExecutionPlanInput) (execute.Plan, error) {
			return execute.Plan{
				ID: core.NewID("plan"), TenantID: in.TenantID, Action: "fake.no_rollback",
				Steps: []execute.Step{{ID: core.NewID("step"), Kind: execute.StepPrecondition, Name: "check"}},
				State: execute.PlanDraft,
			}, nil
		},
	}
	h.svc.d.Executors["fake.no_rollback"] = badExecutor

	_, err := h.svc.PlanExecution(ctxFor(testTenant), testTenant, rec.ID, ports.PlanOptions{RequestedBy: "dev@example.com"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
	assert.Contains(t, err.Error(), "rollback")
}
