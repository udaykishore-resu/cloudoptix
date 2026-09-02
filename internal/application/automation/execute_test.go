package automation

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestExecute_HappyPathRunsEverySteAndAdvancesSavings drives a whole plan
// against the real awssim simulator end to end: approve, execute, confirm
// the instance actually resized in the simulated estate, and confirm the
// savings ladder advanced to StageExecuted.
func TestExecute_HappyPathRunsEveryStepAndAdvancesSavings(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	plan := h.planAndApprove(t, rec.ID)

	executed, err := h.svc.Execute(ctxFor(testTenant), testTenant, plan.ID, "operator@example.com")
	require.NoError(t, err)
	assert.Equal(t, execute.PlanExecuted, executed.State)
	for _, step := range executed.Steps {
		assert.Equal(t, execute.StepSucceeded, step.State, "step %s should have succeeded", step.Name)
	}
	assert.Equal(t, "m5.large", h.estate.EC2Instances[testInstanceID].InstanceType)

	sav, err := h.repos.Savings.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.StageExecuted, sav.Stage)

	updatedRec, err := h.repos.Recommendations.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, optimize.StatusExecuted, updatedRec.Status)

	// A snapshot must have been persisted so a later Rollback call has
	// something to restore from.
	snap, err := h.repos.Executions.GetSnapshot(ctxFor(testTenant), testTenant, plan.ID, res.ID)
	require.NoError(t, err)
	assert.Equal(t, "m5.4xlarge", snap.Attributes["instance_type"])
}

// TestExecute_RefusesWithoutApproval proves Execute will not run a plan that
// still needs a human decision, even though the plan's own persisted state
// was PlanAwaitingApproval and nobody has touched it since.
func TestExecute_RefusesWithoutApproval(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	plan, err := h.svc.PlanExecution(ctxFor(testTenant), testTenant, rec.ID, ports.PlanOptions{RequestedBy: "dev@example.com"})
	require.NoError(t, err)
	require.Equal(t, execute.PlanAwaitingApproval, plan.State)

	_, err = h.svc.Execute(ctxFor(testTenant), testTenant, plan.ID, "operator@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrPreconditionOff)

	stillWaiting, err := h.repos.Executions.GetPlan(ctxFor(testTenant), testTenant, plan.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.PlanFailed, stillWaiting.State, "a refused execution must not be left silently pending forever")
}

// TestExecute_MidPlanFailureTriggersAutomaticRollback proves that a step
// failing after exhausting its retries causes everything already applied to
// be reversed, restoring the simulated instance to its pre-change type.
func TestExecute_MidPlanFailureTriggersAutomaticRollback(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)
	rec.Action = "fake.always_fails_verify"
	require.NoError(t, h.repos.Recommendations.Update(ctxFor(testTenant), rec))

	mutated := false
	fake := &fakeExecutor{
		action: "fake.always_fails_verify",
		planFn: func(in ports.ExecutionPlanInput) (execute.Plan, error) {
			return execute.Plan{
				ID: core.NewID("plan"), TenantID: in.TenantID, RecommendationID: in.Recommendation.ID,
				Action: "fake.always_fails_verify", AccountID: in.Account.AccountID,
				Region: in.Resource.Region, Environment: in.Resource.Environment, ResourceIDs: []core.ID{in.Resource.ID},
				Steps: []execute.Step{
					{ID: core.NewID("step"), Ordinal: 1, Kind: execute.StepSnapshot, Name: "snapshot", AbortOnFailure: true},
					{ID: core.NewID("step"), Ordinal: 2, Kind: execute.StepMutate, Name: "mutate", AbortOnFailure: true},
				},
				Rollback: &execute.RollbackPlan{ID: core.NewID("rbplan"), Feasible: true, Steps: []execute.Step{
					{ID: core.NewID("step"), Kind: execute.StepMutate, Name: "undo-mutate"},
				}},
				ExpectedMonthlySaving: in.Recommendation.EstimatedMonthlySaving, State: execute.PlanDraft,
			}, nil
		},
		applyFn: func(step execute.Step) (map[string]any, error) {
			if step.Name == "mutate" {
				mutated = true
				return nil, errors.New("simulated permanent AWS failure")
			}
			return map[string]any{}, nil
		},
		rollbackFn: func(step execute.Step) error {
			mutated = false
			return nil
		},
	}
	h.svc.d.Executors["fake.always_fails_verify"] = fake

	plan := h.planAndApprove(t, rec.ID)

	_, err := h.svc.Execute(ctxFor(testTenant), testTenant, plan.ID, "operator@example.com")
	require.Error(t, err, "Execute must report the failure even though it rolled back cleanly")

	final, err := h.repos.Executions.GetPlan(ctxFor(testTenant), testTenant, plan.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.PlanRolledBack, final.State)
	assert.False(t, mutated, "rollback must have reversed the mutation")

	sav, err := h.repos.Savings.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.True(t, sav.Lost, "a rolled-back change's saving must be marked lost, not left dangling")

	updatedRec, err := h.repos.Recommendations.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, optimize.StatusRolledBack, updatedRec.Status)
}

