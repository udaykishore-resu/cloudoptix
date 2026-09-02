package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEC2SpotCandidacy flags an on-demand instance whose declared workload
// type tolerates interruption for a move to Spot. It is deliberately narrow:
// only workload types that can be interrupted and restarted without a
// correctness problem are ever considered (see InterruptionTolerant), a
// tier-0 workload is excluded regardless of its type, and the tenant's own
// spotAllowed flag and risk tolerance both have to permit it.
//
// Traceability: REQ-OPT-003, SPEC-OPT-002 (Spot gating).
const RuleIDEC2SpotCandidacy optimize.RuleID = "ec2-spot-candidacy"

type ruleEC2SpotCandidacy struct{}

func NewEC2SpotCandidacyRule() FullRule { return ruleEC2SpotCandidacy{} }

func (ruleEC2SpotCandidacy) ID() optimize.RuleID { return RuleIDEC2SpotCandidacy }

func (ruleEC2SpotCandidacy) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEC2SpotCandidacy, Name: "EC2 Spot candidacy",
		Category: optimize.CategoryCommitment, Action: optimize.ActionEnableSpot,
		Description: "An on-demand instance whose declared workload type tolerates interruption, " +
			"is never tier-0, and whose tenant has opted into Spot, is a Spot candidate.",
		Kinds: []cloud.Kind{cloud.KindEC2Instance}, Enabled: true,
	}
}

func (ruleEC2SpotCandidacy) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEC2Instance && r.State.Active() && r.Purchase == cloud.PurchaseOnDemand &&
		r.Criticality != core.CriticalityTier0
}

func decideEC2SpotCandidacy(ctx EvalContext, r cloud.Resource) (onDemand, spot, saving core.Money, ok bool) {
	if !SpotAllowed(ctx.Spec) || r.Criticality == core.CriticalityTier0 {
		return
	}
	w, found := matchedWorkload(ctx.Spec, r)
	if !found || !InterruptionTolerant(cloud.WorkloadType(w.Type)) {
		return
	}
	if w.Criticality == string(core.CriticalityTier0) {
		return
	}
	onDemand, ok1 := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	spot, ok2 := ctx.Pricing.SpotPrice(r.Region, r.InstanceType)
	if !ok1 || !ok2 || !spot.LessThan(onDemand) {
		return core.Money{}, core.Money{}, core.Money{}, false
	}
	saving = onDemand.MustSub(spot).Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2SpotCandidacy, "min_monthly_saving", 10)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionEnableSpot) {
		return core.Money{}, core.Money{}, core.Money{}, false
	}
	return onDemand, spot, saving, true
}

func (ruleEC2SpotCandidacy) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	onDemand, spot, saving, ok := decideEC2SpotCandidacy(ctx, r)
	if !ok {
		return nil, nil
	}
	w, _ := matchedWorkload(ctx.Spec, r)
	evidence := []optimize.Evidence{
		ConfigEvidence("declared workload type", fmt.Sprintf("%s (interruption-tolerant, criticality %s)", w.Type, nonEmpty(w.Criticality, "unset"))),
		CostEvidence("on-demand vs recent average spot", fmt.Sprintf("%s/hr vs %s/hr", onDemand.Format(), spot.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s (%s workload) is a Spot candidate", r.DisplayName(), w.Type)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2SpotCandidacy{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Declared workload type tolerates interruption; tenant has opted into Spot at the current risk tolerance.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEC2SpotCandidacy) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, spot, _, ok := decideEC2SpotCandidacy(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionEnableSpot,
		Parameters:    map[string]any{"instance_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: spot.Scale(core.HoursPerMonth), Attributes: map[string]string{"purchase_model": "spot"}},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Move %s to Spot", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
