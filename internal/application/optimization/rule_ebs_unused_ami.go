package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEBSUnusedAMI flags an AMI not referenced by any instance's launch
// configuration and older than the age guard. Deregistering it orphans its
// backing snapshots, which ebs-orphaned-snapshot then picks up independently
// on its own next run — the two rules are deliberately not fused into one so
// each keeps its own evidence and its own age guard.
//
// Traceability: REQ-OPT-004.
const RuleIDEBSUnusedAMI optimize.RuleID = "ebs-unused-ami"

type ruleEBSUnusedAMI struct{}

func NewEBSUnusedAMIRule() FullRule { return ruleEBSUnusedAMI{} }

func (ruleEBSUnusedAMI) ID() optimize.RuleID { return RuleIDEBSUnusedAMI }

func (ruleEBSUnusedAMI) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEBSUnusedAMI, Name: "Unused AMI", Category: optimize.CategoryWaste,
		Action: optimize.ActionDeregisterAMI,
		Description: "An AMI not referenced by any running instance and older than the age " +
			"guard is a deregistration candidate.",
		Kinds: []cloud.Kind{cloud.KindAMI}, Enabled: true,
	}
}

func (ruleEBSUnusedAMI) Applies(r cloud.Resource) bool { return r.Kind == cloud.KindAMI }

func amiInUse(ctx EvalContext, ami cloud.Resource) bool {
	for _, e := range ctx.Topology.Outbound(ami.ID, cloud.RelContains, cloud.RelRunsOn) {
		if _, ok := ctx.Inventory.ByID(e.ToID); ok {
			return true
		}
	}
	for _, inst := range ctx.Inventory.OfKind(cloud.KindEC2Instance) {
		if inst.Attr("source_ami_id", "") == ami.NativeID {
			return true
		}
	}
	return false
}

func decideEBSUnusedAMI(ctx EvalContext, r cloud.Resource) (days float64, cost core.Money, ok bool) {
	if amiInUse(ctx, r) {
		return 0, core.Money{}, false
	}
	days = daysSince(r.CreatedAt, ctx.Now())
	minAge := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSUnusedAMI, "min_age_days", 90)
	if days < minAge {
		return 0, core.Money{}, false
	}
	cost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSUnusedAMI, "min_monthly_saving", 0.5)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionDeregisterAMI) {
		return 0, core.Money{}, false
	}
	return days, cost, true
}

func (ruleEBSUnusedAMI) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	days, cost, ok := decideEBSUnusedAMI(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		TopologyEvidence("referencing instances", "none found"),
		ConfigEvidence("age", fmt.Sprintf("%.0f days", days)),
	}
	summary := fmt.Sprintf("%s is unreferenced by any instance and %.0f days old", r.DisplayName(), days)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEBSUnusedAMI{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "No running instance's launch configuration references this AMI.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEBSUnusedAMI) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionDeregisterAMI,
		Parameters:    map[string]any{"ami_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Deregister unused AMI %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
