package optimization

import (
	"fmt"
	"math"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDECSTaskCount flags an ECS service whose desired task count is sized
// well above its observed CPU-utilization ceiling: the P99 CPU utilization
// across running tasks stands in for concurrency demand, and a service
// running far under that ceiling is paying for idle task slots. No executor
// in this catalogue changes an ECS service's desired count, so this is
// advisory with the exact reclaimable task count and saving quantified.
//
// Traceability: REQ-OPT-009.
const RuleIDECSTaskCount optimize.RuleID = "ecs-service-task-count"

type ruleECSTaskCount struct{}

func NewECSTaskCountRule() FullRule { return ruleECSTaskCount{} }

func (ruleECSTaskCount) ID() optimize.RuleID { return RuleIDECSTaskCount }

func (ruleECSTaskCount) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDECSTaskCount, Name: "ECS service task count far above observed concurrency",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionAdvisoryOnly,
		Description: "A desired task count sized well above the observed CPU-utilization ceiling is paying for idle task slots.",
		Kinds:       []cloud.Kind{cloud.KindECSService}, Enabled: true,
	}
}

func (ruleECSTaskCount) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindECSService && r.State.Active() && r.Capacity.DesiredCount > 1
}

func decideECSTaskCount(ctx EvalContext, r cloud.Resource) (utilRatio float64, reclaimable int, saving core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	if !found || m.CPU == nil || !HasSufficientData(m, 0.5, 0) {
		return
	}
	utilRatio = m.CPU.P99 / 100.0
	maxRatio := ctx.Thresholds.Float(ctx.TenantID, RuleIDECSTaskCount, "concurrency_over_desired_max", 0.4)
	if utilRatio > maxRatio {
		return utilRatio, 0, core.Money{}, false
	}
	headroom := 1 + ctx.Thresholds.Float(ctx.TenantID, RuleIDECSTaskCount, "headroom_buffer_pct", 20)/100.0
	neededTasks := int(math.Ceil(float64(r.Capacity.DesiredCount) * utilRatio * headroom))
	if neededTasks < 1 {
		neededTasks = 1
	}
	reclaimable = r.Capacity.DesiredCount - neededTasks
	if reclaimable <= 0 {
		return utilRatio, 0, core.Money{}, false
	}
	totalCost := CostFor(ctx, r)
	if totalCost.IsZero() {
		return utilRatio, reclaimable, core.Money{}, false
	}
	perTaskCost := totalCost.Div(float64(r.Capacity.DesiredCount))
	saving = perTaskCost.Scale(float64(reclaimable))
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDECSTaskCount, "min_monthly_saving", 10)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return utilRatio, reclaimable, core.Money{}, false
	}
	return utilRatio, reclaimable, saving, true
}

func (ruleECSTaskCount) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	utilRatio, reclaimable, saving, ok := decideECSTaskCount(ctx, r)
	if !ok {
		return nil, nil
	}
	m, _ := MetricsFor(ctx, r.ID)
	evidence := []optimize.Evidence{
		MetricEvidence("CPU utilization", m.CPU, m.Window, "cloudwatch"),
		ConfigEvidence("desired task count", fmt.Sprintf("%d", r.Capacity.DesiredCount)),
	}
	summary := fmt.Sprintf("%s could reduce desired count by %d task(s) at %.0f%% P99 CPU", r.DisplayName(), reclaimable, utilRatio*100)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleECSTaskCount{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "No executor changes an ECS service's desired count in this catalogue; advisory only.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleECSTaskCount) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, reclaimable, _, ok := decideECSTaskCount(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Parameters:    map[string]any{"service_id": r.NativeID, "reclaimable_tasks": reclaimable, "target_desired_count": r.Capacity.DesiredCount - reclaimable},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Reduce desired count on %s by %d", r.DisplayName(), reclaimable),
		Rationale:     f.Detail,
	}
}
