package optimization

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestComputeConfidence_Weighting checks that each structured input actually
// moves the score in the direction its doc comment claims: better evidence
// quality raises confidence, and a higher-stakes resource is discounted at
// equal evidence quality.
func TestComputeConfidence_Weighting(t *testing.T) {
	r := mkResource(cloud.KindEC2Instance, "m5.large")
	r.CreatedAt = testNow.Add(-60 * 24 * time.Hour) // well past the 30-day "established" reference
	inv := cloud.NewInventory([]cloud.Resource{r})
	blast := optimize.BlastRadius{Completeness: 0.9}

	strong := ports.ResourceMetrics{
		ResourceID: r.ID, Coverage: 0.98,
		Window: core.Period{Start: testNow.Add(-21 * 24 * time.Hour), End: testNow},
	}
	strongPrimary := &core.Percentiles{Stability: 0.95}

	weak := ports.ResourceMetrics{
		ResourceID: r.ID, Coverage: 0.4,
		Window: core.Period{Start: testNow.Add(-2 * 24 * time.Hour), End: testNow},
	}
	weakPrimary := &core.Percentiles{Stability: 0.3}

	ctx := testEvalContext(inv, nil, nil, testSpec())

	strongConf, _ := ComputeConfidence(ctx, r, "test-rule", strong, strongPrimary, blast)
	weakConf, _ := ComputeConfidence(ctx, r, "test-rule", weak, weakPrimary, blast)
	assert.Greater(t, float64(strongConf), float64(weakConf),
		"a stable metric, high coverage and a full observation window must score higher than the opposite")

	t.Run("higher criticality discounts confidence at equal evidence", func(t *testing.T) {
		tier0 := r
		tier0.Criticality = core.CriticalityTier0
		invT0 := cloud.NewInventory([]cloud.Resource{tier0})
		ctxT0 := testEvalContext(invT0, nil, nil, testSpec())

		unset := r
		unset.Criticality = core.CriticalityUnset
		invUnset := cloud.NewInventory([]cloud.Resource{unset})
		ctxUnset := testEvalContext(invUnset, nil, nil, testSpec())

		tier0Conf, _ := ComputeConfidence(ctxT0, tier0, "test-rule", strong, strongPrimary, blast)
		unsetConf, _ := ComputeConfidence(ctxUnset, unset, "test-rule", strong, strongPrimary, blast)
		assert.Greater(t, float64(unsetConf), float64(tier0Conf),
			"a Tier0 resource must never score AS confident as an equally-well-observed unset-criticality one")
	})
}

// TestComputeConfidence_CalibrationIsMultiplicativeAndLast checks that a
// rule's historical accuracy record scales the evidence-based score rather
// than replacing it or being folded additively into the other six factors.
func TestComputeConfidence_CalibrationIsMultiplicativeAndLast(t *testing.T) {
	r := mkResource(cloud.KindEC2Instance, "m5.large")
	r.CreatedAt = testNow.Add(-60 * 24 * time.Hour)
	inv := cloud.NewInventory([]cloud.Resource{r})
	blast := optimize.BlastRadius{Completeness: 0.9}
	m := ports.ResourceMetrics{
		ResourceID: r.ID, Coverage: 0.9,
		Window: core.Period{Start: testNow.Add(-21 * 24 * time.Hour), End: testNow},
	}
	primary := &core.Percentiles{Stability: 0.9}

	baseCtx := testEvalContext(inv, nil, nil, testSpec())
	uncalibrated, inputs := ComputeConfidence(baseCtx, r, "flaky-rule", m, primary, blast)
	require.NotEmpty(t, inputs)
	var calInput *optimize.ConfidenceInput
	for i := range inputs {
		if inputs[i].Name == "rule_calibration" {
			calInput = &inputs[i]
		}
	}
	require.NotNil(t, calInput)
	assert.Equal(t, 1.0, calInput.Value, "an uncalibrated rule must hold the multiplier at 1.0, not distrust by default")

	distrustedCtx := baseCtx
	distrustedCtx.Calibrations = map[optimize.RuleID]execute.RuleCalibration{
		"flaky-rule": {Samples: 50, SuccessRate: 0.4, RollbackRate: 0.3, ConfidenceMultiplier: 0.5},
	}
	distrusted, _ := ComputeConfidence(distrustedCtx, r, "flaky-rule", m, primary, blast)

	assert.Less(t, float64(distrusted), float64(uncalibrated),
		"a poor track record must pull confidence down")
	// The multiplier is applied last and multiplicatively: distrusted should
	// land at (evidence-based base) * 0.5, not base-0.5 or some additive mix.
	assert.InDelta(t, float64(uncalibrated)*0.5, float64(distrusted), 0.02)
}
