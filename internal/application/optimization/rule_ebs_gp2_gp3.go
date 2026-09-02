package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEBSGp2Gp3 migrates a gp2 volume to gp3, preserving the exact
// baseline performance guarantee AWS documents for the migration:
//
//   - gp2 delivers 3 IOPS per provisioned GiB (its only performance lever),
//     up to 16,000 IOPS, and no separately configurable throughput.
//   - gp3 delivers a flat 3,000 IOPS and 125 MiB/s baseline at no additional
//     charge on every volume regardless of size, with extra IOPS/throughput
//     billed only above those baselines.
//
// So the migration target is never simply "gp3, same size": it is gp3 sized
// to at least the gp2 volume's derived IOPS (max(3000, 3 x sizeGiB)) and at
// least its observed throughput, which is what stops this rule from ever
// proposing a performance regression alongside the cost cut.
//
// Traceability: REQ-OPT-004, SPEC-OPT-002 (storage baseline mapping).
const RuleIDEBSGp2Gp3 optimize.RuleID = "ebs-gp2-to-gp3"

// gp3BaselineIOPS and gp3BaselineThroughputMiBps are what AWS includes free
// on every gp3 volume; billed IOPS/throughput apply only above these.
const (
	gp3BaselineIOPS            = 3000.0
	gp3BaselineThroughputMiBps = 125.0
	gp2IOPSPerGiB              = 3.0
	gp2MaxIOPS                 = 16000.0
)

type ruleEBSGp2Gp3 struct{}

func NewEBSGp2Gp3Rule() FullRule { return ruleEBSGp2Gp3{} }

func (ruleEBSGp2Gp3) ID() optimize.RuleID { return RuleIDEBSGp2Gp3 }

func (ruleEBSGp2Gp3) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEBSGp2Gp3, Name: "gp2 volume — migrate to gp3", Category: optimize.CategoryStorage,
		Action: optimize.ActionModifyVolumeType,
		Description: "gp3 is priced independently of size for its baseline 3,000 IOPS / 125 " +
			"MiB/s, undercutting gp2 with no performance loss when the baseline mapping is honoured.",
		Kinds: []cloud.Kind{cloud.KindEBSVolume}, Enabled: true,
	}
}

func (ruleEBSGp2Gp3) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEBSVolume && r.InstanceType == "gp2" && r.State.Active()
}

// GP2ToGP3Target computes the gp3 IOPS and throughput that preserve at least
// gp2's performance for a volume of the given size and observed throughput.
// Exported so tests exercise the mapping directly, independent of pricing.
func GP2ToGP3Target(sizeGiB, observedThroughputMiBps float64) (targetIOPS int64, targetThroughputMiBps float64) {
	derivedGp2IOPS := minFloat(sizeGiB*gp2IOPSPerGiB, gp2MaxIOPS)
	targetIOPS = int64(maxFloat(gp3BaselineIOPS, derivedGp2IOPS))
	targetThroughputMiBps = maxFloat(gp3BaselineThroughputMiBps, observedThroughputMiBps)
	return
}

func decideEBSGp2Gp3(ctx EvalContext, r cloud.Resource) (targetIOPS int64, targetThroughput float64, gp2Cost, gp3Cost, saving core.Money, ok bool) {
	targetIOPS, targetThroughput = GP2ToGP3Target(r.Capacity.StorageGiB, r.Capacity.ThroughputMiBps)

	gp2StoragePrice, ok1 := ctx.Pricing.StoragePrice(r.Region, "gp2")
	gp3StoragePrice, ok2 := ctx.Pricing.StoragePrice(r.Region, "gp3")
	gp3IOPSPrice, ok3 := ctx.Pricing.IOPSPrice(r.Region, "gp3")
	gp3ThroughputPrice, ok4 := ctx.Pricing.ThroughputPrice(r.Region, "gp3")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return
	}

	gp2Cost = gp2StoragePrice.Scale(r.Capacity.StorageGiB)
	gp3Cost = gp3StoragePrice.Scale(r.Capacity.StorageGiB)
	if extraIOPS := float64(targetIOPS) - gp3BaselineIOPS; extraIOPS > 0 {
		gp3Cost = gp3Cost.MustAdd(gp3IOPSPrice.Scale(extraIOPS))
	}
	if extraThroughput := targetThroughput - gp3BaselineThroughputMiBps; extraThroughput > 0 {
		gp3Cost = gp3Cost.MustAdd(gp3ThroughputPrice.Scale(extraThroughput))
	}
	if !gp3Cost.LessThan(gp2Cost) {
		return targetIOPS, targetThroughput, gp2Cost, gp3Cost, core.Money{}, false
	}
	saving = gp2Cost.MustSub(gp3Cost)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSGp2Gp3, "min_monthly_saving", 1)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionModifyVolumeType) {
		return targetIOPS, targetThroughput, gp2Cost, gp3Cost, core.Money{}, false
	}
	return targetIOPS, targetThroughput, gp2Cost, gp3Cost, saving, true
}

func (ruleEBSGp2Gp3) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	targetIOPS, targetThroughput, gp2Cost, gp3Cost, saving, ok := decideEBSGp2Gp3(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("current type", fmt.Sprintf("gp2, %.0f GiB", r.Capacity.StorageGiB)),
		CostEvidence("gp2 vs gp3 monthly cost", fmt.Sprintf("%s vs %s at %d IOPS / %.0f MiB/s (baseline-preserving)",
			gp2Cost.Format(), gp3Cost.Format(), targetIOPS, targetThroughput), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s (gp2, %.0f GiB) can migrate to gp3 with no performance loss", r.DisplayName(), r.Capacity.StorageGiB)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEBSGp2Gp3{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Target IOPS and throughput are computed to meet or exceed gp2's derived baseline performance.",
		Evidence: evidence, CurrentCost: gp2Cost, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEBSGp2Gp3) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	targetIOPS, targetThroughput, _, gp3Cost, _, ok := decideEBSGp2Gp3(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type: optimize.ActionModifyVolumeType,
		Parameters: map[string]any{
			"volume_id": r.NativeID, "volume_type": "gp3", "iops": targetIOPS, "throughput_mibps": targetThroughput,
		},
		ProposedState: optimize.StateSnapshot{VolumeType: "gp3", IOPS: targetIOPS, MonthlyCost: gp3Cost},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Migrate %s from gp2 to gp3", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
