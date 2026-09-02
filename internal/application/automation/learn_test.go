package automation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func seedOutcome(t *testing.T, h *testHarness, ruleID optimize.RuleID, verdict execute.Verdict, savingRatio float64, rolledBack bool) {
	t.Helper()
	require.NoError(t, h.repos.Savings.SaveOutcome(ctxFor(testTenant), execute.Outcome{
		TenantID: testTenant, RuleID: ruleID, Action: optimize.ActionResizeInstance,
		PredictedMonthlySaving: core.USDollars(100), ActualMonthlySaving: core.USDollars(100 * savingRatio),
		Verdict: verdict, RolledBack: rolledBack, SavingRatio: savingRatio, ObservedAt: testNow,
	}))
}

// TestLearn_MinimumSampleGuardKeepsNeutralMultipliersUntilEnoughEvidence
// proves a rule with only a couple of outcomes — even bad ones — is not
// derated: minCalibrationSamples exists exactly to stop two rollbacks in a
// row from producing a confidence-shrinking verdict on a rule that might
// simply have had unlucky timing.
func TestLearn_MinimumSampleGuardKeepsNeutralMultipliersUntilEnoughEvidence(t *testing.T) {
	h := newHarness(t)
	const rule optimize.RuleID = "rule.ec2.rightsize"

	// Two rollbacks: damning if it were enough evidence, which is exactly
	// why it must not be.
	seedOutcome(t, h, rule, execute.VerdictFailure, 0, true)
	seedOutcome(t, h, rule, execute.VerdictFailure, 0, true)

	result, err := h.svc.Learn(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	require.Contains(t, result.Calibrations, rule)
	calib := result.Calibrations[rule]
	assert.Equal(t, 2, calib.Samples)
	assert.Equal(t, 1.0, calib.ConfidenceMultiplier, "below the sample threshold, the multiplier must stay neutral")
	assert.Equal(t, 1.0, calib.SavingMultiplier)

	// Once enough samples exist, the same bad track record DOES move the
	// multiplier — proving the guard is about sample size, not an
	// unconditional refusal to ever adjust.
	seedOutcome(t, h, rule, execute.VerdictFailure, 0, true)
	seedOutcome(t, h, rule, execute.VerdictFailure, 0, true)
	seedOutcome(t, h, rule, execute.VerdictFailure, 0, true)

	result2, err := h.svc.Learn(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	calib2 := result2.Calibrations[rule]
	assert.Equal(t, 5, calib2.Samples)
	assert.Less(t, calib2.ConfidenceMultiplier, 1.0, "five outcomes, all rolled back, must derate the rule's confidence")

	persisted, err := h.repos.Savings.LoadCalibrations(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, calib2.ConfidenceMultiplier, persisted[rule].ConfidenceMultiplier)
}

// fakeLearner is a minimal Learner double for TestLearn_DelegatesToConfiguredLearner.
type fakeLearner struct {
	fn func() (ports.LearningResult, error)
}

func (f fakeLearner) Recalibrate(context.Context, core.TenantID) (ports.LearningResult, error) {
	return f.fn()
}

// TestLearn_DelegatesToConfiguredLearner proves that when a Learner is
// wired in, Learn defers to it entirely rather than also running its own
// direct calibration in parallel.
func TestLearn_DelegatesToConfiguredLearner(t *testing.T) {
	h := newHarness(t)
	called := false
	d := h.svc.d
	d.Learner = fakeLearner{fn: func() (ports.LearningResult, error) {
		called = true
		return ports.LearningResult{RulesCalibrated: 42}, nil
	}}
	svc, err := NewService(d)
	require.NoError(t, err)

	result, err := svc.Learn(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, 42, result.RulesCalibrated)
}
