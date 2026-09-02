package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDS3NoLifecycle flags a bucket with no lifecycle policy and enough
// standing data (a cold-data profile proxy: a large bucket with no policy is
// far more likely to hold data nobody re-tiered than a small one) to be
// paying Standard rates indefinitely for data that could move to a cheaper
// class over time.
//
// Traceability: REQ-OPT-005.
const RuleIDS3NoLifecycle optimize.RuleID = "s3-no-lifecycle-policy"

type ruleS3NoLifecycle struct{}

func NewS3NoLifecycleRule() FullRule { return ruleS3NoLifecycle{} }

func (ruleS3NoLifecycle) ID() optimize.RuleID { return RuleIDS3NoLifecycle }

func (ruleS3NoLifecycle) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDS3NoLifecycle, Name: "S3 bucket with a cold-data profile and no lifecycle policy",
		Category: optimize.CategoryDataLifecycle, Action: optimize.ActionApplyS3Lifecycle,
		Description: "A bucket with no lifecycle policy and a material amount of standing data " +
			"is paying Standard rates indefinitely.",
		Kinds: []cloud.Kind{cloud.KindS3Bucket}, Enabled: true,
	}
}

func (ruleS3NoLifecycle) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindS3Bucket && !r.AttrBool("has_lifecycle_policy", false)
}

func decideS3NoLifecycle(ctx EvalContext, r cloud.Resource) (targetSaving core.Money, ok bool) {
	minGiB := ctx.Thresholds.Float(ctx.TenantID, RuleIDS3NoLifecycle, "min_size_gib", 50)
	if r.Capacity.StorageGiB < minGiB {
		return core.Money{}, false
	}
	standardPrice, ok1 := ctx.Pricing.StoragePrice(r.Region, "standard")
	iaPrice, ok2 := ctx.Pricing.StoragePrice(r.Region, "standard_ia")
	if !ok1 || !ok2 {
		return core.Money{}, false
	}
	// A conservative estimate: transitioning to Standard-IA after 30-60 days
	// typically moves the majority of an unmanaged bucket's data, since most
	// object access concentrates in the first weeks after write.
	movable := r.Capacity.StorageGiB * 0.6
	saving := standardPrice.MustSub(iaPrice).Scale(movable)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDS3NoLifecycle, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionApplyS3Lifecycle) {
		return core.Money{}, false
	}
	return saving, true
}

func (ruleS3NoLifecycle) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	saving, ok := decideS3NoLifecycle(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("lifecycle policy", "none"),
		ConfigEvidence("bucket size", fmt.Sprintf("%.0f GiB, %d objects", r.Capacity.StorageGiB, r.Capacity.ObjectCount)),
	}
	summary := fmt.Sprintf("%s (%.0f GiB) has no lifecycle policy", r.DisplayName(), r.Capacity.StorageGiB)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleS3NoLifecycle{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "A tiering lifecycle policy (e.g. Standard -> Standard-IA at 30-60 days) would move the majority of this bucket's data to a cheaper class.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

// s3NoLifecycleTransitionDays is the age at which this rule's proposed
// policy moves an object to Standard-IA. It matches the 30-60 day window the
// saving estimate above is derived from; changing one without the other
// would make the recommendation's arithmetic and its action disagree.
const s3NoLifecycleTransitionDays = 30

func (ruleS3NoLifecycle) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	params, ok := s3TransitionParameters(RuleIDS3NoLifecycle, r.NativeID, "standard_ia", s3NoLifecycleTransitionDays)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionApplyS3Lifecycle,
		Parameters:    params,
		ProposedState: optimize.StateSnapshot{Attributes: map[string]string{"lifecycle_policy": "standard_ia_30d"}},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Add a lifecycle policy to %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
