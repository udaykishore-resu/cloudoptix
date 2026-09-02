package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func executeApprovedPlan(t *testing.T, h *testHarness, rec optimize.Recommendation) execute.Plan {
	t.Helper()
	plan := h.planAndApprove(t, rec.ID)
	executed, err := h.svc.Execute(ctxFor(testTenant), testTenant, plan.ID, "operator@example.com")
	require.NoError(t, err)
	require.Equal(t, execute.PlanExecuted, executed.State)
	return executed
}

// TestValidate_CriticalRegressionTriggersAutoRollback proves that a critical
// validation check failing, when the tenant's specification enabled
// Automation.AutoRollback, causes Validate to reverse the change itself —
// restoring the simulated instance to its pre-change type — rather than
// merely reporting the failure and waiting for a human.
func TestValidate_CriticalRegressionTriggersAutoRollback(t *testing.T) {
	h := newHarness(t)
	sp := defaultSpec()
	sp.Automation.AutoRollback = true
	h.seedSpec(t, sp)
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	executed := executeApprovedPlan(t, h, rec)
	require.NotEmpty(t, executed.Validation.AutoRollbackOn, "AutoRollback must have populated at least one critical check name")

	require.NoError(t, h.repos.Metrics.SaveSummaries(ctxFor(testTenant), testTenant, []ports.ResourceMetrics{{
		ResourceID: res.ID, TenantID: testTenant,
		ErrorRate: &core.Percentiles{P99: 0.5, Samples: 100, Coverage: 1}, // way above the 5% threshold
	}}))

	result, err := h.svc.Validate(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.VerdictFailure, result.Verdict)
	assert.True(t, result.RollbackTriggered)

	final, err := h.repos.Executions.GetPlan(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.PlanRolledBack, final.State)
	assert.Equal(t, "m5.4xlarge", h.estate.EC2Instances[testInstanceID].InstanceType, "the resize must have been undone")

	sav, err := h.repos.Savings.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.True(t, sav.Lost)

	outcomes, err := h.repos.Savings.ListOutcomes(ctxFor(testTenant), testTenant, rec.Finding.RuleID, 0)
	require.NoError(t, err)
	require.Len(t, outcomes, 1, "a judged validation must feed the learning loop exactly one outcome")
	assert.Equal(t, execute.VerdictFailure, outcomes[0].Verdict)
	assert.True(t, outcomes[0].RolledBack)
}

// TestValidate_CriticalFailureWithoutAutoRollbackJustFails proves the same
// critical failure, when the tenant did NOT enable Automation.AutoRollback,
// fails the plan and leaves the change in place for a human to look at,
// rather than reversing it unasked.
func TestValidate_CriticalFailureWithoutAutoRollbackJustFails(t *testing.T) {
	h := newHarness(t)
	sp := defaultSpec()
	sp.Automation.AutoRollback = false
	h.seedSpec(t, sp)
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	executed := executeApprovedPlan(t, h, rec)
	assert.Empty(t, executed.Validation.AutoRollbackOn)

	require.NoError(t, h.repos.Metrics.SaveSummaries(ctxFor(testTenant), testTenant, []ports.ResourceMetrics{{
		ResourceID: res.ID, TenantID: testTenant,
		ErrorRate: &core.Percentiles{P99: 0.5, Samples: 100, Coverage: 1},
	}}))

	result, err := h.svc.Validate(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.VerdictFailure, result.Verdict)
	assert.False(t, result.RollbackTriggered)

	final, err := h.repos.Executions.GetPlan(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.PlanFailed, final.State)
	assert.Equal(t, "m5.large", h.estate.EC2Instances[testInstanceID].InstanceType, "without AutoRollback the change stays in place")
}

// TestValidate_SuccessAdvancesToRealized proves that every check passing
// carries a savings record all the way to StageRealized in the same call —
// not merely to StageValidated — because the measurement backing a full
// success verdict is itself the billing confirmation that stage requires.
func TestValidate_SuccessAdvancesToRealized(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	executed := executeApprovedPlan(t, h, rec)

	require.NoError(t, h.repos.Metrics.SaveSummaries(ctxFor(testTenant), testTenant, []ports.ResourceMetrics{{
		ResourceID: res.ID, TenantID: testTenant,
		ErrorRate: &core.Percentiles{P99: 0.01, Samples: 50, Coverage: 1},
	}}))

	result, err := h.svc.Validate(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.VerdictSuccess, result.Verdict)

	final, err := h.repos.Executions.GetPlan(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.PlanValidated, final.State)

	sav, err := h.repos.Savings.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.StageRealized, sav.Stage)

	updatedRec, err := h.repos.Recommendations.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, optimize.StatusValidated, updatedRec.Status)
}

// TestValidate_InsufficientDataStaysInconclusive proves that with no metric
// or cost source wired at all, Validate does not declare success on
// nothing — it stays PlanValidating for a later attempt once real
// telemetry exists.
func TestValidate_InsufficientDataStaysInconclusive(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	executed := executeApprovedPlan(t, h, rec)
	// Deliberately no Metrics.SaveSummaries call: every check has Samples 0.

	result, err := h.svc.Validate(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.VerdictInconclusive, result.Verdict)

	final, err := h.repos.Executions.GetPlan(ctxFor(testTenant), testTenant, executed.ID)
	require.NoError(t, err)
	assert.Equal(t, execute.PlanValidating, final.State)

	outcomes, err := h.repos.Savings.ListOutcomes(ctxFor(testTenant), testTenant, rec.Finding.RuleID, 0)
	require.NoError(t, err)
	assert.Empty(t, outcomes, "an inconclusive verdict must not be recorded as a learned outcome")
}
