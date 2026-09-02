package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEBSSnapshotRetention flags an EBS snapshot older than the tenant's
// retention policy with no dependent AMI. A snapshot backing a registered AMI
// is excluded regardless of age — deleting it would silently break any future
// launch from that AMI, which is exactly the kind of quiet blast radius this
// rule must never create.
//
// Traceability: REQ-OPT-004.
const RuleIDEBSSnapshotRetention optimize.RuleID = "ebs-snapshot-retention"

type ruleEBSSnapshotRetention struct{}

func NewEBSSnapshotRetentionRule() FullRule { return ruleEBSSnapshotRetention{} }

func (ruleEBSSnapshotRetention) ID() optimize.RuleID { return RuleIDEBSSnapshotRetention }

func (ruleEBSSnapshotRetention) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEBSSnapshotRetention, Name: "Snapshot older than retention policy with no dependent AMI",
		Category: optimize.CategoryDataLifecycle, Action: optimize.ActionDeleteSnapshot,
		Description: "A manual snapshot older than the retention policy and not backing any " +
			"registered AMI is a deletion candidate.",
		Kinds: []cloud.Kind{cloud.KindEBSSnapshot}, Enabled: true,
	}
}

func (ruleEBSSnapshotRetention) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEBSSnapshot
}

// hasDependentAMI reports whether any AMI in the inventory was built from
// this snapshot. It walks the topology's attached_to edges first (the
// authoritative signal when discovered) and falls back to the snapshot's own
// volume_id attribute correlated against AMI attributes as a defensive
// second check, since not every discovery adapter populates the edge.
func hasDependentAMI(ctx EvalContext, snap cloud.Resource) bool {
	for _, e := range ctx.Topology.Inbound(snap.ID, cloud.RelAttachedTo, cloud.RelContains) {
		if dep, ok := ctx.Inventory.ByID(e.FromID); ok && dep.Kind == cloud.KindAMI {
			return true
		}
	}
	for _, ami := range ctx.Inventory.OfKind(cloud.KindAMI) {
		if ami.Attr("source_snapshot_id", "") == snap.NativeID {
			return true
		}
	}
	return false
}

func decideEBSSnapshotRetention(ctx EvalContext, r cloud.Resource) (days, retentionDays float64, cost core.Money, ok bool) {
	days = parseFloatAttr(r.Attr("age_days", ""), daysSince(r.CreatedAt, ctx.Now()))
	retentionDays = ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSSnapshotRetention, "retention_days", 90)
	if days < retentionDays {
		return 0, 0, core.Money{}, false
	}
	if hasDependentAMI(ctx, r) {
		return 0, 0, core.Money{}, false
	}
	cost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSSnapshotRetention, "min_monthly_saving", 0.5)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionDeleteSnapshot) {
		return 0, 0, core.Money{}, false
	}
	return days, retentionDays, cost, true
}

func (ruleEBSSnapshotRetention) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	days, retention, cost, ok := decideEBSSnapshotRetention(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("age", fmt.Sprintf("%.0f days (retention policy: %.0f days)", days, retention)),
		TopologyEvidence("dependent AMIs", "none found"),
	}
	summary := fmt.Sprintf("%s is %.0f days old, past the %.0f-day retention policy, with no dependent AMI", r.DisplayName(), days, retention)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEBSSnapshotRetention{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Confirmed no registered AMI references this snapshot before proposing deletion.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEBSSnapshotRetention) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionDeleteSnapshot,
		Parameters:    map[string]any{"snapshot_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Delete expired snapshot %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
