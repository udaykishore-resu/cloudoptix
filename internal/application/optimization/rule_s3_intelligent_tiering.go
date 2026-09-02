package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDS3IntelligentTiering evaluates S3 Intelligent-Tiering candidacy
// against its exact break-even point.
//
// Intelligent-Tiering is not free: AWS bills a flat monitoring-and-automation
// charge per object per month (this catalog's s3/monitoring_per_million_objects),
// independent of object size, in exchange for automatically moving unaccessed
// objects to a cheaper tier. That means the feature's value is a function of
// average object size, not total bucket size: a bucket of a few large objects
// earns far more storage-class saving per monitoring dollar than a bucket of
// millions of small objects, whose monitoring charge can exceed anything a
// tier transition could ever save. This rule computes the exact break-even
// average object size — the size at which the monitoring charge equals the
// achievable storage saving — from the catalog's own prices, and refuses to
// recommend Intelligent-Tiering for any bucket below it.
//
// Traceability: REQ-OPT-005, SPEC-OPT-002 (break-even economics).
const RuleIDS3IntelligentTiering optimize.RuleID = "s3-intelligent-tiering-candidacy"

type ruleS3IntelligentTiering struct{}

func NewS3IntelligentTieringRule() FullRule { return ruleS3IntelligentTiering{} }

func (ruleS3IntelligentTiering) ID() optimize.RuleID { return RuleIDS3IntelligentTiering }

func (ruleS3IntelligentTiering) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDS3IntelligentTiering, Name: "S3 Intelligent-Tiering candidacy (with monitoring-charge break-even)",
		Category: optimize.CategoryStorage, Action: optimize.ActionApplyS3Lifecycle,
		Description: "Recommends Intelligent-Tiering only above the exact break-even average " +
			"object size where the per-object monitoring charge is smaller than the achievable " +
			"storage-class saving; a small-object bucket never qualifies.",
		Kinds: []cloud.Kind{cloud.KindS3Bucket}, Enabled: true,
	}
}

func (ruleS3IntelligentTiering) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindS3Bucket && r.Capacity.ObjectCount > 0 && r.Capacity.StorageGiB > 0 &&
		r.Attr("storage_class", "standard") == "standard"
}

// S3IntelligentTieringBreakEvenGiB computes the average object size, in GiB,
// above which Intelligent-Tiering's monitoring charge is smaller than the
// storage-class saving it could earn. Exported so tests exercise the
// break-even formula directly against known prices.
func S3IntelligentTieringBreakEvenGiB(monitoringPerObject, storageDeltaPerGiB core.Money) (float64, bool) {
	if storageDeltaPerGiB.IsZero() || storageDeltaPerGiB.IsNegative() {
		return 0, false
	}
	return monitoringPerObject.Ratio(storageDeltaPerGiB), true
}

func decideS3IntelligentTiering(ctx EvalContext, r cloud.Resource) (avgObjectGiB, breakEvenGiB float64, saving core.Money, ok bool) {
	monitoringPer1M, found := ctx.Pricing.ServicePrice(r.Region, "s3", "monitoring_per_million_objects")
	if !found {
		return
	}
	monitoringPerObject := monitoringPer1M.Div(1_000_000)

	standardPrice, ok1 := ctx.Pricing.StoragePrice(r.Region, "standard")
	iaPrice, ok2 := ctx.Pricing.StoragePrice(r.Region, "standard_ia")
	if !ok1 || !ok2 {
		return
	}
	delta := standardPrice.MustSub(iaPrice)
	breakEvenGiB, valid := S3IntelligentTieringBreakEvenGiB(monitoringPerObject, delta)
	if !valid {
		return
	}

	avgObjectGiB = r.Capacity.StorageGiB / float64(r.Capacity.ObjectCount)
	if avgObjectGiB <= breakEvenGiB {
		return avgObjectGiB, breakEvenGiB, core.Money{}, false // below break-even: would lose money
	}

	// Conservative estimate of the eligible (cold, tier-transitioned) share,
	// matching the assumption used elsewhere in this package for an
	// unmanaged bucket's access-pattern skew.
	eligibleGiB := r.Capacity.StorageGiB * 0.5
	storageSaving := delta.Scale(eligibleGiB)
	totalMonitoring := monitoringPerObject.Scale(float64(r.Capacity.ObjectCount))
	net := storageSaving.MustSub(totalMonitoring)
	if net.IsZero() || net.IsNegative() {
		return avgObjectGiB, breakEvenGiB, core.Money{}, false
	}
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDS3IntelligentTiering, "min_monthly_saving", 3)
	if !MeetsMinSaving(ctx.Spec, minSaving, net) || ExcludedBySpec(ctx.Spec, r, optimize.ActionApplyS3Lifecycle) {
		return avgObjectGiB, breakEvenGiB, core.Money{}, false
	}
	return avgObjectGiB, breakEvenGiB, net, true
}

func (ruleS3IntelligentTiering) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	avgGiB, breakEvenGiB, saving, ok := decideS3IntelligentTiering(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("average object size", fmt.Sprintf("%.4f GiB (%.0f KiB)", avgGiB, avgGiB*1024*1024)),
		CostEvidence("break-even average object size", fmt.Sprintf("%.4f GiB (%.0f KiB)", breakEvenGiB, breakEvenGiB*1024*1024), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s's average object size clears the Intelligent-Tiering break-even", r.DisplayName())
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleS3IntelligentTiering{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "The monitoring charge for this bucket's object count is smaller than the storage-class saving Intelligent-Tiering can earn on it.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleS3IntelligentTiering) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	// Zero days, not thirty: Intelligent-Tiering does its own access
	// monitoring and moves objects between tiers itself, so the lifecycle
	// rule's job is to put objects into the class immediately and then get
	// out of the way. A delay here would be paying Standard rates for a
	// month before the class that is meant to make that decision even sees
	// the object.
	params, ok := s3TransitionParameters(RuleIDS3IntelligentTiering, r.NativeID, "intelligent_tiering", 0)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionApplyS3Lifecycle,
		Parameters:    params,
		ProposedState: optimize.StateSnapshot{Attributes: map[string]string{"storage_class": "intelligent_tiering"}},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Enable Intelligent-Tiering on %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
