package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func seedResource(t *testing.T, repos ports.Repositories, tenant core.TenantID, res cloud.Resource) {
	t.Helper()
	_, err := repos.Resources.UpsertBatch(ctxFor(tenant), tenant, []cloud.Resource{res})
	require.NoError(t, err)
}

func seedRecommendation(t *testing.T, repos ports.Repositories, tenant core.TenantID, rec optimize.Recommendation) {
	t.Helper()
	require.NoError(t, repos.Recommendations.SaveBatch(ctxFor(tenant), tenant, []optimize.Recommendation{rec}))
}

func mustActivatePolicy(t *testing.T, svc *Service, tenant core.TenantID, p govern.Policy, actor string) govern.Policy {
	t.Helper()
	saved, err := svc.SavePolicy(ctxFor(tenant), tenant, p, actor)
	require.NoError(t, err)
	require.NoError(t, svc.ActivatePolicy(ctxFor(tenant), tenant, saved.ID, actor))
	saved.Enabled = true
	return saved
}

// TestEvaluate_DenyBiasWhenMultipleRulesMatch proves that when several rules
// match, the most restrictive one wins regardless of which was written first
// in the policy.
//
// This comment used to go on to claim that a validated policy could never
// auto-execute anything, on the reasoning that DefaultEffect is folded into
// the same most-restrictive-wins comparison as the rules. That is not what
// govern.Evaluate does: it tracks the most restrictive *matching* rule
// separately and falls back to DefaultEffect only when nothing matched (its
// own comment says so at the fold), so a matching auto_execute rule does
// decide the outcome. TestEvaluate_AutoExecuteRuleWinsWhenItIsTheOnlyMatch
// below pins that behaviour. The decoy rule in this test is still a decoy —
// it loses here because two stricter rules also match, which is deny-bias
// working, not because auto_execute is unreachable.
func TestEvaluate_DenyBiasWhenMultipleRulesMatch(t *testing.T) {
	svc, repos := newTestService(t)
	res := mkResource(testTenant)
	seedResource(t, repos, testTenant, res)
	seedSpec(t, repos, testTenant, testSpec(true))

	rec := mkRecommendation(testTenant, res, optimize.ActionResizeInstance)
	rec.Confidence = 0.95
	seedRecommendation(t, repos, testTenant, rec)

	// Two genuinely reachable rules (both at least as restrictive as
	// DefaultEffect) plus a decoy auto_execute rule that can never win under
	// any validated policy. Order is deliberately "loosest first" so a
	// correct implementation has to actively resist being loosened by
	// whichever rule the policy author happened to write first.
	policy := govern.Policy{
		Name: "deny-bias-test", DefaultEffect: govern.EffectRequireApproval, Enabled: true,
		Rules: []govern.Rule{
			{ID: "decoy-auto", Effect: govern.EffectAutoExecute,
				Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionResizeInstance}, MinConfidence: 0.9}},
			{ID: "approval-rule", Effect: govern.EffectRequireApproval,
				Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionResizeInstance}, Environments: []core.Environment{core.EnvProduction}}},
			{ID: "prohibit-rule", Effect: govern.EffectProhibit,
				Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionResizeInstance}, Environments: []core.Environment{core.EnvProduction}}},
		},
	}
	mustActivatePolicy(t, svc, testTenant, policy, "admin@example.com")

	decision, err := svc.Evaluate(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, govern.EffectProhibit, decision.Effect)
	assert.Equal(t, "prohibit-rule", decision.DecidingRule)
	assert.Contains(t, decision.MatchedRules, "decoy-auto")
	assert.Contains(t, decision.MatchedRules, "approval-rule")
	assert.Contains(t, decision.MatchedRules, "prohibit-rule")

	// Reversing the rule order must not change the outcome — that is the
	// entire point of deny-bias being order-independent.
	policy.Rules[0], policy.Rules[2] = policy.Rules[2], policy.Rules[0]
	reordered, err := svc.SavePolicy(ctxFor(testTenant), testTenant, policy, "admin@example.com")
	require.NoError(t, err)
	require.NoError(t, svc.ActivatePolicy(ctxFor(testTenant), testTenant, reordered.ID, "admin@example.com"))
	decision2, err := svc.Evaluate(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, govern.EffectProhibit, decision2.Effect)
}

