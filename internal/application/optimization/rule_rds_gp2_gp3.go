package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDRDSGp2Gp3 mirrors ebs-gp2-to-gp3 for RDS-attached storage, using the
// catalog's rds_gp2/rds_gp3 bridged pricing (bridgeRDSStorage in the pricing
// adapter). RDS gp3, like EBS gp3, includes a baseline 3,000 IOPS at no
// extra charge, so the migration target preserves at least that baseline.
//
// Traceability: REQ-OPT-005.
const RuleIDRDSGp2Gp3 optimize.RuleID = "rds-gp2-to-gp3"

type ruleRDSGp2Gp3 struct{}

func NewRDSGp2Gp3Rule() FullRule { return ruleRDSGp2Gp3{} }

func (ruleRDSGp2Gp3) ID() optimize.RuleID { return RuleIDRDSGp2Gp3 }

func (ruleRDSGp2Gp3) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDRDSGp2Gp3, Name: "RDS gp2 storage — migrate to gp3", Category: optimize.CategoryStorage,
		Action:      optimize.ActionModifyRDSStorage,
		Description: "Mirrors the EBS gp2->gp3 saving for RDS-attached storage.",
		Kinds:       []cloud.Kind{cloud.KindRDSInstance}, Enabled: true,
	}
}

func (ruleRDSGp2Gp3) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindRDSInstance && r.State.Active() && r.Attr("storage_type", "") == "gp2"
}

func decideRDSGp2Gp3(ctx EvalContext, r cloud.Resource) (gp2Cost, gp3Cost, saving core.Money, ok bool) {
	gp2Price, ok1 := ctx.Pricing.StoragePrice(r.Region, "rds_gp2")
	gp3Price, ok2 := ctx.Pricing.StoragePrice(r.Region, "rds_gp3")
	if !ok1 || !ok2 {
		return
	}
	gp2Cost = gp2Price.Scale(r.Capacity.StorageGiB)
	gp3Cost = gp3Price.Scale(r.Capacity.StorageGiB)
	if !gp3Cost.LessThan(gp2Cost) {
		return core.Money{}, core.Money{}, core.Money{}, false
	}
	saving = gp2Cost.MustSub(gp3Cost)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSGp2Gp3, "min_monthly_saving", 2)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionModifyRDSStorage) {
		return core.Money{}, core.Money{}, core.Money{}, false
	}
	return gp2Cost, gp3Cost, saving, true
}

func (ruleRDSGp2Gp3) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	gp2Cost, gp3Cost, saving, ok := decideRDSGp2Gp3(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("current storage type", fmt.Sprintf("gp2, %.0f GiB", r.Capacity.StorageGiB)),
		CostEvidence("gp2 vs gp3 monthly cost", fmt.Sprintf("%s vs %s", gp2Cost.Format(), gp3Cost.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s's gp2 storage can migrate to gp3", r.DisplayName())
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleRDSGp2Gp3{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "gp3's baseline IOPS/throughput match or exceed gp2's at this size.",
		Evidence: evidence, CurrentCost: gp2Cost, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleRDSGp2Gp3) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, gp3Cost, _, ok := decideRDSGp2Gp3(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type: optimize.ActionModifyRDSStorage,
		// storage_type, not target_storage_type: the modify_rds_storage
		// executor decides what to change by which of allocated_storage_gb /
		// storage_type / iops it finds, and refuses outright when it finds
		// none of them. A key it does not read is not a differently-named
		// instruction, it is no instruction — this recommendation used to
		// clear policy and approval and then fail with "no storage parameters
		// given" at the mutate step.
		Parameters:    map[string]any{"db_instance_id": r.NativeID, "storage_type": "gp3"},
		ProposedState: optimize.StateSnapshot{VolumeType: "gp3", MonthlyCost: gp3Cost},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Migrate %s to gp3", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
