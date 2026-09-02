package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEKSNodeGroupOverprovisioned mirrors ec2-underutilized-rightsize's
// family-ladder walk for an EKS managed node group: packed_fraction (the
// node group's own reported pod-request-vs-allocatable ratio) stands in for
// the CPU/memory percentile signal a single EC2 instance would supply, and
// the rule steps down to the smallest instance type in the same family whose
// capacity still clears the observed packing density with a headroom
// buffer. Distinct from eks-consolidation-opportunity, which targets node
// *count* rather than node *size*.
//
// This rule is advisory, not executable, and that is a statement about AWS
// rather than about CloudOptix's ambition. A managed node group's instance
// type is fixed at creation: eks:UpdateNodegroupConfig can change the
// scaling configuration and the labels, and nothing else. Changing the
// instance type means creating a second node group, cordoning and draining
// the first, and deleting it — a migration a human runs, not a single API
// call an executor makes. Emitting an executable resize_node_group action
// carrying a target_instance_type the executor has no field to put it in
// would produce a recommendation that passes policy, passes approval, passes
// preflight, and then fails at the mutate step; saying "advisory" up front
// is the honest version of the same finding.
//
// Traceability: REQ-OPT-009.
const RuleIDEKSNodeGroupOverprovisioned optimize.RuleID = "eks-nodegroup-overprovisioned"

type ruleEKSNodeGroupOverprovisioned struct{}

func NewEKSNodeGroupOverprovisionedRule() FullRule { return ruleEKSNodeGroupOverprovisioned{} }

func (ruleEKSNodeGroupOverprovisioned) ID() optimize.RuleID { return RuleIDEKSNodeGroupOverprovisioned }

func (ruleEKSNodeGroupOverprovisioned) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEKSNodeGroupOverprovisioned, Name: "EKS node group over-provisioned for pod-packing density",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionAdvisoryOnly,
		Description: "A node group whose packed_fraction is low is running nodes larger than the scheduled workload needs. " +
			"Changing a managed node group's instance type requires replacing the node group, so this is architecture advice a human applies.",
		Kinds: []cloud.Kind{cloud.KindEKSNodeGroup}, Enabled: true,
	}
}

func (ruleEKSNodeGroupOverprovisioned) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEKSNodeGroup && r.State.Active() && r.InstanceType != ""
}

type eksOverprovDecision struct {
	ok            bool
	packed        float64
	candidateType string
	candidateSpec ports.InstanceSpec
	currentHourly core.Money
	candHourly    core.Money
	saving        core.Money
}

func decideEKSNodeGroupOverprovisioned(ctx EvalContext, r cloud.Resource) eksOverprovDecision {
	packed := parseFloatAttr(r.Attr("packed_fraction", ""), -1)
	if packed < 0 {
		return eksOverprovDecision{}
	}
	maxPacked := ctx.Thresholds.Float(ctx.TenantID, RuleIDEKSNodeGroupOverprovisioned, "packed_fraction_max", 0.45)
	if packed > maxPacked {
		return eksOverprovDecision{}
	}
	curSpec, ok := ctx.Pricing.InstanceSpec(r.InstanceType)
	if !ok {
		return eksOverprovDecision{}
	}
	family := ctx.Pricing.InstanceFamily(r.InstanceType)
	idx := indexOfFold(family, curSpec.Type)
	if idx <= 0 {
		return eksOverprovDecision{}
	}
	headroom := 1 + ctx.Thresholds.Float(ctx.TenantID, RuleIDEKSNodeGroupOverprovisioned, "headroom_buffer_pct", 20)/100.0
	neededVCPU := curSpec.VCPU * packed * headroom

	var candType string
	var candSpec ports.InstanceSpec
	for i := idx - 1; i >= 0; i-- {
		cs, ok := ctx.Pricing.InstanceSpec(family[i])
		if !ok {
			continue
		}
		if cs.VCPU < neededVCPU {
			break
		}
		candType, candSpec = family[i], cs
		break
	}
	if candType == "" {
		return eksOverprovDecision{}
	}
	curPrice, ok1 := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	candPrice, ok2 := ctx.Pricing.InstancePrice(r.Region, candType, "")
	if !ok1 || !ok2 || !candPrice.LessThan(curPrice) {
		return eksOverprovDecision{}
	}
	nodeCount := maxFloat(float64(r.Capacity.InstanceCount), 1)
	saving := curPrice.MustSub(candPrice).Scale(core.HoursPerMonth * nodeCount)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEKSNodeGroupOverprovisioned, "min_monthly_saving", 20)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return eksOverprovDecision{}
	}
	return eksOverprovDecision{
		ok: true, packed: packed, candidateType: candType, candidateSpec: candSpec,
		currentHourly: curPrice, candHourly: candPrice, saving: saving,
	}
}

func (ruleEKSNodeGroupOverprovisioned) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	d := decideEKSNodeGroupOverprovisioned(ctx, r)
	if !d.ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("packed fraction", fmt.Sprintf("%.2f", d.packed)),
		CostEvidence("current vs candidate instance type", fmt.Sprintf("%s at %s/hr vs %s at %s/hr",
			r.InstanceType, d.currentHourly.Format(), d.candidateType, d.candHourly.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s packs at %.0f%% — rightsize nodes to %s", r.DisplayName(), d.packed*100, d.candidateType)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEKSNodeGroupOverprovisioned{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "packed_fraction is the group's own reported pod-request-vs-allocatable ratio across its nodes.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: d.saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEKSNodeGroupOverprovisioned) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	d := decideEKSNodeGroupOverprovisioned(ctx, r)
	if !d.ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type: optimize.ActionAdvisoryOnly,
		// Parameters on an advisory action are read by a person, not an
		// executor, so they name the migration's inputs rather than an
		// executor's vocabulary.
		Parameters: map[string]any{
			"node_group_id":             r.NativeID,
			"current_instance_type":     r.InstanceType,
			"recommended_instance_type": d.candidateType,
			"migration":                 "create a replacement node group on the recommended type, cordon and drain the current one, then delete it",
		},
		ProposedState: optimize.StateSnapshot{InstanceType: d.candidateType, VCPU: d.candidateSpec.VCPU, MemoryGiB: d.candidateSpec.MemoryGiB, MonthlyCost: d.candHourly.Scale(core.HoursPerMonth)},
		// A node-group replacement is a project, not a fast reversal: undoing
		// it means migrating back the same way it was migrated forward.
		Reversibility: optimize.ReversibilitySlow,
		Complexity:    optimize.ComplexityHigh,
		// Declared explicitly because the action no longer carries it: this
		// advice claims the identical dollars as eks-consolidation-opportunity
		// and k8s-pod-requests-oversized on the same node group, so it must
		// join their conflict group rather than being added to their totals.
		ConflictDomain: optimize.ConflictDomainNodeGroupCapacity,
		Title:          fmt.Sprintf("Rightsize %s from %s to %s", r.DisplayName(), r.InstanceType, d.candidateType),
		Rationale:      f.Detail,
	}
}