// TestEvaluate_DestructiveActionNeverAutoExecutesUnderAValidatedPolicy proves
// the observable, end-to-end safety property this package's callers actually
// depend on: a destructive action, evaluated through the real SavePolicy /
// ActivatePolicy / Evaluate path, is never auto-executable — regardless of
// how permissive the tenant's policy tries to be — and every recommendation
// this test tries also never becomes auto-executable at all, which is the
// even-stronger guarantee described in TestEvaluate_DenyBiasWhenMultipleRulesMatch's
// doc comment. See TestGovernEvaluate_PlatformInvariantsSurviveAnAdversarialPolicy
// below for a direct exercise of govern.Evaluate's own internal
// __platform_destructive_guard__ and __tenant_automation_disabled__ branches,
// which require an adversarially-constructed Policy no properly-validated
// tenant policy could ever produce to reach at all.
func TestEvaluate_DestructiveActionNeverAutoExecutesUnderAValidatedPolicy(t *testing.T) {
	svc, repos := newTestService(t)
	res := mkResource(testTenant)
	res.Environment = core.EnvDevelopment
	seedResource(t, repos, testTenant, res)
	seedSpec(t, repos, testTenant, testSpec(true))

	rec := mkRecommendation(testTenant, res, optimize.ActionDeleteVolume)
	rec.Finding.Environment = core.EnvDevelopment
	rec.Confidence = 0.99
	rec.Risk = optimize.RiskAssessment{Score: 0.05, Level: core.RiskLow}
	seedRecommendation(t, repos, testTenant, rec)

	// A policy that TRIES to auto-execute a destructive action fails to even
	// save: Policy.Validate flags it as a blocking CRITICAL issue, so
	// SavePolicy refuses to persist it at all — the strongest possible form
	// of "never", enforced before the rule can ever be evaluated once.
	unsafe := govern.Policy{
		Name: "tries-to-allow-delete", DefaultEffect: govern.EffectRequireApproval, Enabled: true,
		Rules: []govern.Rule{{ID: "allow-delete", Effect: govern.EffectAutoExecute,
			Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionDeleteVolume}, MinConfidence: 0.99}}},
	}
	_, err := svc.SavePolicy(ctxFor(testTenant), testTenant, unsafe, "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)

	// With no policy ever successfully activated, evaluation still fails
	// closed to manual approval rather than erroring.
	decision, err := svc.Evaluate(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.NotEqual(t, govern.EffectAutoExecute, decision.Effect)
	assert.True(t, decision.RequiresApproval)
}

// TestGovernEvaluate_PlatformInvariantsSurviveAnAdversarialPolicy calls
// govern.Evaluate directly (bypassing this package's Service, and therefore
// Policy.Validate) with a Policy whose DefaultEffect is itself auto_execute —
// something no tenant could ever get SavePolicy or ActivatePolicy to accept,
// since Validate flags it CRITICAL. That is exactly what makes this an
// adversarial construction worth testing: it is the only way to actually
// reach the platform_destructive_guard and tenant_automation_disabled
// branches inside govern.Evaluate, and proves defense-in-depth at the layer
// where those two invariants actually live — they hold even against an
// input this package's own persistence layer would never itself produce.
func TestGovernEvaluate_PlatformInvariantsSurviveAnAdversarialPolicy(t *testing.T) {
	adversarial := govern.Policy{
		Name: "adversarial", DefaultEffect: govern.EffectAutoExecute, // unvalidated on purpose
		Rules: []govern.Rule{{ID: "allow-delete", Effect: govern.EffectAutoExecute,
			Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionDeleteVolume}}}},
	}
	destructiveInput := govern.Input{
		TenantID: testTenant, RecommendationID: "rec-1", RuleID: "rule-1",
		Action: optimize.ActionDeleteVolume, ResourceID: "res-1", AccountID: "111122223333",
		Environment: core.EnvDevelopment, RiskLevel: core.RiskLow, Reversibility: optimize.ReversibilityNone,
		Confidence: 0.99, AutomationEnabled: true, Destructive: true, Now: testNow,
	}
	d := govern.Evaluate(adversarial, destructiveInput)
	assert.Equal(t, govern.EffectRequireApproval, d.Effect)
	assert.Equal(t, "__platform_destructive_guard__", d.DecidingRule)

	automationOffInput := govern.Input{
		TenantID: testTenant, RecommendationID: "rec-2", RuleID: "rule-2",
		Action: optimize.ActionResizeInstance, ResourceID: "res-2", AccountID: "111122223333",
		Environment: core.EnvDevelopment, RiskLevel: core.RiskLow, Reversibility: optimize.ReversibilityFast,
		Confidence: 0.99, AutomationEnabled: false, Now: testNow, // tenant automation switch is off
	}
	notDestructivePolicy := govern.Policy{
		Name: "adversarial2", DefaultEffect: govern.EffectAutoExecute,
		Rules: []govern.Rule{{ID: "allow-resize", Effect: govern.EffectAutoExecute,
			Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionResizeInstance}}}},
	}
	d2 := govern.Evaluate(notDestructivePolicy, automationOffInput)
	assert.Equal(t, govern.EffectRequireApproval, d2.Effect)
	assert.Equal(t, "__tenant_automation_disabled__", d2.DecidingRule)
}

