package automation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/awssim"
	"github.com/udaykishore-resu/cloudoptix/internal/application/governance"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// autoExecuteGovernance wraps the real governance.Service, embedding it so
// every method except Evaluate is the genuine implementation (deny-bias
// fold, fail-closed defaults, the lot), and overrides Evaluate to report
// EffectAutoExecute. This exists solely to test ProcessAutonomous's own
// caps and gating logic in isolation from the domain-level defect
// documented in this package's autonomous.go and in governance's package
// doc: govern.Evaluate can never actually return EffectAutoExecute for a
// policy that passed govern.Policy.Validate, so nothing in this codebase
// can otherwise reach the AutoExecute path to test it. What this type does
// NOT change is any of automation's own decisions — the concurrency cap,
// the impact budget, the maintenance-window gate — which is exactly what
// these tests verify.
type autoExecuteGovernance struct{ *governance.Service }

func (g autoExecuteGovernance) Evaluate(ctx context.Context, tenant core.TenantID, recID core.ID) (govern.Decision, error) {
	d, err := g.Service.Evaluate(ctx, tenant, recID)
	if err != nil {
		return d, err
	}
	d.Effect = govern.EffectAutoExecute
	d.RequiresApproval = false
	return d, nil
}

func newAutoExecuteHarness(t *testing.T) *testHarness {
	t.Helper()
	h := newHarness(t)
	d := h.svc.d
	d.Governance = autoExecuteGovernance{h.gov}
	svc, err := NewService(d)
	require.NoError(t, err)
	h.svc = svc
	return h
}

// seedAutoExecutableEC2 seeds a distinct simulated instance and a matching
// recommendation already flagged AutoExecutable — standing in for what a
// prior, correctly-functioning governance evaluation would have set on the
// recommendation (see autoExecuteGovernance's doc comment for why this has
// to be set directly in these tests rather than produced by the real
// evaluator).
func (h *testHarness) seedAutoExecutableEC2(t *testing.T, nativeID string, monthlySaving core.Money) optimize.Recommendation {
	t.Helper()
	h.estate.EC2Instances[nativeID] = &awssim.EC2Instance{
		Base:         awssim.Base{ID: nativeID, Region: testRegion, AZ: "us-east-1a", State: cloud.StateRunning, Tags: core.Tags{}},
		InstanceType: "m5.4xlarge", Platform: "linux",
	}
	res := cloud.Resource{
		ID: core.NewID("res"), TenantID: testTenant, AccountID: testAccountID, Region: testRegion,
		Kind: cloud.KindEC2Instance, NativeID: nativeID, State: cloud.StateRunning,
		Environment: core.EnvDevelopment, EnvironmentSource: core.ProvenanceConfirmed,
		MonthlyCost: core.USDollars(500), FirstSeenAt: testNow, LastSeenAt: testNow,
	}
	_, err := h.repos.Resources.UpsertBatch(ctxFor(testTenant), testTenant, []cloud.Resource{res})
	require.NoError(t, err)

	rec := h.seedRecommendation(t, res)
	rec.EstimatedMonthlySaving = monthlySaving
	rec.AutoExecutable = true
	require.NoError(t, h.repos.Recommendations.Update(ctxFor(testTenant), rec))
	return rec
}

func TestProcessAutonomous_DisabledInSpecDoesNothing(t *testing.T) {
	h := newAutoExecuteHarness(t)
	sp := defaultSpec()
	sp.Automation.Enabled = false
	h.seedSpec(t, sp)
	h.seedAutoExecutableEC2(t, "i-auto-1", core.USDollars(150))

	result, err := h.svc.ProcessAutonomous(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Considered)
	assert.Equal(t, 0, result.Executed)
}

