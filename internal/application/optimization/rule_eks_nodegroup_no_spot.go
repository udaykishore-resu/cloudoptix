package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEKSNodeGroupNoSpot mirrors ec2-spot-candidacy for a node group:
// an interruption-tolerant, on-demand node group whose tenant has opted
// into Spot is leaving the Spot discount on the table. Kubernetes's own
// pod rescheduling is what makes node-level interruption tolerable at all,
// so this still requires the declared workload behind the group to be
// interruption-tolerant and never tier-0 — the scheduler retrying a pod
// elsewhere doesn't help a workload that cannot survive losing the pod.
//
// Traceability: REQ-OPT-009.
const RuleIDEKSNodeGroupNoSpot optimize.RuleID = "eks-nodegroup-no-spot"

type ruleEKSNodeGroupNoSpot struct{}

func NewEKSNodeGroupNoSpotRule() FullRule { return ruleEKSNodeGroupNoSpot{} }

func (ruleEKSNodeGroupNoSpot) ID() optimize.RuleID { return RuleIDEKSNodeGroupNoSpot }

func (ruleEKSNodeGroupNoSpot) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEKSNodeGroupNoSpot, Name: "Node group with no Spot capacity", Category: optimize.CategoryCommitment,
		Action:      optimize.ActionEnableSpot,
		Description: "An interruption-tolerant node group running entirely on-demand is leaving the Spot discount on the table.",
		Kinds:       []cloud.Kind{cloud.KindEKSNodeGroup}, Enabled: true,
	}
}

func (ruleEKSNodeGroupNoSpot) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEKSNodeGroup && r.State.Active() && r.Purchase == cloud.PurchaseOnDemand &&
		r.Criticality != core.CriticalityTier0 && r.InstanceType != ""
}

func decideEKSNodeGroupNoSpot(ctx EvalContext, r cloud.Resource) (onDemand, spot, saving core.Money, ok bool) {
	if !SpotAllowed(ctx.Spec) || r.Criticality == core.CriticalityTier0 {
		return
	}
	w, found := matchedWorkload(ctx.Spec, r)
	if !found || !InterruptionTolerant(cloud.WorkloadType(w.Type)) || w.Criticality == string(core.CriticalityTier0) {
		return
	}
	onDemand, ok1 := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	spot, ok2 := ctx.Pricing.SpotPrice(r.Region, r.InstanceType)
	if !ok1 || !ok2 || !spot.LessThan(onDemand) {
		return core.Money{}, core.Money{}, core.Money{}, false
	}
	nodeCount := maxFloat(float64(r.Capacity.InstanceCount), 1)
	saving = onDemand.MustSub(spot).Scale(core.HoursPerMonth * nodeCount)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEKSNodeGroupNoSpot, "min_monthly_saving", 15)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionEnableSpot) {
		return core.Money{}, core.Money{}, core.Money{}, false
	}
	return onDemand, spot, saving, true
}

func (ruleEKSNodeGroupNoSpot) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	onDemand, spot, saving, ok := decideEKSNodeGroupNoSpot(ctx, r)
	if !ok {
		return nil, nil
	}
	w, _ := matchedWorkload(ctx.Spec, r)
	evidence := []optimize.Evidence{
		ConfigEvidence("declared workload type", fmt.Sprintf("%s (interruption-tolerant)", w.Type)),
		CostEvidence("on-demand vs recent average spot", fmt.Sprintf("%s/hr vs %s/hr", onDemand.Format(), spot.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s (%s workload) can run on Spot", r.DisplayName(), w.Type)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEKSNodeGroupNoSpot{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Kubernetes reschedules interrupted pods elsewhere in the cluster, making node-level interruption tolerable.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEKSNodeGroupNoSpot) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, spot, _, ok := decideEKSNodeGroupNoSpot(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	nodeCount := maxFloat(float64(r.Capacity.InstanceCount), 1)
	return RuleAction{
		Type:          optimize.ActionEnableSpot,
		Parameters:    map[string]any{"node_group_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: spot.Scale(core.HoursPerMonth * nodeCount), Attributes: map[string]string{"purchase_model": "spot"}},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Enable Spot capacity on %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
