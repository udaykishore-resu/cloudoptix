package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEC2OversizedDeclared flags an instance that is oversized relative to
// the workload type declared in the tenant's specification, independent of
// live telemetry. It exists for the gap RuleIDEC2Rightsize cannot cover: a
// freshly-launched batch or worker instance has no metric history yet, but
// "a batch workload does not need a 16-vCPU general-purpose box" is knowable
// from the specification alone the moment it is onboarded.
//
// Traceability: REQ-OPT-003, SPEC-OPT-002.
const RuleIDEC2OversizedDeclared optimize.RuleID = "ec2-oversized-declared-workload"

type ruleEC2OversizedDeclared struct{}

func NewEC2OversizedDeclaredRule() FullRule { return ruleEC2OversizedDeclared{} }

func (ruleEC2OversizedDeclared) ID() optimize.RuleID { return RuleIDEC2OversizedDeclared }

func (ruleEC2OversizedDeclared) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID:       RuleIDEC2OversizedDeclared,
		Name:     "EC2 oversized relative to declared workload type",
		Category: optimize.CategoryRightsizing,
		Action:   optimize.ActionResizeInstance,
		Description: "A batch or worker workload declared in the specification is running on " +
			"a general-purpose instance far larger than that workload type typically needs.",
		Kinds:   []cloud.Kind{cloud.KindEC2Instance},
		Enabled: true,
	}
}

func (ruleEC2OversizedDeclared) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEC2Instance && r.State == cloud.StateRunning && r.InstanceType != ""
}

type oversizedDeclaredDecision struct {
	ok            bool
	workload      string
	spec          ports.InstanceSpec
	candidateType string
	candidateSpec ports.InstanceSpec
	curPrice      core.Money
	candPrice     core.Money
	saving        core.Money
}

func decideEC2OversizedDeclared(ctx EvalContext, r cloud.Resource) oversizedDeclaredDecision {
	w, ok := matchedWorkload(ctx.Spec, r)
	if !ok || w.Type != "batch" && w.Type != "worker" {
		return oversizedDeclaredDecision{}
	}
	curSpec, ok := ctx.Pricing.InstanceSpec(r.InstanceType)
	if !ok {
		return oversizedDeclaredDecision{}
	}
	maxVCPU := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2OversizedDeclared, "batch_max_vcpu", 4)
	if curSpec.VCPU <= maxVCPU {
		return oversizedDeclaredDecision{}
	}
	family := ctx.Pricing.InstanceFamily(r.InstanceType)
	idx := indexOfFold(family, curSpec.Type)
	if idx <= 0 {
		return oversizedDeclaredDecision{}
	}
	// Walk down to the smallest size that still clears the declared ceiling.
	var candType string
	var candSpec ports.InstanceSpec
	for i := idx - 1; i >= 0; i-- {
		cs, ok := ctx.Pricing.InstanceSpec(family[i])
		if !ok {
			continue
		}
		if cs.VCPU < maxFloat(1, maxVCPU/2) {
			break
		}
		candType, candSpec = family[i], cs
	}
	if candType == "" {
		return oversizedDeclaredDecision{}
	}
	curPrice, ok1 := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	candPrice, ok2 := ctx.Pricing.InstancePrice(r.Region, candType, "")
	if !ok1 || !ok2 || !candPrice.LessThan(curPrice) {
		return oversizedDeclaredDecision{}
	}
	saving := curPrice.MustSub(candPrice).Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2OversizedDeclared, "min_monthly_saving", 15)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionResizeInstance) {
		return oversizedDeclaredDecision{}
	}
	return oversizedDeclaredDecision{
		ok: true, workload: w.Name, spec: curSpec, candidateType: candType, candidateSpec: candSpec,
		curPrice: curPrice, candPrice: candPrice, saving: saving,
	}
}

func (ruleEC2OversizedDeclared) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	d := decideEC2OversizedDeclared(ctx, r)
	if !d.ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("declared workload", fmt.Sprintf("%q (batch/worker)", d.workload)),
		ConfigEvidence("current instance type", fmt.Sprintf("%s (%.1f vCPU)", r.InstanceType, d.spec.VCPU)),
		CostEvidence("candidate instance type", fmt.Sprintf("%s (%.1f vCPU)", d.candidateType, d.candidateSpec.VCPU), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s is oversized for its declared %q workload type", r.DisplayName(), d.workload)
	detail := "Declared batch and worker workloads rarely need more than a handful of vCPUs; " +
		"this instance's declared type, not its (possibly absent) telemetry, is the evidence here."
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2OversizedDeclared{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: detail, Evidence: evidence,
		CurrentCost: CostFor(ctx, r), Saving: d.saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEC2OversizedDeclared) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	d := decideEC2OversizedDeclared(ctx, r)
	if !d.ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:       optimize.ActionResizeInstance,
		Parameters: map[string]any{"instance_type": d.candidateType, "current_instance_type": r.InstanceType},
		ProposedState: optimize.StateSnapshot{
			InstanceType: d.candidateType, VCPU: d.candidateSpec.VCPU, MemoryGiB: d.candidateSpec.MemoryGiB,
			MonthlyCost: d.candPrice.Scale(core.HoursPerMonth),
		},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Rightsize %s to %s for its declared workload", r.DisplayName(), d.candidateType),
		Rationale:     f.Detail,
	}
}