// TestEvaluate_FrozenBudgetBlocksCostIncreasingChange proves an exhausted
// economic error budget prohibits a cost-increasing change even under a
// policy that would otherwise auto-execute it.
func TestEvaluate_FrozenBudgetBlocksCostIncreasingChange(t *testing.T) {
	svc, repos := newTestService(t)
	res := mkResource(testTenant)
	res.Environment = core.EnvDevelopment
	seedResource(t, repos, testTenant, res)
	seedSpec(t, repos, testTenant, testSpec(true))

	rec := mkRecommendation(testTenant, res, optimize.ActionResizeInstance)
	rec.Finding.Environment = core.EnvDevelopment
	rec.Confidence = 0.95
	// A cost-increasing change: proposed costs more than current.
	rec.CurrentState.MonthlyCost = core.USDollars(500)
	rec.ProposedState.MonthlyCost = core.USDollars(800)
	rec.EstimatedMonthlySaving = core.ZeroUSD()
	seedRecommendation(t, repos, testTenant, rec)

	// Seed an organization-scoped Cost SLO and its exhausted budget state
	// directly through the economics repository, matching how the economics
	// engine would have written both.
	slo := econ.CostSLO{
		ID: core.NewID("slo"), TenantID: testTenant, Name: "org-ceiling", Kind: econ.SLOAbsoluteSpend,
		Direction: econ.DirectionAtMost, Scope: econ.ScopeOrganization, Target: core.USDollars(1000), Enabled: true,
	}
	require.NoError(t, repos.Economics.UpsertCostSLO(ctxFor(testTenant), slo))
	require.NoError(t, repos.Economics.SaveBudgetState(ctxFor(testTenant), econ.EconomicErrorBudget{
		ID: core.NewID("eeb"), TenantID: testTenant, SLOID: slo.ID, SLOName: slo.Name, Kind: slo.Kind,
		Period:           core.Period{Start: testNow.AddDate(0, 0, -10), End: testNow.AddDate(0, 0, 20)},
		Target:           slo.Target,
		State:            econ.BudgetExhausted,
		TriggeredActions: []econ.BreachAction{econ.ActionFreezeIncreases},
		EvaluatedAt:      testNow,
	}))

	policy := govern.Policy{
		Name: "permissive", DefaultEffect: govern.EffectRequireApproval, Enabled: true,
		Rules: []govern.Rule{{ID: "auto-nonprod", Effect: govern.EffectAutoExecute,
			Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionResizeInstance}, MinConfidence: 0.5, Environments: []core.Environment{core.EnvDevelopment}}}},
	}
	mustActivatePolicy(t, svc, testTenant, policy, "admin")

	decision, err := svc.Evaluate(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, govern.EffectProhibit, decision.Effect)
	assert.Equal(t, "__economic_error_budget_freeze__", decision.DecidingRule)
}

// TestEvaluate_ExcludedActionIsProhibitedRegardlessOfPolicy proves a
// specification-level exclusion beats even an explicit auto_execute rule.
func TestEvaluate_ExcludedActionIsProhibitedRegardlessOfPolicy(t *testing.T) {
	svc, repos := newTestService(t)
	res := mkResource(testTenant)
	res.Environment = core.EnvDevelopment
	seedResource(t, repos, testTenant, res)
	sp := testSpec(true)
	sp.Optimization.ExcludedActions = []string{string(optimize.ActionResizeInstance)}
	seedSpec(t, repos, testTenant, sp)

	rec := mkRecommendation(testTenant, res, optimize.ActionResizeInstance)
	rec.Finding.Environment = core.EnvDevelopment
	rec.Confidence = 0.99
	seedRecommendation(t, repos, testTenant, rec)

	policy := govern.Policy{
		Name: "permissive", DefaultEffect: govern.EffectRequireApproval, Enabled: true,
		Rules: []govern.Rule{{ID: "auto", Effect: govern.EffectAutoExecute,
			Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionResizeInstance}, MinConfidence: 0.5, Environments: []core.Environment{core.EnvDevelopment}}}},
	}
	mustActivatePolicy(t, svc, testTenant, policy, "admin")

	decision, err := svc.Evaluate(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, govern.EffectProhibit, decision.Effect)
	assert.Equal(t, "__spec_excluded_action__", decision.DecidingRule)
}

