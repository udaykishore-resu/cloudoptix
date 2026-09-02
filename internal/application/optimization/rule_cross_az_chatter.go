package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDCrossAZChatter flags a resource exchanging enough cross-AZ traffic
// with a request-path dependency for the transfer charge alone to rival a
// rightsizing recommendation. It requires two independent confirmations
// before firing: the topology graph must show a real request-path edge to a
// partner discovered in a different AZ (so this is never inferred from
// price-book arithmetic alone), and an attribute must carry the priced
// volume (cross_az_gb_month) — a flow-log-aware discovery adapter's signal
// that this reference implementation's simulator does not populate. No
// executor moves a running fleet's AZ, so the recommendation is advisory.
//
// Traceability: REQ-OPT-007.
const RuleIDCrossAZChatter optimize.RuleID = "cross-az-chatter"

type ruleCrossAZChatter struct{}

func NewCrossAZChatterRule() FullRule { return ruleCrossAZChatter{} }

func (ruleCrossAZChatter) ID() optimize.RuleID { return RuleIDCrossAZChatter }

func (ruleCrossAZChatter) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDCrossAZChatter, Name: "Cross-AZ chatter between co-locatable components",
		Category: optimize.CategoryNetwork, Action: optimize.ActionAdvisoryOnly,
		Description: "Two workload-owned resources on a request path, in different AZs, whose transfer charge rivals a rightsizing win.",
		Kinds:       []cloud.Kind{cloud.KindEC2Instance, cloud.KindRDSInstance, cloud.KindElastiCache}, Enabled: true,
	}
}

func (ruleCrossAZChatter) Applies(r cloud.Resource) bool {
	return r.State.Active() && r.AZ != ""
}

func crossAZPartners(ctx EvalContext, r cloud.Resource) []cloud.Resource {
	var partners []cloud.Resource
	seen := map[core.ID]bool{}
	edges := append(ctx.Topology.Outbound(r.ID), ctx.Topology.Inbound(r.ID)...)
	for _, e := range edges {
		if !e.Kind.TraversesRequestPath() {
			continue
		}
		partnerID := e.ToID
		if partnerID == r.ID {
			partnerID = e.FromID
		}
		if seen[partnerID] {
			continue
		}
		partner, ok := ctx.Inventory.ByID(partnerID)
		if !ok || partner.AZ == "" || partner.AZ == r.AZ {
			continue
		}
		seen[partnerID] = true
		partners = append(partners, partner)
	}
	return partners
}

func decideCrossAZChatter(ctx EvalContext, r cloud.Resource) (gb float64, cost core.Money, ok bool) {
	partners := crossAZPartners(ctx, r)
	if len(partners) == 0 {
		return 0, core.Money{}, false
	}
	gb = parseFloatAttr(r.Attr("cross_az_gb_month", ""), -1)
	if gb < 0 {
		return 0, core.Money{}, false
	}
	minGB := ctx.Thresholds.Float(ctx.TenantID, RuleIDCrossAZChatter, "min_cross_az_gb_month", 500)
	if gb < minGB {
		return gb, core.Money{}, false
	}
	crossAZPrice, found := ctx.Pricing.DataTransferPrice(r.Region, "cross_az")
	if !found {
		return gb, core.Money{}, false
	}
	cost = crossAZPrice.Scale(gb)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDCrossAZChatter, "min_monthly_saving", 20)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return gb, core.Money{}, false
	}
	return gb, cost, true
}

func (ruleCrossAZChatter) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	gb, cost, ok := decideCrossAZChatter(ctx, r)
	if !ok {
		return nil, nil
	}
	partners := crossAZPartners(ctx, r)
	evidence := []optimize.Evidence{
		TopologyEvidence("cross-AZ request-path partners", fmt.Sprintf("%d partner(s) outside %s", len(partners), r.AZ)),
		ConfigEvidence("cross-AZ transfer volume", fmt.Sprintf("%.0f GB/month", gb)),
	}
	summary := fmt.Sprintf("%s exchanges %.0f GB/month cross-AZ with a request-path dependency", r.DisplayName(), gb)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleCrossAZChatter{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Zone-aware placement or co-location would eliminate this transfer charge; no executor moves a running fleet's AZ.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleCrossAZChatter) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Evaluate zone-aware placement for %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