// TestExecute_StepIdempotencyUnderRetry proves a step that fails twice with
// a retryable error and succeeds on the third attempt is retried with the
// same idempotency key and parameters, and the plan proceeds normally —
// exactly the "network timeout, but the call actually landed" case
// idempotency exists for.
func TestExecute_StepIdempotencyUnderRetry(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)
	rec.Action = "fake.flaky"
	require.NoError(t, h.repos.Recommendations.Update(ctxFor(testTenant), rec))

	attempts := 0
	var seenKeys []string
	fake := &fakeExecutor{
		action: "fake.flaky",
		planFn: func(in ports.ExecutionPlanInput) (execute.Plan, error) {
			return execute.Plan{
				ID: core.NewID("plan"), TenantID: in.TenantID, RecommendationID: in.Recommendation.ID,
				Action: "fake.flaky", AccountID: in.Account.AccountID, Region: in.Resource.Region,
				Environment: in.Resource.Environment, ResourceIDs: []core.ID{in.Resource.ID},
				Steps: []execute.Step{
					{ID: core.NewID("step"), Ordinal: 1, Kind: execute.StepSnapshot, Name: "snapshot", AbortOnFailure: true},
					{ID: core.NewID("step"), Ordinal: 2, Kind: execute.StepMutate, Name: "mutate", IdempotencyKey: "fixed-key-1", AbortOnFailure: true},
				},
				Rollback:              &execute.RollbackPlan{ID: core.NewID("rbplan"), Feasible: true},
				ExpectedMonthlySaving: in.Recommendation.EstimatedMonthlySaving, State: execute.PlanDraft,
			}, nil
		},
		applyFn: func(step execute.Step) (map[string]any, error) {
			if step.Name != "mutate" {
				return map[string]any{}, nil
			}
			attempts++
			seenKeys = append(seenKeys, step.IdempotencyKey)
			if attempts < 3 {
				return nil, core.NewError(core.ErrThrottled, "throttled", "simulated throttling")
			}
			return map[string]any{"applied": true}, nil
		},
	}
	h.svc.d.Executors["fake.flaky"] = fake

	plan := h.planAndApprove(t, rec.ID)
	executed, err := h.svc.Execute(ctxFor(testTenant), testTenant, plan.ID, "operator@example.com")
	require.NoError(t, err)
	assert.Equal(t, execute.PlanExecuted, executed.State)
	assert.Equal(t, 3, attempts, "the mutate step should have been retried until it succeeded")
	for _, k := range seenKeys {
		assert.Equal(t, "fixed-key-1", k, "every retry must carry the same idempotency key")
	}

	var mutateStep execute.Step
	for _, s := range executed.Steps {
		if s.Name == "mutate" {
			mutateStep = s
		}
	}
	assert.Equal(t, execute.StepSucceeded, mutateStep.State)
	assert.Equal(t, 3, mutateStep.Attempts)
}
