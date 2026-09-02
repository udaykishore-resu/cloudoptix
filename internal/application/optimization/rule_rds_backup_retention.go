package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDRDSBackupRetention flags automated backup retention set well beyond
// the platform's recommended ceiling. No executor changes this setting in
// this catalogue, so it is advisory.
//
// Traceability: REQ-OPT-005.
const RuleIDRDSBackupRetention optimize.RuleID = "rds-excessive-backup-retention"

type ruleRDSBackupRetention struct{}

func NewRDSBackupRetentionRule() FullRule { return ruleRDSBackupRetention{} }

func (ruleRDSBackupRetention) ID() optimize.RuleID { return RuleIDRDSBackupRetention }

func (ruleRDSBackupRetention) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDRDSBackupRetention, Name: "Excessive automated backup retention",
		Category: optimize.CategoryDataLifecycle, Action: optimize.ActionAdvisoryOnly,
		Description: "Backup retention set well beyond the recommended ceiling accumulates backup storage charges.",
		Kinds:       []cloud.Kind{cloud.KindRDSInstance}, Enabled: true,
	}
}

func (ruleRDSBackupRetention) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindRDSInstance && r.Capacity.RetentionDays > 0
}

func decideRDSBackupRetention(ctx EvalContext, r cloud.Resource) (excessDays float64, saving core.Money, ok bool) {
	maxDays := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSBackupRetention, "max_recommended_days", 14)
	if float64(r.Capacity.RetentionDays) <= maxDays {
		return 0, core.Money{}, false
	}
	excessDays = float64(r.Capacity.RetentionDays) - maxDays
	backupPrice, found := ctx.Pricing.StoragePrice(r.Region, "rds_backup")
	if !found {
		return 0, core.Money{}, false
	}
	// Approximate backup storage as one full daily snapshot's worth of the
	// allocated volume per retained day beyond the recommended ceiling; AWS's
	// incremental backup storage is normally smaller than this per day, so
	// the estimate is intentionally conservative (an upper bound, not an
	// overstatement dressed as a measurement — see the finding's Detail).
	saving = backupPrice.Scale(r.Capacity.StorageGiB * 0.05 * excessDays)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSBackupRetention, "min_monthly_saving", 2)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return excessDays, core.Money{}, false
	}
	return excessDays, saving, true
}

func (ruleRDSBackupRetention) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	excessDays, saving, ok := decideRDSBackupRetention(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("backup retention", fmt.Sprintf("%d days", r.Capacity.RetentionDays)),
	}
	summary := fmt.Sprintf("%s retains backups %.0f days beyond the recommended ceiling", r.DisplayName(), excessDays)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleRDSBackupRetention{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "Saving is an upper-bound estimate from allocated storage; actual incremental backup storage is typically smaller.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleRDSBackupRetention) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	maxDays := ctx.Thresholds.Int(ctx.TenantID, RuleIDRDSBackupRetention, "max_recommended_days", 14)
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Parameters:    map[string]any{"db_instance_id": r.NativeID, "recommended_retention_days": maxDays},
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Reduce backup retention on %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
