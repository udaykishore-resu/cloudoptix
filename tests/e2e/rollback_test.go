package e2e_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestAutomaticRollbackOnRegression proves the platform's most important
// safety property end to end: a change that executes successfully but then
// regresses a critical validation check is undone without a human, the
// estate returns to exactly its prior state, and the savings the change was
// supposed to produce are recorded as lost rather than quietly counted.
//
// The regression is injected by tightening the plan's own validation plan
// after execution — adding a check the post-change world cannot satisfy and
// naming it in AutoRollbackOn. That is a real configuration a tenant can
// write, not a stubbed-out service: everything downstream of it (the
// verdict, the rollback decision, the executor's reverse steps, the savings
// record) is the production path.
//
// Traceability: REQ-AUTO-007, REQ-SAVE-004, SPEC-AUTO-004.
func TestAutomaticRollbackOnRegression(t *testing.T) {
	a, seeded := seed(t)
	ctx := tenantCtx(seeded.TenantID)
	svcs := a.Services

	rec := pickExecutable(t, ctx, a, seeded.TenantID)
	costBefore := a.Estate.TotalMonthlyCost()

	plan, err := svcs.Automation.PlanExecution(ctx, seeded.TenantID, rec.ID,
		ports.PlanOptions{RequestedBy: "rollback-e2e"})
	require.NoError(t, err)
	require.NotNil(t, plan.Rollback)
	require.True(t, plan.Rollback.Feasible, "this test needs a reversible change to reverse")

	if plan.State == execute.PlanAwaitingApproval {
		requests, err := a.Repositories.Approvals.ListBySubject(ctx, seeded.TenantID,
			govern.SubjectExecutionPlan, plan.ID)
		require.NoError(t, err)
		require.NotEmpty(t, requests)
		_, err = svcs.Governance.Decide(ctx, seeded.TenantID, requests[0].ID, govern.Response{
			Principal: "approver@shopfleet.example", Role: core.RoleTenantAdmin,
			Approved: true, Comment: "Approved; we expect this one to regress.", At: time.Now().UTC(),
		})
		require.NoError(t, err)
	}

	executed, err := svcs.Automation.Execute(ctx, seeded.TenantID, plan.ID, "rollback-e2e")
	require.NoError(t, err)
	require.Equal(t, execute.PlanExecuted, executed.State, "reason: %s", executed.StateReason)

	costAfterExecute := a.Estate.TotalMonthlyCost()
	require.True(t, costAfterExecute.LessThan(costBefore),
		"the change must actually have taken effect before there is anything to roll back")

	// Tighten the validation plan the way a tenant who cares about latency
	// would: a critical check the post-change world cannot pass, named in
	// AutoRollbackOn so its failure reverses the change rather than merely
	// alerting. MinSamples is zeroed so the check is judged rather than
	// dismissed as insufficient telemetry — the point under test is the
	// rollback path, not the sample-count guard, which has its own tests.
	executed.Validation = execute.ValidationPlan{
		ObservationWindow: time.Minute,
		BaselineWindow:    time.Hour,
		MinSamples:        0,
		Checks: []execute.ValidationCheck{{
			Name:      "cost-must-have-collapsed",
			Metric:    "monthly_cost",
			Statistic: "avg",
			// "improved_by_pct" with a threshold no real change can reach:
			// the observed cost would have to be better than baseline by
			// 10,000%. An absolute-threshold check would risk passing
			// vacuously when the observation window has no billed data yet
			// and the observed value falls back to a predicted zero; a
			// relative one cannot.
			Comparison: "improved_by_pct",
			Threshold:  10_000,
			Critical:   true,
			Reason:     "deliberately unsatisfiable, standing in for a critical regression",
		}},
		AutoRollbackOn: []string{"cost-must-have-collapsed"},
	}
	require.NoError(t, a.Repositories.Executions.UpdatePlan(ctx, executed))

	result, err := svcs.Automation.Validate(ctx, seeded.TenantID, plan.ID)
	require.NoError(t, err, "validation itself must succeed even when its verdict is failure")

	t.Run("the verdict is failure and rollback was triggered", func(t *testing.T) {
		assert.Equal(t, execute.VerdictFailure, result.Verdict, "explanation: %s", result.Explanation)
		assert.True(t, result.RollbackTriggered,
			"a critical check named in AutoRollbackOn must trigger a rollback, not an alert")
		assert.NotEmpty(t, result.RollbackReason)
		require.Len(t, result.Checks, 1)
		assert.False(t, result.Checks[0].Passed)
		assert.True(t, result.Checks[0].Critical)
	})

	t.Run("the plan is rolled back", func(t *testing.T) {
		rolled, err := svcs.Automation.GetPlan(ctx, seeded.TenantID, plan.ID)
		require.NoError(t, err)
		assert.Equal(t, execute.PlanRolledBack, rolled.State,
			"plan ended as %s: %s", rolled.State, rolled.StateReason)
		require.NotNil(t, rolled.Rollback)
		require.NotEmpty(t, rolled.Rollback.Steps)
		// A completed reverse step is marked StepRolledBack, not
		// StepSucceeded: the distinction is what lets the audit trail say
		// which steps undid a change rather than made one.
		for _, step := range rolled.Rollback.Steps {
			assert.Equal(t, execute.StepRolledBack, step.State,
				"rollback step %q ended as %s: %s", step.Name, step.State, step.Error)
		}
	})

	t.Run("the estate is restored to its pre-change state", func(t *testing.T) {
		restored := a.Estate.TotalMonthlyCost()
		// Exact, not approximate. The rollback replays the snapshot the plan
		// captured before mutating, so the estate must return to the same
		// number to the micro — "close enough" would hide a rollback that
		// restored a rounded or recomputed value instead of the captured one.
		assert.Equal(t, costBefore.Micros(), restored.Micros(),
			"estate was %s before, %s after execution, %s after rollback",
			costBefore.Format(), costAfterExecute.Format(), restored.Format())
	})

	t.Run("the savings record is marked lost", func(t *testing.T) {
		record, err := a.Repositories.Savings.Get(ctx, seeded.TenantID, rec.ID)
		require.NoError(t, err)
		assert.True(t, record.Lost,
			"a rolled-back change produced no saving; recording it as anything else overstates the platform's own results")
		assert.NotEmpty(t, record.LostReason)
		assert.True(t, record.RealizedMonthly.IsZero(),
			"nothing was realized: realized is %s", record.RealizedMonthly.Format())
	})

	t.Run("the funnel does not count the reversed saving as realized", func(t *testing.T) {
		funnel, err := svcs.Automation.Funnel(ctx, seeded.TenantID, core.PeriodOfDays(time.Now().UTC(), 30))
		require.NoError(t, err)
		assert.True(t, funnel.Realized.IsZero(),
			"the only executed change was rolled back, so realized savings must be zero, not %s",
			funnel.Realized.Format())
		assert.NotEmpty(t, funnel.Leakage,
			"value lost between rungs is the funnel's actionable part and must be reported")
	})

	t.Run("the recommendation is not left looking open and healthy", func(t *testing.T) {
		after, err := svcs.Optimization.Get(ctx, seeded.TenantID, rec.ID)
		require.NoError(t, err)
		assert.NotEqual(t, optimize.StatusValidated, after.Status,
			"a rolled-back change must never read as validated")
	})

	t.Run("the learning loop recorded the failure", func(t *testing.T) {
		outcomes, err := a.Repositories.Savings.ListOutcomes(ctx, seeded.TenantID, rec.Finding.RuleID, 10)
		require.NoError(t, err)
		require.NotEmpty(t, outcomes, "a judged validation must produce an outcome for the learning corpus")
		var sawFailure bool
		for _, o := range outcomes {
			if o.Verdict == execute.VerdictFailure {
				sawFailure = true
			}
		}
		assert.True(t, sawFailure, "the recorded outcome must reflect the failure, not the prediction")
	})
}
