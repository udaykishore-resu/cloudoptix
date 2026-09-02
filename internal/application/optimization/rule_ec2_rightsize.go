package optimization

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleEC2Rightsize is the flagship rule: rightsizing an underutilized EC2
// instance down its family ladder.
//
// It rightsizes on P95 and P99 utilization, never the mean, and this is not
// a stylistic choice. The mean of a CPU series answers "on average, how busy
// was this box", which is exactly the wrong question for a capacity
// decision: an instance can average 8% CPU while touching 95% every night
// during a batch job, or every Monday morning during a traffic spike, or
// every time an incident triggers a retry storm. A mean-based rule sees 8%
// and recommends downsizing straight into that spike, and the failure shows
// up not as a cost regression but as an outage during the exact window the
// business cares about most — the naive-tool failure mode this platform
// exists to not repeat. P95/P99 instead answer "how busy does this box get
// on its worst days", which is the number capacity planning has always
// needed; the mean is discarded from the rightsizing decision entirely
// (it still appears in evidence, for a human's context, never in the logic).
//
// Traceability: REQ-OPT-003, SPEC-OPT-002 (percentile-based rightsizing).
type ruleEC2Rightsize struct{}

// RuleIDEC2Rightsize is this rule's stable identifier.
const RuleIDEC2Rightsize optimize.RuleID = "ec2-underutilized-rightsize"

// NewEC2RightsizeRule constructs the rule.
func NewEC2RightsizeRule() FullRule { return ruleEC2Rightsize{} }

func (ruleEC2Rightsize) ID() optimize.RuleID { return RuleIDEC2Rightsize }

func (ruleEC2Rightsize) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID:       RuleIDEC2Rightsize,
		Name:     "EC2 underutilized — rightsize down the family ladder",
		Category: optimize.CategoryRightsizing,
		Action:   optimize.ActionResizeInstance,
		Description: "Steps a running EC2 instance down one rung on its instance-family " +
			"ladder when P95/P99 CPU and memory utilization both show sustained headroom.",
		Kinds:   []cloud.Kind{cloud.KindEC2Instance},
		Enabled: true,
	}
}

func (ruleEC2Rightsize) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEC2Instance && r.State == cloud.StateRunning &&
		r.Purchase != cloud.PurchaseSpot && r.InstanceType != ""
}

// ec2RightsizeDecision is the one place this rule decides whether, and to
// what, an instance should be resized. Evaluate and BuildAction both call it
// so the finding and the action it proposes can never disagree.
type ec2RightsizeDecision struct {
	ok              bool
	currentSpec     ports.InstanceSpec
	candidateType   string
	candidateSpec   ports.InstanceSpec
	currentHourly   core.Money
	candidateHourly core.Money
	monthlySaving   core.Money
	metrics         ports.ResourceMetrics
}

