package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEBSOrphanedSnapshot flags a snapshot whose source volume ID no
// longer resolves to any volume in the inventory — distinct from
// ebs-snapshot-retention, which targets snapshots whose volume still exists
// but whose age exceeds policy. An orphaned snapshot has outlived any restore
// workflow tied to a volume that itself no longer exists, independent of age.
//
// Traceability: REQ-OPT-004.
const RuleIDEBSOrphanedSnapshot optimize.RuleID = "ebs-orphaned-snapshot"

type ruleEBSOrphanedSnapshot struct{}

func NewEBSOrphanedSnapshotRule() FullRule { return ruleEBSOrphanedSnapshot{} }

func (ruleEBSOrphanedSnapshot) ID() optimize.RuleID { return RuleIDEBSOrphanedSnapshot }

func (ruleEBSOrphanedSnapshot) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEBSOrphanedSnapshot, Name: "Orphaned snapshot — source volume no longer exists",
		Category: optimize.CategoryWaste, Action: optimize.ActionDeleteSnapshot,
		Description: "The snapshot's source volume ID no longer resolves to a volume in the inventory.",
		Kinds:       []cloud.Kind{cloud.KindEBSSnapshot}, Enabled: true,
	}
}

func (ruleEBSOrphanedSnapshot) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEBSSnapshot && r.Attr("volume_id", "") != ""
}

func decideEBSOrphanedSnapshot(ctx EvalContext, r cloud.Resource) (days float64, cost core.Money, ok bool) {
	volID := r.Attr("volume_id", "")
	if volID == "" {
		return 0, core.Money{}, false
	}
	if _, found := ctx.Inventory.ByNativeID(volID); found {
		return 0, core.Money{}, false // source volume still exists
	}
	days = parseFloatAttr(r.Attr("age_days", ""), daysSince(r.CreatedAt, ctx.Now()))
	minAge := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSOrphanedSnapshot, "min_age_days", 1)
	if days < minAge {
		return 0, core.Money{}, false
	}
	if hasDependentAMI(ctx, r) {
		return 0, core.Money{}, false
	}
	cost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSOrphanedSnapshot, "min_monthly_saving", 0.25)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionDeleteSnapshot) {
		return 0, core.Money{}, false
	}
	return days, cost, true
}

func (ruleEBSOrphanedSnapshot) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	days, cost, ok := decideEBSOrphanedSnapshot(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("source volume_id", r.Attr("volume_id", "")),
		TopologyEvidence("source volume lookup", "not found in inventory"),
	}
	summary := fmt.Sprintf("%s's source volume %s no longer exists", r.DisplayName(), r.Attr("volume_id", ""))
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEBSOrphanedSnapshot{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: fmt.Sprintf("Snapshot is %.0f days old and its source volume is gone; no dependent AMI was found.", days),
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEBSOrphanedSnapshot) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionDeleteSnapshot,
		Parameters:    map[string]any{"snapshot_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Delete orphaned snapshot %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
