package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDLBIdle flags a load balancer whose LCU consumption sits at or near
// the fixed hourly floor: AWS bills at least the base LCU rate regardless of
// traffic, so a load balancer this quiet is serving negligible traffic while
// still paying the full base charge. Removing a load balancer requires
// confirming nothing still resolves to its DNS name, which this rule cannot
// see, so it is advisory.
//
// Traceability: REQ-OPT-007.
const RuleIDLBIdle optimize.RuleID = "load-balancer-idle"

type ruleLBIdle struct{}

func NewLBIdleRule() FullRule { return ruleLBIdle{} }

func (ruleLBIdle) ID() optimize.RuleID { return RuleIDLBIdle }

func (ruleLBIdle) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDLBIdle, Name: "Idle load balancer", Category: optimize.CategoryWaste,
		Action:      optimize.ActionAdvisoryOnly,
		Description: "A load balancer whose LCU consumption sits near the fixed hourly floor is serving negligible traffic.",
		Kinds:       []cloud.Kind{cloud.KindALB, cloud.KindNLB}, Enabled: true,
	}
}

func (ruleLBIdle) Applies(r cloud.Resource) bool {
	return (r.Kind == cloud.KindALB || r.Kind == cloud.KindNLB) && r.State.Active()
}

func lbServiceName(k cloud.Kind) string {
	if k == cloud.KindNLB {
		return "nlb"
	}
	return "alb"
}

func decideLBIdle(ctx EvalContext, r cloud.Resource) (lcu float64, cost core.Money, ok bool) {
	lcu = parseFloatAttr(r.Attr("lcu_hour_avg", ""), -1)
	if lcu < 0 {
		return 0, core.Money{}, false
	}
	maxLCU := ctx.Thresholds.Float(ctx.TenantID, RuleIDLBIdle, "max_lcu_hour_avg", 0.05)
	if lcu > maxLCU {
		return lcu, core.Money{}, false
	}
	cost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDLBIdle, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return lcu, core.Money{}, false
	}
	return lcu, cost, true
}

func (ruleLBIdle) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	lcu, cost, ok := decideLBIdle(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("average consumed LCUs / hour", fmt.Sprintf("%.3f", lcu)),
	}
	summary := fmt.Sprintf("%s (%s) is serving negligible traffic at %.3f LCU/hr average", r.DisplayName(), lbServiceName(r.Kind), lcu)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleLBIdle{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Removing a load balancer requires confirming nothing still resolves to its DNS name; advisory only.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleLBIdle) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Confirm and remove idle load balancer %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
