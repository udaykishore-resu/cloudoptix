package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDNATRedundant flags a per-AZ NAT gateway whose processed volume is
// low enough that consolidating onto a sibling NAT gateway in the same
// region — accepting the cross-AZ data-transfer charge as the trade-off —
// would cost less than keeping this one running. It only fires when a
// sibling NAT gateway exists to consolidate onto; a lone NAT gateway is not
// redundant no matter how little it processes.
//
// Traceability: REQ-OPT-007.
const RuleIDNATRedundant optimize.RuleID = "nat-gateway-redundant"

type ruleNATRedundant struct{}

func NewNATRedundantRule() FullRule { return ruleNATRedundant{} }

func (ruleNATRedundant) ID() optimize.RuleID { return RuleIDNATRedundant }

func (ruleNATRedundant) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDNATRedundant, Name: "Redundant per-AZ NAT gateway", Category: optimize.CategoryNetwork,
		Action: optimize.ActionRemoveNATGateway,
		Description: "A per-AZ NAT gateway with a sibling to consolidate onto and low enough processed volume " +
			"costs less removed, even after the added cross-AZ transfer charge.",
		Kinds: []cloud.Kind{cloud.KindNATGateway}, Enabled: true,
	}
}

func (ruleNATRedundant) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindNATGateway && r.State.Active()
}

func natSiblings(ctx EvalContext, r cloud.Resource) []cloud.Resource {
	var siblings []cloud.Resource
	for _, other := range ctx.Inventory.OfKind(cloud.KindNATGateway) {
		if other.ID == r.ID || !other.State.Active() || other.Region != r.Region {
			continue
		}
		siblings = append(siblings, other)
	}
	return siblings
}

func decideNATRedundant(ctx EvalContext, r cloud.Resource) (gb float64, saving core.Money, ok bool) {
	if len(natSiblings(ctx, r)) == 0 {
		return 0, core.Money{}, false
	}
	gb = parseFloatAttr(r.Attr("gb_processed_month", ""), -1)
	if gb < 0 {
		return 0, core.Money{}, false
	}
	maxGB := ctx.Thresholds.Float(ctx.TenantID, RuleIDNATRedundant, "max_gb_processed_month", 200)
	if gb > maxGB {
		return gb, core.Money{}, false
	}
	hoursPrice, ok1 := ctx.Pricing.ServicePrice(r.Region, "nat_gateway", "hours")
	gbPrice, ok2 := ctx.Pricing.ServicePrice(r.Region, "nat_gateway", "gb_processed")
	crossAZPrice, ok3 := ctx.Pricing.DataTransferPrice(r.Region, "cross_az")
	if !ok1 || !ok2 || !ok3 {
		return gb, core.Money{}, false
	}
	removedCost := hoursPrice.Scale(core.HoursPerMonth).MustAdd(gbPrice.Scale(gb))
	// Consolidating routes this NAT's traffic across AZs to reach the
	// sibling, which is not free — subtract that added charge rather than
	// counting the full removed cost as saving.
	addedCrossAZCost := crossAZPrice.Scale(gb)
	if !addedCrossAZCost.LessThan(removedCost) {
		return gb, core.Money{}, false
	}
	saving = removedCost.MustSub(addedCrossAZCost)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDNATRedundant, "min_monthly_saving", 15)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionRemoveNATGateway) {
		return gb, core.Money{}, false
	}
	return gb, saving, true
}

func (ruleNATRedundant) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	gb, saving, ok := decideNATRedundant(ctx, r)
	if !ok {
		return nil, nil
	}
	siblings := natSiblings(ctx, r)
	evidence := []optimize.Evidence{
		ConfigEvidence("processed GB / month", fmt.Sprintf("%.0f", gb)),
		TopologyEvidence("consolidation target", fmt.Sprintf("%d sibling NAT gateway(s) in %s", len(siblings), r.Region)),
	}
	summary := fmt.Sprintf("%s processes little enough traffic to consolidate onto a sibling NAT gateway", r.DisplayName())
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleNATRedundant{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Saving nets out the base NAT charge removed against the cross-AZ transfer charge added by consolidating.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleNATRedundant) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionRemoveNATGateway,
		Parameters:    map[string]any{"nat_gateway_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilitySlow,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Remove redundant NAT gateway %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
