package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEIPUnattached flags an Elastic IP not attached to a running instance
// or NAT gateway: AWS bills an idle-hour charge for exactly this case, with
// no offsetting use.
//
// Traceability: REQ-OPT-007.
const RuleIDEIPUnattached optimize.RuleID = "elastic-ip-unattached"

type ruleEIPUnattached struct{}

func NewEIPUnattachedRule() FullRule { return ruleEIPUnattached{} }

func (ruleEIPUnattached) ID() optimize.RuleID { return RuleIDEIPUnattached }

func (ruleEIPUnattached) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEIPUnattached, Name: "Unattached Elastic IP", Category: optimize.CategoryWaste,
		Action:      optimize.ActionReleaseElasticIP,
		Description: "An Elastic IP not attached to a running instance or NAT gateway bills an idle-hour charge for nothing.",
		Kinds:       []cloud.Kind{cloud.KindElasticIP}, Enabled: true,
	}
}

func (ruleEIPUnattached) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindElasticIP && r.State != cloud.StateInUse
}

func decideEIPUnattached(ctx EvalContext, r cloud.Resource) (days float64, saving core.Money, ok bool) {
	days = daysSince(r.FirstSeenAt, ctx.Now())
	minDays := ctx.Thresholds.Float(ctx.TenantID, RuleIDEIPUnattached, "min_unattached_days", 1)
	if days < minDays {
		return 0, core.Money{}, false
	}
	idlePrice, found := ctx.Pricing.ServicePrice(r.Region, "elastic_ip", "idle_hour")
	if !found {
		return days, core.Money{}, false
	}
	saving = idlePrice.Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEIPUnattached, "min_monthly_saving", 1)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionReleaseElasticIP) {
		return days, core.Money{}, false
	}
	return days, saving, true
}

func (ruleEIPUnattached) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	days, saving, ok := decideEIPUnattached(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("state", string(r.State)),
		ConfigEvidence("observed unattached for", fmt.Sprintf("%.0f days", days)),
	}
	summary := fmt.Sprintf("%s has been unattached for %.0f days", r.DisplayName(), days)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEIPUnattached{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "No instance or NAT gateway holds this address; releasing it stops the idle-hour charge.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEIPUnattached) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionReleaseElasticIP,
		Parameters:    map[string]any{"allocation_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Release unattached Elastic IP %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
