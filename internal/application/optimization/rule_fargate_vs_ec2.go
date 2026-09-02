package optimization

import (
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDFargateVsEC2 compares a Fargate ECS service's metered vCPU-hour and
// GB-hour charge against the on-demand price of an EC2 instance sized to the
// service's aggregate desired capacity (VCPU x DesiredCount,
// MemoryGiB x DesiredCount) — no bin-packing credit assumed beyond that
// aggregate match, which is the same "Fargate premium" comparison AWS's own
// guidance uses. Fargate's per-task premium is worthwhile below a
// utilization break-even; above it, the observed CPU utilization means the
// tasks are consistently using what they reserve, and EC2's flat per-instance
// rate wins. Architectural, so advisory only.
//
// Traceability: REQ-OPT-009.
const RuleIDFargateVsEC2 optimize.RuleID = "fargate-vs-ec2-breakeven"

// fargateEC2Seed is the family CloudOptix compares Fargate against — a
// general-purpose, no-burst-credit-complication baseline, matching the seed
// used elsewhere in this package for a similar generic-fleet comparison.
const fargateEC2Seed = "m5.large"

type ruleFargateVsEC2 struct{}

func NewFargateVsEC2Rule() FullRule { return ruleFargateVsEC2{} }

func (ruleFargateVsEC2) ID() optimize.RuleID { return RuleIDFargateVsEC2 }

func (ruleFargateVsEC2) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDFargateVsEC2, Name: "Fargate-vs-EC2 break-even", Category: optimize.CategoryArchitecture,
		Action:      optimize.ActionAdvisoryOnly,
		Description: "Above the utilization break-even, EC2 (or Fargate Spot) prices cheaper than Fargate's per-task premium.",
		Kinds:       []cloud.Kind{cloud.KindECSService}, Enabled: true,
	}
}

func (ruleFargateVsEC2) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindECSService && r.State.Active() && strings.EqualFold(r.Attr("launch_type", ""), "FARGATE")
}

func ec2CandidateForCapacity(ctx EvalContext, region core.Region, vcpu, memGiB float64) (string, ports.InstanceSpec, bool) {
	family := ctx.Pricing.InstanceFamily(fargateEC2Seed)
	for _, t := range family {
		spec, ok := ctx.Pricing.InstanceSpec(t)
		if !ok {
			continue
		}
		if spec.VCPU >= vcpu && spec.MemoryGiB >= memGiB {
			return t, spec, true
		}
	}
	return "", ports.InstanceSpec{}, false
}

func decideFargateVsEC2(ctx EvalContext, r cloud.Resource) (fargateCost, ec2Cost, saving core.Money, candidateType string, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	if !found || m.CPU == nil || !HasSufficientData(m, 0.5, 0) {
		return
	}
	breakeven := ctx.Thresholds.Float(ctx.TenantID, RuleIDFargateVsEC2, "breakeven_utilization_pct", 55)
	if m.CPU.P50 < breakeven {
		return core.Money{}, core.Money{}, core.Money{}, "", false
	}
	desiredCount := maxFloat(float64(r.Capacity.DesiredCount), 1)
	totalVCPU := r.Capacity.VCPU * desiredCount
	totalMemGiB := r.Capacity.MemoryGiB * desiredCount
	if totalVCPU <= 0 || totalMemGiB <= 0 {
		return core.Money{}, core.Money{}, core.Money{}, "", false
	}
	vcpuPrice, ok1 := ctx.Pricing.ServicePrice(r.Region, "fargate", "vcpu_hour")
	gbPrice, ok2 := ctx.Pricing.ServicePrice(r.Region, "fargate", "gb_hour")
	if !ok1 || !ok2 {
		return core.Money{}, core.Money{}, core.Money{}, "", false
	}
	fargateCost = vcpuPrice.Scale(totalVCPU).MustAdd(gbPrice.Scale(totalMemGiB)).Scale(core.HoursPerMonth)

	candidateType, _, found = ec2CandidateForCapacity(ctx, r.Region, totalVCPU, totalMemGiB)
	if !found {
		return core.Money{}, core.Money{}, core.Money{}, "", false
	}
	ec2Price, ok3 := ctx.Pricing.InstancePrice(r.Region, candidateType, "")
	if !ok3 {
		return core.Money{}, core.Money{}, core.Money{}, "", false
	}
	ec2Cost = ec2Price.Scale(core.HoursPerMonth)
	if !ec2Cost.LessThan(fargateCost) {
		return fargateCost, ec2Cost, core.Money{}, candidateType, false
	}
	saving = fargateCost.MustSub(ec2Cost)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDFargateVsEC2, "min_monthly_saving", 20)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return fargateCost, ec2Cost, core.Money{}, candidateType, false
	}
	return fargateCost, ec2Cost, saving, candidateType, true
}

func (ruleFargateVsEC2) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	fargateCost, ec2Cost, saving, candidateType, ok := decideFargateVsEC2(ctx, r)
	if !ok {
		return nil, nil
	}
	m, _ := MetricsFor(ctx, r.ID)
	evidence := []optimize.Evidence{
		MetricEvidence("CPU utilization", m.CPU, m.Window, "cloudwatch"),
		CostEvidence("Fargate vs EC2 equivalent monthly cost", fmt.Sprintf("%s vs %s (%s)", fargateCost.Format(), ec2Cost.Format(), candidateType), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s runs above the Fargate/EC2 utilization break-even", r.DisplayName())
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleFargateVsEC2{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "EC2 comparison assumes no bin-packing credit beyond matching the service's aggregate desired capacity.",
		Evidence: evidence, CurrentCost: fargateCost, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleFargateVsEC2) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilitySlow,
		Complexity:    optimize.ComplexityHigh,
		Title:         fmt.Sprintf("Evaluate EC2 launch type for %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
