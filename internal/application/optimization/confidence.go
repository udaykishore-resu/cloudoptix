package optimization

import (
	"fmt"
	"math"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// This file computes core.Confidence for a finding.
//
// It is deliberately NOT an LLM self-report, and this is not a stylistic
// preference: an LLM asked "how confident are you" answers from its own
// training distribution of confident-sounding text, which is uncorrelated
// with whether the underlying telemetry actually supports the claim — a
// model will happily report 90% confidence on a resource with three data
// points and 95% confidence on a resource with three thousand, because
// nothing in its prompt encodes what "enough evidence" means for this
// domain. Confidence here is instead a weighted function of structured facts
// a reviewer could recompute by hand from the same finding: how stable the
// metric was, how long and how completely it was observed, how much of the
// dependency graph is visible, how much is at stake, how old the resource is,
// and — multiplicatively, last — how often this exact rule has been right
// before. Every one of those is auditable; "the model felt confident" is not.
//
// Traceability: REQ-OPT-006, SPEC-OPT-004.

// confidenceWeights sum to 1 across the six additive factors; the calibration
// factor is multiplicative and applied after, so it is not part of this sum.
const (
	weightStability     = 0.20
	weightCoverage      = 0.18
	weightWindow        = 0.16
	weightDependency    = 0.16
	weightCriticality   = 0.15
	weightAge           = 0.15
	confidentWindowDays = 14.0 // a two-week window is treated as "fully observed"
	confidentAgeDays    = 30.0 // a month old is treated as "established"
)

// ComputeConfidence derives a finding's confidence and the itemised inputs
// that explain it.
//
// primary is the percentile summary the rule's own logic leaned on most (CPU
// for a rightsizing finding, connections for an idle-database finding, and
// so on); passing nil is valid for config-only findings (an unattached
// volume, a missing lifecycle policy) and scores that factor neutrally rather
// than penalising a rule that was never metric-based to begin with.
func ComputeConfidence(ctx EvalContext, r cloud.Resource, ruleID optimize.RuleID, m ports.ResourceMetrics, primary *core.Percentiles, blast optimize.BlastRadius) (core.Confidence, []optimize.ConfidenceInput) {
	inputs := make([]optimize.ConfidenceInput, 0, 7)

	stability := 0.65 // neutral: this finding did not depend on a time series
	stabilityNote := "finding is not metric-derived; stability held neutral"
	if primary != nil {
		stability = clamp01(primary.Stability)
		stabilityNote = fmt.Sprintf("coefficient-of-variation-derived stability of the %s distribution", primary.Label())
	}
	inputs = append(inputs, optimize.ConfidenceInput{
		Name: "metric_stability", Value: stability, Weight: weightStability, Explanation: stabilityNote,
	})

	coverage := m.Coverage
	covNote := fmt.Sprintf("%.0f%% of the observation window has telemetry", coverage*100)
	if m.ResourceID == "" {
		coverage = 0.3
		covNote = "no telemetry summary exists for this resource; coverage held low"
	}
	inputs = append(inputs, optimize.ConfidenceInput{
		Name: "metric_coverage", Value: clamp01(coverage), Weight: weightCoverage, Explanation: covNote,
	})

	windowDays := m.Window.Days()
	windowScore := clamp01(windowDays / confidentWindowDays)
	inputs = append(inputs, optimize.ConfidenceInput{
		Name: "observation_window", Value: windowScore, Weight: weightWindow,
		Explanation: fmt.Sprintf("%.1f day observation window against a %.0f day reference", windowDays, confidentWindowDays),
	})

	inputs = append(inputs, optimize.ConfidenceInput{
		Name: "dependency_completeness", Value: clamp01(blast.Completeness), Weight: weightDependency,
		Explanation: "share of the resource's dependency graph the twin could resolve",
	})

	// A higher-stakes resource gets a more conservative (lower) confidence
	// contribution at equal evidence quality: the cost of a wrong prediction
	// scales with criticality, so the bar for calling it "confident" should
	// too. This is a deliberate asymmetry, not a measurement of the evidence
	// itself — it is why the factor lives here rather than folded into risk,
	// which already penalises criticality on the consequence side; this is
	// the belief side.
	critFactor := clamp01(1 - 0.3*r.Criticality.Weight())
	inputs = append(inputs, optimize.ConfidenceInput{
		Name: "business_criticality", Value: critFactor, Weight: weightCriticality,
		Explanation: fmt.Sprintf("criticality %s discounts confidence for higher-stakes resources", nonEmpty(string(r.Criticality), "UNSET")),
	})

	ageDays := r.Age(ctx.Now()).Hours() / 24
	ageScore := clamp01(ageDays / confidentAgeDays)
	ageNote := fmt.Sprintf("resource is %.0f days old against a %.0f day reference for an established pattern", ageDays, confidentAgeDays)
	if r.CreatedAt.IsZero() {
		ageScore = 0.5
		ageNote = "creation time unknown; age held neutral"
	}
	inputs = append(inputs, optimize.ConfidenceInput{
		Name: "resource_age", Value: ageScore, Weight: weightAge, Explanation: ageNote,
	})

	var weighted, totalWeight float64
	for _, in := range inputs {
		weighted += in.Value * in.Weight
		totalWeight += in.Weight
	}
	base := 0.5
	if totalWeight > 0 {
		base = weighted / totalWeight
	}

	// The calibration multiplier is applied last and multiplicatively, exactly
	// as execute.RuleCalibration's own doc comment describes it: it can
	// substantially distrust a rule with a bad track record but only
	// marginally boost one with a good one (the multiplier is bounded to
	// [0.5, 1.1] at calibration time), because over-confidence is the failure
	// mode with real consequences.
	calMultiplier := 1.0
	calNote := "no calibration history yet for this rule; multiplier held at 1.0"
	if c, ok := ctx.Calibrations[ruleID]; ok && c.Samples > 0 {
		calMultiplier = c.ConfidenceMultiplier
		calNote = fmt.Sprintf("%d historical outcomes, %.0f%% success rate, %.0f%% rollback rate",
			c.Samples, c.SuccessRate*100, c.RollbackRate*100)
	}
	inputs = append(inputs, optimize.ConfidenceInput{
		Name: "rule_calibration", Value: calMultiplier, Weight: 1.0,
		Explanation: "multiplicative adjustment from this rule's historical accuracy: " + calNote,
	})

	final := core.Confidence(clamp01(base * calMultiplier))
	return final, inputs
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
