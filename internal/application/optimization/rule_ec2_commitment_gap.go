package optimization

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEC2CommitmentGap flags an on-demand instance that has run
// continuously — the steady-state baseline signal, not a usage peak — for
// long enough that a 1-year no-upfront Savings Plan would cost less than
// staying on-demand for the rest of its life. Steady-state is read from the
// resource's own uninterrupted age plus (when telemetry exists) its
// near-total window coverage, deliberately not from a single day's or a
// single peak's usage: a coverage-gap analysis run against a burst of
// activity would recommend committing to capacity the workload does not
// actually sustain.
//
// Traceability: REQ-OPT-003, SPEC-OPT-002 (commitment coverage).
const RuleIDEC2CommitmentGap optimize.RuleID = "ec2-commitment-coverage-gap"

type ruleEC2CommitmentGap struct{}

func NewEC2CommitmentGapRule() FullRule { return ruleEC2CommitmentGap{} }

func (ruleEC2CommitmentGap) ID() optimize.RuleID { return RuleIDEC2CommitmentGap }

func (ruleEC2CommitmentGap) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEC2CommitmentGap, Name: "Savings Plan / Reserved Instance coverage gap",
		Category: optimize.CategoryCommitment, Action: optimize.ActionPurchaseCommitment,
		Description: "An on-demand instance running continuously for the steady-state lookback " +
			"window with no commitment coverage is priced against a 1-year no-upfront Savings Plan.",
		Kinds: []cloud.Kind{cloud.KindEC2Instance}, Enabled: true,
	}
}

func (ruleEC2CommitmentGap) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEC2Instance && r.State == cloud.StateRunning && r.Purchase == cloud.PurchaseOnDemand
}

func decideEC2CommitmentGap(ctx EvalContext, r cloud.Resource) (onDemand, spRate, saving core.Money, ok bool) {
	minDays := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2CommitmentGap, "min_steady_state_days", 21)
	if r.Age(ctx.Now()) < time.Duration(minDays*24)*time.Hour {
		return
	}
	if m, found := MetricsFor(ctx, r.ID); found && m.Coverage > 0 {
		coverageMax := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2CommitmentGap, "coverage_max", 0.6)
		// Coverage here is telemetry coverage, used as a steadiness proxy: a
		// resource whose window is only thinly observed has not demonstrated
		// the sustained presence a commitment is priced against.
		if m.Coverage < 1-coverageMax {
			return
		}
	}
	onDemand, ok1 := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	spRate, ok2 := ctx.Pricing.CommitmentPrice(r.Region, r.InstanceType, "1yr", "savings_plan_no_upfront")
	if !ok1 || !ok2 || !spRate.LessThan(onDemand) {
		return core.Money{}, core.Money{}, core.Money{}, false
	}
	saving = onDemand.MustSub(spRate).Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2CommitmentGap, "min_monthly_saving", 20)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionPurchaseCommitment) {
		return core.Money{}, core.Money{}, core.Money{}, false
	}
	return onDemand, spRate, saving, true
}

func (ruleEC2CommitmentGap) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	onDemand, spRate, saving, ok := decideEC2CommitmentGap(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("continuous age", fmt.Sprintf("%.0f days on-demand, no commitment coverage", r.Age(ctx.Now()).Hours()/24)),
		CostEvidence("on-demand vs 1yr no-upfront Savings Plan", fmt.Sprintf("%s/hr vs %s/hr", onDemand.Format(), spRate.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s has run on-demand for %.0f days with no commitment coverage", r.DisplayName(), r.Age(ctx.Now()).Hours()/24)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2CommitmentGap{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Steady-state baseline usage (sustained presence, not a peak) supports committing this capacity.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEC2CommitmentGap) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, spRate, _, ok := decideEC2CommitmentGap(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type: optimize.ActionPurchaseCommitment,
		Parameters: map[string]any{
			"instance_type": r.InstanceType, "term": "1yr", "payment": "savings_plan_no_upfront",
		},
		ProposedState: optimize.StateSnapshot{MonthlyCost: spRate.Scale(core.HoursPerMonth)},
		Reversibility: optimize.ReversibilityNone, // a purchased commitment cannot be undone
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Cover %s with a 1-year Savings Plan", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
