package optimization

import (
	"fmt"
	"math"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDK8sPodRequestsOversized quantifies the single largest source of
// Kubernetes waste: pods requesting far more CPU/memory than they use force
// the scheduler to reserve capacity nobody consumes, so the node group runs
// more nodes than the workload's real footprint needs.
//
// Traceability: REQ-OPT-009.
const RuleIDK8sPodRequestsOversized optimize.RuleID = "k8s-pod-requests-oversized"

type ruleK8sPodRequestsOversized struct{}

func NewK8sPodRequestsOversizedRule() FullRule { return ruleK8sPodRequestsOversized{} }

func (ruleK8sPodRequestsOversized) ID() optimize.RuleID { return RuleIDK8sPodRequestsOversized }

func (ruleK8sPodRequestsOversized) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDK8sPodRequestsOversized, Name: "Pod requests far above observed usage",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionAdjustPodResources,
		Description: "Quantifies the reclaimable node count when pod requests sit far above observed usage.",
		Kinds:       []cloud.Kind{cloud.KindEKSNodeGroup}, Enabled: true,
	}
}

func (ruleK8sPodRequestsOversized) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEKSNodeGroup && r.State.Active() && r.Capacity.InstanceCount > 0
}

// PodRequestReclaimableNodes computes how many of currentNodes a node group
// could shed if pod resource requests matched observed usage with the given
// headroom multiplier (>= 1.0), instead of requestedOverActualRatio times
// observed usage. A ratio at or below the headroom needs no correction and
// reclaims zero nodes; the result never exceeds currentNodes-1, since the
// group always needs at least one node while pods still run on it.
func PodRequestReclaimableNodes(currentNodes int, requestedOverActualRatio, headroom float64) int {
	if currentNodes <= 0 || requestedOverActualRatio <= 0 || headroom <= 0 {
		return 0
	}
	if requestedOverActualRatio <= headroom {
		return 0
	}
	neededNodes := math.Ceil(float64(currentNodes) * headroom / requestedOverActualRatio)
	reclaimable := currentNodes - int(neededNodes)
	if reclaimable < 0 {
		return 0
	}
	if reclaimable > currentNodes-1 {
		reclaimable = currentNodes - 1
	}
	return reclaimable
}

func decideK8sPodRequestsOversized(ctx EvalContext, r cloud.Resource) (ratio float64, reclaimable int, saving core.Money, ok bool) {
	ratio = parseFloatAttr(r.Attr("requested_over_actual_ratio", ""), -1)
	if ratio < 0 {
		return
	}
	maxRatio := ctx.Thresholds.Float(ctx.TenantID, RuleIDK8sPodRequestsOversized, "requested_over_actual_max", 1.8)
	if ratio <= maxRatio {
		return ratio, 0, core.Money{}, false
	}
	headroom := 1 + ctx.Thresholds.Float(ctx.TenantID, RuleIDK8sPodRequestsOversized, "headroom_buffer_pct", 20)/100.0
	reclaimable = PodRequestReclaimableNodes(r.Capacity.InstanceCount, ratio, headroom)
	if reclaimable <= 0 {
		return ratio, 0, core.Money{}, false
	}
	price, found := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	if !found {
		return ratio, reclaimable, core.Money{}, false
	}
	saving = price.Scale(core.HoursPerMonth * float64(reclaimable))
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDK8sPodRequestsOversized, "min_monthly_saving", 15)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdjustPodResources) {
		return ratio, reclaimable, core.Money{}, false
	}
	return ratio, reclaimable, saving, true
}

func (ruleK8sPodRequestsOversized) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	ratio, reclaimable, saving, ok := decideK8sPodRequestsOversized(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("requested-over-actual ratio", fmt.Sprintf("%.2fx", ratio)),
		ConfigEvidence("current node count", fmt.Sprintf("%d", r.Capacity.InstanceCount)),
	}
	summary := fmt.Sprintf("%s could shed %d node(s) if pod requests matched usage", r.DisplayName(), reclaimable)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleK8sPodRequestsOversized{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "Reclaimable node count is computed with a headroom buffer, not a bare 1:1 shrink to observed usage.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleK8sPodRequestsOversized) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, reclaimable, _, ok := decideK8sPodRequestsOversized(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	targetCount := r.Capacity.InstanceCount - reclaimable
	return RuleAction{
		Type:          optimize.ActionAdjustPodResources,
		Parameters:    map[string]any{"node_group_id": r.NativeID, "reclaimable_nodes": reclaimable, "desired_size": targetCount},
		ProposedState: optimize.StateSnapshot{Count: targetCount},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Right-size pod requests on %s to reclaim %d node(s)", r.DisplayName(), reclaimable),
		Rationale:     f.Detail,
	}
}
