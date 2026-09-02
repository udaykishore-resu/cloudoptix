package optimization

import (
	"fmt"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEC2BurstCredit flags a t-family instance running in unlimited-credit
// mode whose sustained CPU sits well above its baseline: unlimited mode never
// throttles, it bills the overage as surplus credits at a per-vCPU-hour rate,
// and a workload that burns surplus credits steadily is often already
// cheaper on a fixed-performance instance of comparable capacity.
//
// The rule prices this from the tenant's own attributed bill, not a modelled
// surplus-credit rate: CostFor(ctx, r) already reflects whatever AWS actually
// charged, surplus credits included, so comparing it against the catalog's
// on-demand price for a same-or-better fixed instance is a real, unfabricated
// comparison rather than a second pricing model layered on top of the first.
//
// Traceability: REQ-OPT-003.
const RuleIDEC2BurstCredit optimize.RuleID = "ec2-burstable-credit-exhaustion"

type ruleEC2BurstCredit struct{}

func NewEC2BurstCreditRule() FullRule { return ruleEC2BurstCredit{} }

func (ruleEC2BurstCredit) ID() optimize.RuleID { return RuleIDEC2BurstCredit }

func (ruleEC2BurstCredit) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEC2BurstCredit, Name: "Burstable instance on unlimited credits costing more than fixed-size",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionResizeInstance,
		Description: "A t-family instance in unlimited credit mode with sustained CPU above " +
			"baseline is paying surplus-credit charges that exceed a fixed-performance equivalent.",
		Kinds: []cloud.Kind{cloud.KindEC2Instance}, Enabled: true,
	}
}

func (ruleEC2BurstCredit) Applies(r cloud.Resource) bool {
	if r.Kind != cloud.KindEC2Instance || !r.State.Active() {
		return false
	}
	t := strings.ToLower(r.InstanceType)
	return strings.HasPrefix(t, "t2.") || strings.HasPrefix(t, "t3.") || strings.HasPrefix(t, "t3a.") || strings.HasPrefix(t, "t4g.")
}

// burstFixedFamilySeed names, for each burstable family, one instance type in
// a fixed-performance family of comparable vintage/architecture whose family
// list (InstanceFamily) is walked to find the smallest capacity-sufficient
// candidate. Using a seed rather than a bare family name lets the catalog's
// own InstanceFamily/InstanceSpec calls be the source of truth for what
// exists, rather than this rule guessing a size.
var burstFixedFamilySeed = map[string]string{
	"t2": "m5.large", "t3": "m5.large", "t3a": "m5a.large", "t4g": "m6g.large",
}

func decideEC2BurstCredit(ctx EvalContext, r cloud.Resource) (m ports.ResourceMetrics, candType string, candMonthly, actualCost, saving core.Money, ok bool) {
	curSpec, found := ctx.Pricing.InstanceSpec(r.InstanceType)
	if !found || !curSpec.Burstable {
		return
	}
	if strings.EqualFold(r.Attr("credit_specification", "unlimited"), "standard") {
		return
	}
	m, found = MetricsFor(ctx, r.ID)
	if !found || m.CPU == nil || !HasSufficientData(m, 0.4, 7*24*time.Hour) {
		return
	}
	cpuP50Min := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2BurstCredit, "cpu_p50_min", 35)
	if m.CPU.P50 < cpuP50Min {
		return
	}
	seed, known := burstFixedFamilySeed[curSpec.Family]
	if !known {
		return
	}
	family := ctx.Pricing.InstanceFamily(seed)
	for _, t := range family {
		cs, found := ctx.Pricing.InstanceSpec(t)
		if !found || cs.VCPU < curSpec.VCPU || cs.MemoryGiB < curSpec.MemoryGiB {
			continue
		}
		price, found := ctx.Pricing.InstancePrice(r.Region, t, "")
		if !found {
			continue
		}
		candType = t
		candMonthly = price.Scale(core.HoursPerMonth)
		break
	}
	if candType == "" {
		return
	}
	actualCost = CostFor(ctx, r)
	if !actualCost.GreaterThan(candMonthly) {
		return // still cheaper on burst credits than a fixed instance would be
	}
	saving = actualCost.MustSub(candMonthly)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2BurstCredit, "min_monthly_saving", 8)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionResizeInstance) {
		candType = ""
		return
	}
	ok = true
	return
}

func (ruleEC2BurstCredit) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	m, candType, candMonthly, actualCost, saving, ok := decideEC2BurstCredit(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		MetricEvidence("CPU utilization", m.CPU, m.Window, "cloudwatch"),
		ConfigEvidence("credit specification", r.Attr("credit_specification", "unlimited")),
		CostEvidence("attributed bill vs fixed-instance on-demand", fmt.Sprintf("%s billed vs %s for %s", actualCost.Format(), candMonthly.Format(), candType), "cost_engine"),
	}
	summary := fmt.Sprintf("%s sustains P50 CPU %.0f%% on unlimited credits, costing more than %s would", r.DisplayName(), m.CPU.P50, candType)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2BurstCredit{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "Sustained above-baseline CPU on an unlimited t-family instance is billing surplus credits past the point where a fixed-performance instance is cheaper.",
		Evidence: evidence, CurrentCost: actualCost, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEC2BurstCredit) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, candType, candMonthly, _, _, ok := decideEC2BurstCredit(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	candSpec, _ := ctx.Pricing.InstanceSpec(candType)
	return RuleAction{
		Type:       optimize.ActionResizeInstance,
		Parameters: map[string]any{"instance_type": candType, "current_instance_type": r.InstanceType},
		ProposedState: optimize.StateSnapshot{
			InstanceType: candType, VCPU: candSpec.VCPU, MemoryGiB: candSpec.MemoryGiB, MonthlyCost: candMonthly,
		},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Move %s off burst credits to %s", r.DisplayName(), candType),
		Rationale:     f.Detail,
	}
}
