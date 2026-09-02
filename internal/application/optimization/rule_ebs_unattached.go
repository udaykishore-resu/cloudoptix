package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEBSUnattached flags an EBS volume in the available (not attached)
// state, gated by an age guard: a volume this rule read as detached ten
// minutes into a live migration must never be proposed for deletion, so the
// rule requires LastSeenAt-relative detachment age rather than firing on
// state alone.
//
// Traceability: REQ-OPT-004.
const RuleIDEBSUnattached optimize.RuleID = "ebs-unattached-volume"

type ruleEBSUnattached struct{}

func NewEBSUnattachedRule() FullRule { return ruleEBSUnattached{} }

func (ruleEBSUnattached) ID() optimize.RuleID { return RuleIDEBSUnattached }

func (ruleEBSUnattached) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEBSUnattached, Name: "Unattached EBS volume", Category: optimize.CategoryWaste,
		Action: optimize.ActionDeleteVolume,
		Description: "A volume in the available state, detached for at least the age guard, " +
			"is billing storage nobody is reading.",
		Kinds: []cloud.Kind{cloud.KindEBSVolume}, Enabled: true,
	}
}

func (ruleEBSUnattached) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEBSVolume && r.State == cloud.StateAvailable
}

func decideEBSUnattached(ctx EvalContext, r cloud.Resource) (days float64, cost core.Money, ok bool) {
	// LastSeenAt tracks discovery presence, not attachment history, so the
	// age guard uses how long the volume has continuously been observed in
	// the available state: FirstSeenAt is the earliest discovery saw it in
	// its current form, which for a volume that just detached is close to
	// the detach moment, and for a volume detached long ago is close to when
	// discovery first ran — either way it never understates the age.
	days = daysSince(r.FirstSeenAt, ctx.Now())
	minDays := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSUnattached, "min_unattached_days", 7)
	if days < minDays {
		return 0, core.Money{}, false
	}
	cost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSUnattached, "min_monthly_saving", 1)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionDeleteVolume) {
		return 0, core.Money{}, false
	}
	return days, cost, true
}

func (ruleEBSUnattached) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	days, cost, ok := decideEBSUnattached(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("state", string(r.State)),
		ConfigEvidence("observed unattached for", fmt.Sprintf("%.0f days", days)),
	}
	summary := fmt.Sprintf("%s has been unattached for %.0f days", r.DisplayName(), days)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEBSUnattached{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "No instance has this volume attached; it clears the age guard for a deletion candidate.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEBSUnattached) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionDeleteVolume,
		Parameters:    map[string]any{"volume_id": r.NativeID, "snapshot_before_delete": true},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Delete unattached volume %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