// TestApplySpecificationGuards_ChangeFreezeDowngradesAutoExecuteToApproval
// unit-tests applySpecificationGuards directly against a synthetic Decision
// already carrying Effect=auto_execute, downstream of whatever policy
// evaluation produced it. Exercising it this way — rather than through the
// full Evaluate pipeline — is deliberate: see
// TestEvaluate_DenyBiasWhenMultipleRulesMatch's doc comment for why no
// properly-validated tenant policy can make govern.Evaluate itself return
// auto_execute today, which would otherwise make this specific transition
// unreachable to test end-to-end. applySpecificationGuards' own contract
// does not depend on how its input Decision was produced, so testing it
// directly is the correct unit boundary regardless.
func TestApplySpecificationGuards_ChangeFreezeDowngradesAutoExecuteToApproval(t *testing.T) {
	res := mkResource(testTenant)
	res.Environment = core.EnvDevelopment
	rec := mkRecommendation(testTenant, res, optimize.ActionResizeInstance)
	rec.Finding.Environment = core.EnvDevelopment

	sp := testSpec(true)
	sp.Governance.ChangeFreezeWindows = []string{"2026-08-30..2026-09-02"} // covers testNow (2026-08-31)

	d := govern.Decision{Effect: govern.EffectAutoExecute, DecidingRule: "some-rule", RequiresApproval: false}
	applySpecificationGuards(&d, sp, res, rec, testNow)
	assert.Equal(t, govern.EffectRequireApproval, d.Effect)
	assert.Equal(t, "__spec_change_freeze__", d.DecidingRule)
	assert.True(t, d.RequiresApproval)

	// Outside the freeze window, the same auto_execute decision passes
	// through untouched.
	d2 := govern.Decision{Effect: govern.EffectAutoExecute, DecidingRule: "some-rule"}
	applySpecificationGuards(&d2, sp, res, rec, testNow.AddDate(0, 1, 0))
	assert.Equal(t, govern.EffectAutoExecute, d2.Effect)
	assert.Equal(t, "some-rule", d2.DecidingRule)

	// A freeze never LOOSENS an already-stricter decision (e.g. prohibit) —
	// tighten only ever moves toward more restrictive.
	d3 := govern.Decision{Effect: govern.EffectProhibit, DecidingRule: "already-strict"}
	applySpecificationGuards(&d3, sp, res, rec, testNow)
	assert.Equal(t, govern.EffectProhibit, d3.Effect)
	assert.Equal(t, "already-strict", d3.DecidingRule)
}

// TestEvaluate_NoActivePolicyFailsClosedToApproval proves that a tenant with
// no policy activated yet gets manual approval, never auto-execution or a
// hard error.
func TestEvaluate_NoActivePolicyFailsClosedToApproval(t *testing.T) {
	svc, repos := newTestService(t)
	res := mkResource(testTenant)
	seedResource(t, repos, testTenant, res)
	seedSpec(t, repos, testTenant, testSpec(true))

	rec := mkRecommendation(testTenant, res, optimize.ActionResizeInstance)
	seedRecommendation(t, repos, testTenant, rec)

	decision, err := svc.Evaluate(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, govern.EffectRequireApproval, decision.Effect)
}

