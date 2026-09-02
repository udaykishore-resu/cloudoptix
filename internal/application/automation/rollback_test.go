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

// TestRollback_ManualCallReversesAnExecutedPlan proves a human-initiated
// Rollback on a plan that finished successfully (no failure, no validation
// even run yet) works the same way an automatic one does.
func TestRollback_ManualCallReversesAnExecutedPlan(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	executed := executeApprovedPlan(t, h, rec)
	assert.Equal(t, "m5.large", h.estate.EC2Instances[testInstanceID].InstanceType)

	rolledBack, err := h.svc.Rollback(ctxFor(testTenant), testTenant, executed.ID, "customer requested manual rollback", "sre@example.com")
	require.NoError(t, err)
	assert.Equal(t, execute.PlanRolledBack, rolledBack.State)
	assert.Equal(t, "m5.4xlarge", h.estate.EC2Instances[testInstanceID].InstanceType)

	updatedRec, err := h.repos.Recommendations.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, optimize.StatusRolledBack, updatedRec.Status)
}

// TestRollback_FailedStepStopsAndNeverRetriesBlindly proves that when a
// rollback step itself fails after exhausting its own retries, the plan
// lands in PlanRollbackFailed — a terminal state — and a second call to
// Rollback is refused outright rather than the platform silently trying
// again on its own.
func TestRollback_FailedStepStopsAndNeverRetriesBlindly(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)
	rec.Action = "fake.unrollbackable_in_practice"
	require.NoError(t, h.repos.Recommendations.Update(ctxFor(testTenant), rec))

	rollbackAttempts := 0
	fake := &fakeExecutor{
		action: "fake.unrollbackable_in_practice",
		planFn: func(in ports.ExecutionPlanInput) (execute.Plan, error) {
			return execute.Plan{
				ID: core.NewID("plan"), TenantID: in.TenantID, RecommendationID: in.Recommendation.ID,
				Action: "fake.unrollbackable_in_practice", AccountID: in.Account.AccountID,
				Region: in.Resource.Region, Environment: in.Resource.Environment, ResourceIDs: []core.ID{in.Resource.ID},
				Steps: []execute.Step{
					{ID: core.NewID("step"), Ordinal: 1, Kind: execute.StepSnapshot, Name: "snapshot", AbortOnFailure: true},
					{ID: core.NewID("step"), Ordinal: 2, Kind: execute.StepMutate, Name: "mutate", AbortOnFailure: true},
				},
				Rollback: &execute.RollbackPlan{ID: core.NewID("rbplan"), Feasible: true, Steps: []execute.Step{
					{ID: core.NewID("step"), Kind: execute.StepMutate, Name: "undo-mutate", MaxRetries: 1},
				}},
				ExpectedMonthlySaving: in.Recommendation.EstimatedMonthlySaving, State: execute.PlanDraft,
			}, nil
		},
		rollbackFn: func(step execute.Step) error {
			rollbackAttempts++
			return errors.New("simulated AWS API always refuses to undo this")
		},
	}
	h.svc.d.Executors["fake.unrollbackable_in_practice"] = fake

	executed := executeApprovedPlan(t, h, rec)

	_, err := h.svc.Rollback(ctxFor(testTenant), testTenant, executed.ID, "attempting manual rollback", "sre@example.com")
	require.Error(t, err)

	firstAttemptCount := rollbackAttempts
	assert.Greater(t, firstAttemptCount, 0)

	final, err := h.repos.Executions.GetPlan(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.PlanRollbackFailed, final.State)
	assert.True(t, final.State.Terminal(), "PlanRollbackFailed must be a terminal state")

	// A second attempt must be refused outright, not silently retried.
	_, err = h.svc.Rollback(ctxFor(testTenant), testTenant, executed.ID, "trying again", "sre@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrPreconditionOff)
	assert.Equal(t, firstAttemptCount, rollbackAttempts, "a refused second call must not have touched the executor at all")
}
