package optimization

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDRDSOversized rightsizes an RDS instance down one class on P95/P99
// CPU and memory, the same percentile-not-mean discipline as
// ec2-underutilized-rightsize and for the same reason: a database that
// averages low CPU but spikes during a nightly reindex or a monthly batch
// close is not a downsizing candidate.
//
// Traceability: REQ-OPT-005.
const RuleIDRDSOversized optimize.RuleID = "rds-oversized-instance"

type ruleRDSOversized struct{}

func NewRDSOversizedRule() FullRule { return ruleRDSOversized{} }

func (ruleRDSOversized) ID() optimize.RuleID { return RuleIDRDSOversized }

func (ruleRDSOversized) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDRDSOversized, Name: "RDS instance class oversized for observed load",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionResizeRDS,
		Description: "P95/P99 CPU and memory show sustained headroom over the observation window.",
		Kinds:       []cloud.Kind{cloud.KindRDSInstance}, Enabled: true,
	}
}

func (ruleRDSOversized) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindRDSInstance && r.State.Active() && !r.AttrBool("is_read_replica", false)
}

func decideRDSOversized(ctx EvalContext, r cloud.Resource) (m ports.ResourceMetrics, candidate string, curPrice, candPrice, saving core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSOversized, "min_coverage", 0.5)
	minWindow := ctx.Thresholds.Duration(ctx.TenantID, RuleIDRDSOversized, "min_window_hours", time.Hour, 168*time.Hour)
	if !found || m.CPU == nil || !HasSufficientData(m, minCoverage, minWindow) {
		return
	}
	cpuP95Max := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSOversized, "cpu_p95_max", 40)
	cpuP99Max := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSOversized, "cpu_p99_max", 55)
	if m.CPU.P95 > cpuP95Max || m.CPU.P99 > cpuP99Max {
		return
	}
	if m.Memory != nil {
		memP95Max := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSOversized, "mem_p95_max", 55)
		if m.Memory.P95 > memP95Max {
			return
		}
	}
	multiAZ := r.AttrBool("multi_az", false)
	candidate, stepped := stepDBClass(r.InstanceType, false)
	if !stepped {
		return
	}
	curPrice, ok1 := ctx.Pricing.DatabasePrice(r.Region, r.InstanceType, r.Engine, multiAZ)
	candPrice, ok2 := ctx.Pricing.DatabasePrice(r.Region, candidate, r.Engine, multiAZ)
	if !ok1 || !ok2 || !candPrice.LessThan(curPrice) {
		return m, "", core.Money{}, core.Money{}, core.Money{}, false
	}
	saving = curPrice.MustSub(candPrice).Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSOversized, "min_monthly_saving", 20)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionResizeRDS) {
		return m, "", core.Money{}, core.Money{}, core.Money{}, false
	}
	return m, candidate, curPrice, candPrice, saving, true
}

func (ruleRDSOversized) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	m, candidate, curPrice, candPrice, saving, ok := decideRDSOversized(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		MetricEvidence("CPU utilization", m.CPU, m.Window, "cloudwatch"),
		CostEvidence("current vs candidate class", fmt.Sprintf("%s (%s/hr) vs %s (%s/hr)", r.InstanceType, curPrice.Format(), candidate, candPrice.Format()), "pricing_catalog"),
	}
	if m.Memory != nil {
		evidence = append(evidence, MetricEvidence("Memory utilization", m.Memory, m.Window, "cloudwatch"))
	}
	summary := fmt.Sprintf("%s (%s) shows P99 CPU %.0f%%: rightsize to %s", r.DisplayName(), r.InstanceType, m.CPU.P99, candidate)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleRDSOversized{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "P95/P99, never the mean, support stepping down one instance class.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleRDSOversized) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, candidate, _, candPrice, _, ok := decideRDSOversized(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionResizeRDS,
		Parameters:    map[string]any{"db_instance_id": r.NativeID, "instance_class": candidate},
		ProposedState: optimize.StateSnapshot{InstanceType: candidate, MonthlyCost: candPrice.Scale(core.HoursPerMonth)},
		Reversibility: optimize.ReversibilitySlow, // requires a maintenance-window reboot
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Rightsize %s to %s", r.DisplayName(), candidate),
		Rationale:     f.Detail,
	}
}
