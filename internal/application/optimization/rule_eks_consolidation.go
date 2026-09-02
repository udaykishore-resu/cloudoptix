package optimization

import (
	"fmt"
	"math"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEKSConsolidation flags a node group whose packing density and node
// count together indicate several partially-empty nodes a bin-packing
// consolidation pass (Karpenter, Cluster Autoscaler) would merge. It is
// distinct from eks-nodegroup-overprovisioned: that rule targets node
// *size* (same node count, smaller instances); this one targets node
// *count* (same instance size, fewer nodes), and only fires with enough
// nodes in the group for a consolidation pass to be meaningful.
//
// Traceability: REQ-OPT-009.
const RuleIDEKSConsolidation optimize.RuleID = "eks-consolidation-opportunity"

type ruleEKSConsolidation struct{}

func NewEKSConsolidationRule() FullRule { return ruleEKSConsolidation{} }

func (ruleEKSConsolidation) ID() optimize.RuleID { return RuleIDEKSConsolidation }

func (ruleEKSConsolidation) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEKSConsolidation, Name: "Karpenter/Cluster-Autoscaler consolidation opportunity",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionResizeNodeGroup,
		Description: "A node group's packing density and node count together indicate several partially-empty nodes a consolidation pass would merge.",
		Kinds:       []cloud.Kind{cloud.KindEKSNodeGroup}, Enabled: true,
	}
}

func (ruleEKSConsolidation) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEKSNodeGroup && r.State.Active() && r.InstanceType != ""
}

func decideEKSConsolidation(ctx EvalContext, r cloud.Resource) (packed float64, reclaimable int, saving core.Money, ok bool) {
	minNodeCount := ctx.Thresholds.Int(ctx.TenantID, RuleIDEKSConsolidation, "min_node_count", 3)
	if r.Capacity.InstanceCount < minNodeCount {
		return
	}
	packed = parseFloatAttr(r.Attr("packed_fraction", ""), -1)
	if packed < 0 {
		return 0, 0, core.Money{}, false
	}
	maxPacked := ctx.Thresholds.Float(ctx.TenantID, RuleIDEKSConsolidation, "packed_fraction_max", 0.55)
	if packed > maxPacked {
		return packed, 0, core.Money{}, false
	}
	targetPacked := ctx.Thresholds.Float(ctx.TenantID, RuleIDEKSConsolidation, "target_packed_fraction", 0.85)
	neededNodes := int(math.Ceil(float64(r.Capacity.InstanceCount) * packed / targetPacked))
	reclaimable = r.Capacity.InstanceCount - neededNodes
	if reclaimable <= 0 {
		return packed, 0, core.Money{}, false
	}
	price, found := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	if !found {
		return packed, reclaimable, core.Money{}, false
	}
	saving = price.Scale(core.HoursPerMonth * float64(reclaimable))
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEKSConsolidation, "min_monthly_saving", 20)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionResizeNodeGroup) {
		return packed, reclaimable, core.Money{}, false
	}
	return packed, reclaimable, saving, true
}

func (ruleEKSConsolidation) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	packed, reclaimable, saving, ok := decideEKSConsolidation(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("packed fraction", fmt.Sprintf("%.2f", packed)),
		ConfigEvidence("current node count", fmt.Sprintf("%d", r.Capacity.InstanceCount)),
	}
	summary := fmt.Sprintf("%s could consolidate to %d fewer node(s)", r.DisplayName(), reclaimable)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEKSConsolidation{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "Same instance size, fewer nodes — a bin-packing consolidation pass, not a resize.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEKSConsolidation) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, reclaimable, _, ok := decideEKSConsolidation(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	targetCount := r.Capacity.InstanceCount - reclaimable
	return RuleAction{
		Type:          optimize.ActionResizeNodeGroup,
		Parameters:    map[string]any{"node_group_id": r.NativeID, "desired_size": targetCount},
		ProposedState: optimize.StateSnapshot{Count: targetCount},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Consolidate %s to %d node(s)", r.DisplayName(), targetCount),
		Rationale:     f.Detail,
	}
}
