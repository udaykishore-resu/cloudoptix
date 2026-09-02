package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDCWLogRetention flags a log group with no retention policy (modelled
// as Capacity.RetentionDays == 0, "Never Expire"): storage accumulates
// forever with nothing to bound it. The saving is estimated by assuming the
// group's current stored size reflects unbounded accumulation over its
// observed age, and that bounding retention at the recommended ceiling
// would, at steady state, hold storage to that same fraction of its current
// size — the same "estimate, documented as such" approach used by
// rds-excessive-backup-retention, rather than a bare guess.
//
// Traceability: REQ-OPT-010.
const RuleIDCWLogRetention optimize.RuleID = "cloudwatch-log-retention-unbounded"

type ruleCWLogRetention struct{}

func NewCWLogRetentionRule() FullRule { return ruleCWLogRetention{} }

func (ruleCWLogRetention) ID() optimize.RuleID { return RuleIDCWLogRetention }

func (ruleCWLogRetention) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDCWLogRetention, Name: "CloudWatch log group with infinite retention",
		Category: optimize.CategoryDataLifecycle, Action: optimize.ActionSetLogRetention,
		Description: "A log group with no retention policy accumulates storage charges forever.",
		Kinds:       []cloud.Kind{cloud.KindLogGroup}, Enabled: true,
	}
}

func (ruleCWLogRetention) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindLogGroup && r.State.Active() && r.Capacity.RetentionDays == 0
}

func decideCWLogRetention(ctx EvalContext, r cloud.Resource) (ageDays float64, currentCost, saving core.Money, ok bool) {
	sizeGiB := r.Capacity.StorageGiB
	if sizeGiB <= 0 {
		return
	}
	recommendedDays := ctx.Thresholds.Float(ctx.TenantID, RuleIDCWLogRetention, "recommended_retention_days", 90)
	ageDays = daysSince(r.FirstSeenAt, ctx.Now())
	if ageDays <= recommendedDays {
		return ageDays, core.Money{}, core.Money{}, false
	}
	storagePrice, found := ctx.Pricing.ServicePrice(r.Region, "cloudwatch", "log_storage_gb")
	if !found {
		return ageDays, core.Money{}, core.Money{}, false
	}
	currentCost = storagePrice.Scale(sizeGiB)
	steadyStateGiB := sizeGiB * (recommendedDays / ageDays)
	saving = storagePrice.Scale(sizeGiB - steadyStateGiB)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDCWLogRetention, "min_monthly_saving", 0.5)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionSetLogRetention) {
		return ageDays, currentCost, core.Money{}, false
	}
	return ageDays, currentCost, saving, true
}

func (ruleCWLogRetention) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	ageDays, currentCost, saving, ok := decideCWLogRetention(ctx, r)
	if !ok {
		return nil, nil
	}
	recommendedDays := ctx.Thresholds.Int(ctx.TenantID, RuleIDCWLogRetention, "recommended_retention_days", 90)
	evidence := []optimize.Evidence{
		ConfigEvidence("retention policy", "never expire"),
		ConfigEvidence("observed group age", fmt.Sprintf("%.0f days", ageDays)),
	}
	summary := fmt.Sprintf("%s has never-expire retention at %.0f days old", r.DisplayName(), ageDays)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleCWLogRetention{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: fmt.Sprintf("Saving assumes steady-state storage at the recommended %d-day ceiling; an estimate, not a measurement.", recommendedDays),
		Evidence: evidence, CurrentCost: currentCost, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleCWLogRetention) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	recommendedDays := ctx.Thresholds.Int(ctx.TenantID, RuleIDCWLogRetention, "recommended_retention_days", 90)
	return RuleAction{
		Type:          optimize.ActionSetLogRetention,
		Parameters:    map[string]any{"log_group_id": r.NativeID, "retention_days": recommendedDays},
		ProposedState: optimize.StateSnapshot{Attributes: map[string]string{"retention_days": fmt.Sprintf("%d", recommendedDays)}},
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Set %d-day retention on %s", recommendedDays, r.DisplayName()),
		Rationale:     f.Detail,
	}
}