// TestProcessAutonomous_RespectsMonthlyImpactCap proves a candidate whose
// saving would push the tenant's declared MaxMonthlyImpact over budget is
// skipped rather than executed, even though nothing else about it is
// disqualifying.
func TestProcessAutonomous_RespectsMonthlyImpactCap(t *testing.T) {
	h := newAutoExecuteHarness(t)
	sp := defaultSpec()
	sp.Automation.MaxMonthlyImpact = 100 // dollars; the candidate below claims 150
	h.seedSpec(t, sp)
	h.seedAutoExecutableEC2(t, "i-auto-1", core.USDollars(150))

	result, err := h.svc.ProcessAutonomous(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Considered)
	assert.Equal(t, 0, result.Executed)
	assert.Equal(t, 1, result.SkipReasons["monthly_impact_cap_reached"])
}

// TestProcessAutonomous_RespectsConcurrencyCap proves that a change already
// in flight (simulating a previous cycle's plan still executing) counts
// against MaxConcurrentChanges and blocks a new candidate from starting,
// even though the new candidate is otherwise fully eligible.
func TestProcessAutonomous_RespectsConcurrencyCap(t *testing.T) {
	h := newAutoExecuteHarness(t)
	sp := defaultSpec()
	sp.Automation.MaxConcurrentChanges = 1
	h.seedSpec(t, sp)
	rec := h.seedAutoExecutableEC2(t, "i-auto-1", core.USDollars(150))

	// Simulate one change already mid-flight from an earlier cycle.
	require.NoError(t, h.repos.Executions.CreatePlan(ctxFor(testTenant), execute.Plan{
		ID: core.NewID("plan"), TenantID: testTenant, RecommendationID: core.NewID("rec"),
		Action: optimize.ActionResizeInstance, State: execute.PlanExecuting,
		Rollback:  &execute.RollbackPlan{ID: core.NewID("rbplan"), Feasible: true},
		Steps:     []execute.Step{{ID: core.NewID("step"), Kind: execute.StepMutate}},
		CreatedAt: testNow,
	}))

	result, err := h.svc.ProcessAutonomous(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Considered)
	assert.Equal(t, 0, result.Executed)
	assert.Equal(t, 1, result.SkipReasons["concurrency_cap_reached"])

	// The candidate itself must be untouched.
	unchanged, err := h.repos.Recommendations.Get(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, optimize.StatusOpen, unchanged.Status)
}

// TestProcessAutonomous_OutsideMaintenanceWindowIsSkipped proves a tenant
// that declares maintenance windows at all only has autonomous changes run
// inside one — testNow here deliberately falls outside every declared
// window.
func TestProcessAutonomous_OutsideMaintenanceWindowIsSkipped(t *testing.T) {
	h := newAutoExecuteHarness(t)
	sp := defaultSpec()
	sp.Automation.MaintenanceWindows = []spec.MaintenanceWindow{
		{Name: "weekend-only", Days: []string{"saturday", "sunday"}, StartUTC: "00:00", DurationMinutes: 1440},
	}
	h.seedSpec(t, sp) // testNow is a Monday
	h.seedAutoExecutableEC2(t, "i-auto-1", core.USDollars(150))

	result, err := h.svc.ProcessAutonomous(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Executed)
	assert.Equal(t, 1, result.SkipReasons["outside_maintenance_window"])
}

// TestProcessAutonomous_ExecutesInsideMaintenanceWindow is the positive
// counterpart: the same declared window, but one that actually covers
// testNow, lets the change run end to end.
func TestProcessAutonomous_ExecutesInsideMaintenanceWindow(t *testing.T) {
	h := newAutoExecuteHarness(t)
	sp := defaultSpec()
	sp.Automation.MaintenanceWindows = []spec.MaintenanceWindow{
		{Name: "monday-all-day", Days: []string{"monday"}, StartUTC: "00:00", DurationMinutes: 1440},
	}
	h.seedSpec(t, sp) // testNow is a Monday
	h.seedAutoExecutableEC2(t, "i-auto-1", core.USDollars(150))

	result, err := h.svc.ProcessAutonomous(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Executed)
	assert.True(t, result.MonthlySaving.Units() > 0)
	assert.Equal(t, "m5.large", h.estate.EC2Instances["i-auto-1"].InstanceType)
}
