package execute

import (
	"fmt"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// SavingsStage is a rung on the savings ladder.
//
// Nearly every cloud cost tool reports the top rung — "potential savings" —
// and stops. That number is a marketing figure: it assumes every
// recommendation is approved, executed perfectly, and holds. CloudOptix tracks
// all six rungs so a FinOps lead can see exactly where value leaks out of the
// funnel: recommendations nobody approved, approvals nobody executed,
// executions that were rolled back, and executed changes whose predicted
// saving never showed up on the invoice.
//
// Traceability: REQ-SAV-001..007, SPEC-AUTO-005.
type SavingsStage string

const (
	// StagePotential: a recommendation exists.
	StagePotential SavingsStage = "potential"
	// StageApproved: a human or policy authorised it.
	StageApproved SavingsStage = "approved"
	// StagePlanned: an execution plan with a rollback exists and is scheduled.
	StagePlanned SavingsStage = "planned"
	// StageExecuted: the AWS mutation succeeded.
	StageExecuted SavingsStage = "executed"
	// StageValidated: the observation window closed without a critical
	// regression.
	StageValidated SavingsStage = "validated"
	// StageRealized: billing data after the change confirms the reduction.
	// This is the only figure CloudOptix will put in front of a CFO.
	StageRealized SavingsStage = "realized"
)

// Order returns the ladder position.
func (s SavingsStage) Order() int {
	switch s {
	case StagePotential:
		return 0
	case StageApproved:
		return 1
	case StagePlanned:
		return 2
	case StageExecuted:
		return 3
	case StageValidated:
		return 4
	case StageRealized:
		return 5
	}
	return 0
}

// SavingsRecord tracks one recommendation's journey down the ladder.
type SavingsRecord struct {
	ID               core.ID             `json:"id"`
	TenantID         core.TenantID       `json:"tenant_id"`
	RecommendationID core.ID             `json:"recommendation_id"`
	PlanID           core.ID             `json:"plan_id,omitempty"`
	RuleID           optimize.RuleID     `json:"rule_id"`
	Action           optimize.ActionType `json:"action"`
	ResourceID       core.ID             `json:"resource_id"`
	ApplicationID    core.ID             `json:"application_id,omitempty"`
	Environment      core.Environment    `json:"environment"`

	Stage SavingsStage `json:"stage"`

	PotentialMonthly core.Money `json:"potential_monthly"`
	ApprovedMonthly  core.Money `json:"approved_monthly,omitempty"`
	ExecutedMonthly  core.Money `json:"executed_monthly,omitempty"`
	ValidatedMonthly core.Money `json:"validated_monthly,omitempty"`
	RealizedMonthly  core.Money `json:"realized_monthly,omitempty"`

	// BaselineCost and PostChangeCost are the measured figures the realized
	// saving is computed from, retained so the claim can be audited.
	BaselineCost   core.Money  `json:"baseline_cost,omitempty"`
	PostChangeCost core.Money  `json:"post_change_cost,omitempty"`
	MeasuredWindow core.Period `json:"measured_window,omitempty"`

	StageHistory []StageTransition `json:"stage_history"`
	Lost         bool              `json:"lost"`
	LostReason   string            `json:"lost_reason,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// StageTransition records a movement on the ladder.
type StageTransition struct {
	From   SavingsStage `json:"from"`
	To     SavingsStage `json:"to"`
	Amount core.Money   `json:"amount"`
	Actor  string       `json:"actor"`
	Reason string       `json:"reason,omitempty"`
	At     time.Time    `json:"at"`
}

// Advance moves the record to a later stage.
func (r *SavingsRecord) Advance(to SavingsStage, amount core.Money, actor, reason string, at time.Time) {
	if to.Order() <= r.Stage.Order() {
		return
	}
	r.StageHistory = append(r.StageHistory, StageTransition{
		From: r.Stage, To: to, Amount: amount, Actor: actor, Reason: reason, At: at,
	})
	r.Stage = to
	switch to {
	case StageApproved:
		r.ApprovedMonthly = amount
	case StageExecuted:
		r.ExecutedMonthly = amount
	case StageValidated:
		r.ValidatedMonthly = amount
	case StageRealized:
		r.RealizedMonthly = amount
	}
	r.UpdatedAt = at
}

// MarkLost records that the saving will not be realized, with the reason. A
// lost saving stays in the funnel report; hiding it would make the conversion
// rate look better than it is.
func (r *SavingsRecord) MarkLost(reason string, at time.Time) {
	r.Lost = true
	r.LostReason = reason
	r.UpdatedAt = at
}

// Funnel is the aggregated savings lifecycle for a tenant or scope.
type Funnel struct {
	TenantID core.TenantID `json:"tenant_id"`
	Period   core.Period   `json:"period"`

	Potential core.Money `json:"potential_monthly"`
	Approved  core.Money `json:"approved_monthly"`
	Planned   core.Money `json:"planned_monthly"`
	Executed  core.Money `json:"executed_monthly"`
	Validated core.Money `json:"validated_monthly"`
	Realized  core.Money `json:"realized_monthly"`

	RealizedAnnual core.Money `json:"realized_annual"`

	Counts map[SavingsStage]int `json:"counts"`
	// Leakage explains where value is lost between rungs, which is the
	// actionable part of the funnel.
	Leakage []LeakagePoint `json:"leakage"`
	// PredictionAccuracy is realized/executed: how good CloudOptix's
	// estimates actually were. It is published rather than hidden because a
	// platform that predicts badly should be visibly bad at it.
	PredictionAccuracy float64 `json:"prediction_accuracy"`
	// OverAttributed is money a later stage claimed beyond what the stage
	// above it carried, capped away to hold the funnel invariant. A non-zero
	// value is a data-quality finding about CloudOptix's own measurement, not
	// a saving, and is surfaced rather than absorbed.
	OverAttributed       core.Money `json:"over_attributed"`
	OverAttributionNotes []string   `json:"over_attribution_notes,omitempty"`
	ComputedAt           time.Time  `json:"computed_at"`
}

// LeakagePoint is value lost between two rungs.
type LeakagePoint struct {
	From       SavingsStage `json:"from"`
	To         SavingsStage `json:"to"`
	Amount     core.Money   `json:"amount"`
	Count      int          `json:"count"`
	Rate       float64      `json:"conversion_rate"`
	TopReasons []string     `json:"top_reasons,omitempty"`
}

// BuildFunnel aggregates savings records into the funnel view.
func BuildFunnel(tenant core.TenantID, period core.Period, records []SavingsRecord) Funnel {
	f := Funnel{
		TenantID: tenant, Period: period,
		Potential: core.ZeroUSD(), Approved: core.ZeroUSD(), Planned: core.ZeroUSD(),
		Executed: core.ZeroUSD(), Validated: core.ZeroUSD(), Realized: core.ZeroUSD(),
		OverAttributed: core.ZeroUSD(),
		Counts:         map[SavingsStage]int{}, ComputedAt: time.Now().UTC(),
	}
	reasons := map[SavingsStage][]string{}
	for _, r := range records {
		f.Potential = f.Potential.MustAdd(r.PotentialMonthly)
		f.Counts[StagePotential]++
		if r.Stage.Order() >= StageApproved.Order() {
			f.Approved = f.Approved.MustAdd(nonZero(r.ApprovedMonthly, r.PotentialMonthly))
			f.Counts[StageApproved]++
		}
		if r.Stage.Order() >= StagePlanned.Order() {
			f.Planned = f.Planned.MustAdd(nonZero(r.ApprovedMonthly, r.PotentialMonthly))
			f.Counts[StagePlanned]++
		}
		if r.Stage.Order() >= StageExecuted.Order() {
			f.Executed = f.Executed.MustAdd(nonZero(r.ExecutedMonthly, r.PotentialMonthly))
			f.Counts[StageExecuted]++
		}
		if r.Stage.Order() >= StageValidated.Order() {
			f.Validated = f.Validated.MustAdd(nonZero(r.ValidatedMonthly, r.ExecutedMonthly))
			f.Counts[StageValidated]++
		}
		if r.Stage.Order() >= StageRealized.Order() {
			f.Realized = f.Realized.MustAdd(r.RealizedMonthly)
			f.Counts[StageRealized]++
		}
		if r.Lost && r.LostReason != "" {
			reasons[r.Stage] = append(reasons[r.Stage], r.LostReason)
		}
	}
	// The funnel is monotonically non-increasing by construction: a stage
	// describes a subset of the money that reached the stage above it, so no
	// rung can exceed its predecessor. Enforcing that here rather than trusting
	// every writer is the difference between a report and a guarantee — a
	// single mis-measured validation that credits a change with more than it
	// executed would otherwise produce a funnel that grows downward, which is
	// both obviously wrong and, worse, wrong in the flattering direction.
	//
	// Excess is not silently truncated: it is recorded on OverAttributed so the
	// disagreement is visible and can be investigated, because a stage
	// exceeding its predecessor always means a measurement attributed money to
	// a change that did not produce it.
	stages := []*core.Money{&f.Potential, &f.Approved, &f.Planned, &f.Executed, &f.Validated, &f.Realized}
	names := []SavingsStage{StagePotential, StageApproved, StagePlanned, StageExecuted, StageValidated, StageRealized}
	for i := 1; i < len(stages); i++ {
		if stages[i].GreaterThan(*stages[i-1]) {
			excess := stages[i].MustSub(*stages[i-1])
			f.OverAttributed = f.OverAttributed.MustAdd(excess)
			f.OverAttributionNotes = append(f.OverAttributionNotes, fmt.Sprintf(
				"%s (%s) exceeded %s (%s) by %s; capped to preserve the funnel invariant",
				names[i], stages[i].Format(), names[i-1], stages[i-1].Format(), excess.Format()))
			*stages[i] = *stages[i-1]
		}
	}

	f.RealizedAnnual = f.Realized.Annualized()
	if !f.Executed.IsZero() {
		f.PredictionAccuracy = f.Realized.Ratio(f.Executed)
	}
	rungs := []struct {
		from, to SavingsStage
		a, b     core.Money
	}{
		{StagePotential, StageApproved, f.Potential, f.Approved},
		{StageApproved, StagePlanned, f.Approved, f.Planned},
		{StagePlanned, StageExecuted, f.Planned, f.Executed},
		{StageExecuted, StageValidated, f.Executed, f.Validated},
		{StageValidated, StageRealized, f.Validated, f.Realized},
	}
	for _, rung := range rungs {
		lost := rung.a.MustSub(rung.b)
		rate := 1.0
		if !rung.a.IsZero() {
			rate = rung.b.Ratio(rung.a)
		}
		lp := LeakagePoint{
			From: rung.from, To: rung.to, Amount: lost,
			Count: f.Counts[rung.from] - f.Counts[rung.to], Rate: rate,
		}
		lp.TopReasons = topReasons(reasons[rung.from], 3)
		f.Leakage = append(f.Leakage, lp)
	}
	return f
}

func nonZero(primary, fallback core.Money) core.Money {
	if primary.IsZero() {
		return fallback
	}
	return primary
}

func topReasons(all []string, n int) []string {
	if len(all) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, r := range all {
		counts[r]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// Outcome is the historical record the learning loop consumes.
//
// The loop is deliberately narrow: it adjusts the confidence and savings
// estimates that future recommendations from the same rule receive. It does
// not modify rules, generate rules, or alter policy. A system that could
// rewrite its own safety rules based on outcomes is not one you would grant an
// execute role.
//
// Traceability: REQ-LRN-001..006, SPEC-OPT-008.
type Outcome struct {
	ID           core.ID             `json:"id"`
	TenantID     core.TenantID       `json:"tenant_id"`
	RuleID       optimize.RuleID     `json:"rule_id"`
	Action       optimize.ActionType `json:"action"`
	ResourceKind string              `json:"resource_kind"`
	Environment  core.Environment    `json:"environment"`

	PredictedMonthlySaving core.Money      `json:"predicted_monthly_saving"`
	ActualMonthlySaving    core.Money      `json:"actual_monthly_saving"`
	PredictedConfidence    core.Confidence `json:"predicted_confidence"`
	PredictedRisk          core.RiskLevel  `json:"predicted_risk"`

	Verdict            Verdict `json:"verdict"`
	RolledBack         bool    `json:"rolled_back"`
	PerformanceImpact  float64 `json:"performance_impact_pct"`
	AvailabilityImpact float64 `json:"availability_impact_pct"`

	// SavingRatio is actual/predicted. Values below 1 mean the rule
	// over-promises; the calibrator shrinks its future estimates.
	SavingRatio float64   `json:"saving_ratio"`
	ObservedAt  time.Time `json:"observed_at"`
}

// RuleCalibration is the aggregated track record of a rule, applied as a
// multiplier to future estimates and confidences from that rule.
type RuleCalibration struct {
	RuleID   optimize.RuleID `json:"rule_id"`
	TenantID core.TenantID   `json:"tenant_id"`

	Samples           int     `json:"samples"`
	SuccessRate       float64 `json:"success_rate"`
	RollbackRate      float64 `json:"rollback_rate"`
	MeanSavingRatio   float64 `json:"mean_saving_ratio"`
	MedianSavingRatio float64 `json:"median_saving_ratio"`
	// ConfidenceMultiplier is applied to raw rule confidence. It is bounded
	// to [0.5, 1.1]: the loop can substantially distrust a rule but can only
	// marginally increase trust, because over-confidence is the failure mode
	// with real consequences.
	ConfidenceMultiplier float64   `json:"confidence_multiplier"`
	SavingMultiplier     float64   `json:"saving_multiplier"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Calibrate recomputes a rule's calibration from its outcome history.
//
// It requires a minimum sample count before applying any adjustment, because
// shrinking a rule's estimates on the strength of two observations is noise
// amplification rather than learning.
func Calibrate(ruleID optimize.RuleID, tenant core.TenantID, outcomes []Outcome, minSamples int) RuleCalibration {
	c := RuleCalibration{
		RuleID: ruleID, TenantID: tenant, Samples: len(outcomes),
		ConfidenceMultiplier: 1, SavingMultiplier: 1, UpdatedAt: time.Now().UTC(),
	}
	if len(outcomes) == 0 {
		return c
	}
	var successes, rollbacks int
	ratios := make([]float64, 0, len(outcomes))
	var ratioSum float64
	for _, o := range outcomes {
		if o.Verdict == VerdictSuccess {
			successes++
		}
		if o.RolledBack {
			rollbacks++
		}
		if o.SavingRatio > 0 {
			ratios = append(ratios, o.SavingRatio)
			ratioSum += o.SavingRatio
		}
	}
	c.SuccessRate = float64(successes) / float64(len(outcomes))
	c.RollbackRate = float64(rollbacks) / float64(len(outcomes))
	if len(ratios) > 0 {
		c.MeanSavingRatio = ratioSum / float64(len(ratios))
		sort.Float64s(ratios)
		c.MedianSavingRatio = ratios[len(ratios)/2]
	}
	if len(outcomes) < minSamples {
		return c // not enough evidence to adjust anything
	}
	// Confidence multiplier tracks success rate, penalising rollbacks
	// disproportionately: a rule that saves money nine times and causes one
	// rollback is not a 90%-confidence rule.
	m := c.SuccessRate - 2*c.RollbackRate
	c.ConfidenceMultiplier = clampFloat(0.5+0.6*m, 0.5, 1.1)
	if c.MedianSavingRatio > 0 {
		c.SavingMultiplier = clampFloat(c.MedianSavingRatio, 0.3, 1.2)
	}
	return c
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
