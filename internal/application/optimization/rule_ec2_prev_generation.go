package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEC2PrevGeneration flags an instance whose price-book entry names a
// same-or-better successor type at a lower on-demand price — including a
// cross-architecture successor (e.g. t3 -> t4g), since InstanceSpec.Successor
// is the catalog's own authoritative answer to "what should this become",
// never guessed by this rule.
//
// Traceability: REQ-OPT-003.
const RuleIDEC2PrevGeneration optimize.RuleID = "ec2-previous-generation-instance"

type ruleEC2PrevGeneration struct{}

func NewEC2PrevGenerationRule() FullRule { return ruleEC2PrevGeneration{} }

func (ruleEC2PrevGeneration) ID() optimize.RuleID { return RuleIDEC2PrevGeneration }

func (ruleEC2PrevGeneration) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEC2PrevGeneration, Name: "Previous-generation instance with a same-or-better successor",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionResizeInstance,
		Description: "The price book names a current-generation successor at equal-or-better " +
			"capacity and a lower on-demand price.",
		Kinds: []cloud.Kind{cloud.KindEC2Instance}, Enabled: true,
	}
}

func (ruleEC2PrevGeneration) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEC2Instance && r.State.Active() && r.InstanceType != ""
}

func decideEC2PrevGeneration(ctx EvalContext, r cloud.Resource) (curSpec, succSpec ports.InstanceSpec, curPrice, succPrice, saving core.Money, ok bool) {
	cs, found := ctx.Pricing.InstanceSpec(r.InstanceType)
	if !found || cs.SuccessorType == "" {
		return ports.InstanceSpec{}, ports.InstanceSpec{}, core.Money{}, core.Money{}, core.Money{}, false
	}
	ss, found := ctx.Pricing.InstanceSpec(cs.SuccessorType)
	if !found || ss.VCPU < cs.VCPU || ss.MemoryGiB < cs.MemoryGiB {
		// The catalog must confirm the successor is capacity-equal-or-better;
		// never trust the name alone.
		return ports.InstanceSpec{}, ports.InstanceSpec{}, core.Money{}, core.Money{}, core.Money{}, false
	}
	cp, ok1 := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	sp, ok2 := ctx.Pricing.InstancePrice(r.Region, cs.SuccessorType, "")
	if !ok1 || !ok2 || !sp.LessThan(cp) {
		return ports.InstanceSpec{}, ports.InstanceSpec{}, core.Money{}, core.Money{}, core.Money{}, false
	}
	sv := cp.MustSub(sp).Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2PrevGeneration, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, sv) || ExcludedBySpec(ctx.Spec, r, optimize.ActionResizeInstance) {
		return ports.InstanceSpec{}, ports.InstanceSpec{}, core.Money{}, core.Money{}, core.Money{}, false
	}
	return cs, ss, cp, sp, sv, true
}

func (ruleEC2PrevGeneration) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	curSpec, succSpec, curPrice, succPrice, saving, ok := decideEC2PrevGeneration(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("current generation", fmt.Sprintf("%s (gen %d, %s)", r.InstanceType, curSpec.Generation, curSpec.Architecture)),
		CostEvidence("successor type", fmt.Sprintf("%s (gen %d, %s) at %s/hr vs %s/hr",
			succSpec.Type, succSpec.Generation, succSpec.Architecture, succPrice.Format(), curPrice.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s runs previous-generation %s; %s is the same-or-better successor", r.DisplayName(), r.InstanceType, succSpec.Type)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2PrevGeneration{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "The successor type has equal or greater vCPU and memory at a lower on-demand rate.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEC2PrevGeneration) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, succSpec, _, succPrice, _, ok := decideEC2PrevGeneration(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:       optimize.ActionResizeInstance,
		Parameters: map[string]any{"instance_type": succSpec.Type, "current_instance_type": r.InstanceType},
		ProposedState: optimize.StateSnapshot{
			InstanceType: succSpec.Type, VCPU: succSpec.VCPU, MemoryGiB: succSpec.MemoryGiB,
			MonthlyCost: succPrice.Scale(core.HoursPerMonth),
		},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Migrate %s to %s", r.DisplayName(), succSpec.Type),
		Rationale:     f.Detail,
	}
}