func decideEC2Rightsize(ctx EvalContext, r cloud.Resource) ec2RightsizeDecision {
	m, ok := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2Rightsize, "min_coverage", 0.5)
	minWindow := ctx.Thresholds.Duration(ctx.TenantID, RuleIDEC2Rightsize, "min_window_hours", time.Hour, 168*time.Hour)
	if !ok || !HasSufficientData(m, minCoverage, minWindow) || m.CPU == nil {
		return ec2RightsizeDecision{}
	}

	cpuP95Max := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2Rightsize, "cpu_p95_max", 40)
	cpuP99Max := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2Rightsize, "cpu_p99_max", 55)
	if m.CPU.P95 > cpuP95Max || m.CPU.P99 > cpuP99Max {
		return ec2RightsizeDecision{}
	}
	if m.Memory != nil {
		memP95Max := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2Rightsize, "mem_p95_max", 55)
		memP99Max := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2Rightsize, "mem_p99_max", 70)
		if m.Memory.P95 > memP95Max || m.Memory.P99 > memP99Max {
			return ec2RightsizeDecision{}
		}
	}

	curSpec, ok := ctx.Pricing.InstanceSpec(r.InstanceType)
	if !ok {
		return ec2RightsizeDecision{}
	}
	family := ctx.Pricing.InstanceFamily(r.InstanceType)
	idx := indexOfFold(family, curSpec.Type)
	if idx <= 0 {
		return ec2RightsizeDecision{} // already the smallest size in the family, or unknown
	}

	headroom := 1 + ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2Rightsize, "headroom_buffer_pct", 15)/100.0
	neededVCPU := curSpec.VCPU * (m.CPU.P99 / 100.0) * headroom
	neededMemGiB := 0.0
	if m.Memory != nil {
		neededMemGiB = curSpec.MemoryGiB * (m.Memory.P99 / 100.0) * headroom
	}

	// Walk down the ladder from the current size, taking the first (closest)
	// candidate whose capacity still clears the P99-plus-headroom requirement
	// — never jump straight to the smallest available size, which is what
	// would erase the very headroom this rule just measured.
	var candType string
	var candSpec ports.InstanceSpec
	for i := idx - 1; i >= 0; i-- {
		cs, ok := ctx.Pricing.InstanceSpec(family[i])
		if !ok {
			continue
		}
		if cs.VCPU < neededVCPU || cs.MemoryGiB < neededMemGiB {
			break // this size and everything smaller cannot hold the load
		}
		candType, candSpec = family[i], cs
		break
	}
	if candType == "" {
		return ec2RightsizeDecision{}
	}

	curPrice, ok1 := ctx.Pricing.InstancePrice(r.Region, r.InstanceType, "")
	candPrice, ok2 := ctx.Pricing.InstancePrice(r.Region, candType, "")
	if !ok1 || !ok2 || !candPrice.LessThan(curPrice) {
		return ec2RightsizeDecision{}
	}

	saving := curPrice.MustSub(candPrice).Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2Rightsize, "min_monthly_saving", 15)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) {
		return ec2RightsizeDecision{}
	}
	if ExcludedBySpec(ctx.Spec, r, optimize.ActionResizeInstance) {
		return ec2RightsizeDecision{}
	}

	return ec2RightsizeDecision{
		ok: true, currentSpec: curSpec, candidateType: candType, candidateSpec: candSpec,
		currentHourly: curPrice, candidateHourly: candPrice, monthlySaving: saving, metrics: m,
	}
}

func (ruleEC2Rightsize) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	d := decideEC2Rightsize(ctx, r)
	if !d.ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		MetricEvidence("CPU utilization", d.metrics.CPU, d.metrics.Window, "cloudwatch"),
	}
	if d.metrics.Memory != nil {
		evidence = append(evidence, MetricEvidence("Memory utilization", d.metrics.Memory, d.metrics.Window, "cloudwatch"))
	}
	evidence = append(evidence,
		ConfigEvidence("current instance type", fmt.Sprintf("%s (%.1f vCPU, %.1f GiB)", r.InstanceType, d.currentSpec.VCPU, d.currentSpec.MemoryGiB)),
		CostEvidence("candidate instance type", fmt.Sprintf("%s (%.1f vCPU, %.1f GiB) at %s/hr vs %s/hr",
			d.candidateType, d.candidateSpec.VCPU, d.candidateSpec.MemoryGiB, d.candidateHourly.Format(), d.currentHourly.Format()), "pricing_catalog"),
	)

	summary := fmt.Sprintf("%s (%s) shows P99 CPU %.0f%%: rightsize to %s", r.DisplayName(), r.InstanceType, d.metrics.CPU.P99, d.candidateType)
	detail := fmt.Sprintf("Over a %.1f-day window with %.0f%% metric coverage, P95/P99 CPU stayed at %.0f%%/%.0f%%, "+
		"never the mean, which is what this decision is made on. %s clears the observed peak with a %.0f%% headroom buffer.",
		d.metrics.Window.Days(), d.metrics.Coverage*100, d.metrics.CPU.P95, d.metrics.CPU.P99, d.candidateType,
		ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2Rightsize, "headroom_buffer_pct", 15))

	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2Rightsize{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: detail, Evidence: evidence,
		CurrentCost: CostFor(ctx, r), Saving: d.monthlySaving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEC2Rightsize) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	d := decideEC2Rightsize(ctx, r)
	if !d.ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type: optimize.ActionResizeInstance,
		Parameters: map[string]any{
			"instance_type":         d.candidateType,
			"current_instance_type": r.InstanceType,
		},
		ProposedState: optimize.StateSnapshot{
			InstanceType: d.candidateType,
			VCPU:         d.candidateSpec.VCPU,
			MemoryGiB:    d.candidateSpec.MemoryGiB,
			MonthlyCost:  d.candidateHourly.Scale(core.HoursPerMonth),
		},
		Reversibility: optimize.ReversibilityFast, // resize back is one API call plus a restart
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Rightsize %s from %s to %s", r.DisplayName(), r.InstanceType, d.candidateType),
		Rationale:     f.Detail,
	}
}
