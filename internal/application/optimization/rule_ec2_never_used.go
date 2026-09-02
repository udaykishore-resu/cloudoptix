package optimization

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEC2NeverUsed flags a running instance whose CPU has never exceeded a
// near-zero ceiling across a full observation window with adequate
// coverage — distinct from underutilized-rightsize because there is no
// smaller instance to step down to that is worth the operational effort;
// the honest recommendation is to stop it.
//
// Traceability: REQ-OPT-003.
const RuleIDEC2NeverUsed optimize.RuleID = "ec2-never-used-instance"

type ruleEC2NeverUsed struct{}

func NewEC2NeverUsedRule() FullRule { return ruleEC2NeverUsed{} }

func (ruleEC2NeverUsed) ID() optimize.RuleID { return RuleIDEC2NeverUsed }

func (ruleEC2NeverUsed) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEC2NeverUsed, Name: "EC2 instance with no observed utilization",
		Category: optimize.CategoryWaste, Action: optimize.ActionStopInstance,
		Description: "A running instance whose P99 CPU has not exceeded a near-zero ceiling " +
			"across a full, well-covered observation window has never done real work.",
		Kinds: []cloud.Kind{cloud.KindEC2Instance}, Enabled: true,
	}
}

func (ruleEC2NeverUsed) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEC2Instance && r.State == cloud.StateRunning
}

func decideEC2NeverUsed(ctx EvalContext, r cloud.Resource) (m ports.ResourceMetrics, cost core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2NeverUsed, "min_coverage", 0.5)
	minWindow := ctx.Thresholds.Duration(ctx.TenantID, RuleIDEC2NeverUsed, "min_window_hours", time.Hour, 336*time.Hour)
	if !found || !HasSufficientData(m, minCoverage, minWindow) || m.CPU == nil {
		return ports.ResourceMetrics{}, core.Money{}, false
	}
	maxCPU := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2NeverUsed, "cpu_p99_max", 2)
	if m.CPU.P99 > maxCPU {
		return ports.ResourceMetrics{}, core.Money{}, false
	}
	cost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2NeverUsed, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionStopInstance) {
		return ports.ResourceMetrics{}, core.Money{}, false
	}
	return m, cost, true
}

func (ruleEC2NeverUsed) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	m, cost, ok := decideEC2NeverUsed(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{MetricEvidence("CPU utilization", m.CPU, m.Window, "cloudwatch")}
	summary := fmt.Sprintf("%s has never exceeded %.0f%% CPU over %.0f days (%.0f%% coverage)", r.DisplayName(), m.CPU.P99, m.Window.Days(), m.Coverage*100)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2NeverUsed{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "No sustained CPU activity was observed at any percentile; this looks unused.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEC2NeverUsed) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionStopInstance,
		Parameters:    map[string]any{"instance_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Stop unused instance %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