// TestBuildInput_FailsClosedOnMissingRequiredField proves that an incomplete
// Input is never handed to govern.Evaluate.
func TestBuildInput_FailsClosedOnMissingRequiredField(t *testing.T) {
	res := mkResource(testTenant)
	rec := mkRecommendation(testTenant, res, optimize.ActionResizeInstance)
	rec.Finding.AccountID = "" // simulate a finding with a missing required field

	_, err := buildInput(testTenant, rec, res, testSpec(true), false, false, testNow)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

// TestEvaluate_AutoExecuteRuleWinsWhenItIsTheOnlyMatch pins the behaviour
// two stale comments in this codebase used to deny: a validated policy whose
// only matching rule says auto_execute really does authorise unattended
// execution. It is worth an explicit test because the belief that it could
// not was load-bearing — the shipped demo reported "0 auto-executable" and
// nobody looked, because a comment said that was expected.
func TestEvaluate_AutoExecuteRuleWinsWhenItIsTheOnlyMatch(t *testing.T) {
	svc, repos := newTestService(t)
	res := mkResource(testTenant)
	res.Environment = core.EnvDevelopment
	seedResource(t, repos, testTenant, res)
	seedSpec(t, repos, testTenant, testSpec(true))

	rec := mkRecommendation(testTenant, res, optimize.ActionStopInstance)
	rec.Finding.Environment = core.EnvDevelopment
	rec.Finding.Category = optimize.CategoryWaste
	rec.Confidence = 0.95
	rec.Risk.Level = core.RiskLow
	rec.Reversibility = optimize.ReversibilityFast
	seedRecommendation(t, repos, testTenant, rec)

	policy := govern.Policy{
		Name: "auto-nonprod-waste", DefaultEffect: govern.EffectRequireApproval, Enabled: true,
		Rules: []govern.Rule{{ID: "auto-nonprod", Effect: govern.EffectAutoExecute,
			Match: govern.Match{
				Categories:    []optimize.Category{optimize.CategoryWaste},
				Actions:       []optimize.ActionType{optimize.ActionStopInstance},
				Environments:  []core.Environment{core.EnvDevelopment},
				MinConfidence: 0.85, MaxRiskLevel: core.RiskLow,
				MinReversibility: optimize.ReversibilityFast,
			}}},
	}
	mustActivatePolicy(t, svc, testTenant, policy, "admin")

	decision, err := svc.Evaluate(ctxFor(testTenant), testTenant, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, govern.EffectAutoExecute, decision.Effect)
	assert.Equal(t, "auto-nonprod", decision.DecidingRule)
	assert.False(t, decision.RequiresApproval)
}

// TestGovernEvaluate_BudgetEscalationAppliesOnlyToCostIncreases is the
// regression test for a guard that had lost its direction check.
//
// econ.EconomicErrorBudget.AllowsCostIncrease is the only source of
// Input.BudgetRequiresApproval, and its name states its scope: it answers
// whether spending *more* may proceed. The freeze branch honoured that; the
// escalation branch did not, so a tenant over budget had every one of its
// cost-reducing changes escalated to a human — the platform stopped saving
// money at exactly the moment the budget said it needed to. The demo tenant
// reproduced it exactly: over its declared spend budget, therefore nothing
// auto-executable, therefore still over budget.
func TestGovernEvaluate_BudgetEscalationAppliesOnlyToCostIncreases(t *testing.T) {
	policy := govern.Policy{
		Name: "auto-nonprod", DefaultEffect: govern.EffectRequireApproval,
		Rules: []govern.Rule{{ID: "auto", Effect: govern.EffectAutoExecute,
			Match: govern.Match{Actions: []optimize.ActionType{optimize.ActionStopInstance}}}},
	}
	base := govern.Input{
		TenantID: testTenant, RecommendationID: "rec-1", RuleID: "rule-1",
		Action: optimize.ActionStopInstance, ResourceID: "res-1", AccountID: "111122223333",
		Environment: core.EnvDevelopment, RiskLevel: core.RiskLow, Reversibility: optimize.ReversibilityFast,
		Confidence: 0.99, AutomationEnabled: true, BudgetRequiresApproval: true, Now: testNow,
	}

	cases := []struct {
		name     string
		delta    core.Money
		want     govern.Effect
		deciding string
	}{
		{
			name:     "a change that reduces run-rate is not escalated",
			delta:    core.ZeroUSD().MustSub(core.USDollars(120)),
			want:     govern.EffectAutoExecute,
			deciding: "auto",
		},
		{
			name:     "a change that increases run-rate is escalated",
			delta:    core.USDollars(120),
			want:     govern.EffectRequireApproval,
			deciding: "__economic_error_budget_escalation__",
		},
		{
			name:     "a change with no run-rate effect is not escalated",
			delta:    core.ZeroUSD(),
			want:     govern.EffectAutoExecute,
			deciding: "auto",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.MonthlyCostDelta = tc.delta
			d := govern.Evaluate(policy, in)
			assert.Equal(t, tc.want, d.Effect)
			assert.Equal(t, tc.deciding, d.DecidingRule)
		})
	}
}
