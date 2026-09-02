package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDS3WrongStorageClass flags a bucket already assigned to a storage
// class (via the discovered storage_class attribute) that does not match its
// observed access pattern: cold data still sitting in Standard, or churny
// data sitting in an infrequent-access class where early-deletion and
// retrieval charges erase the storage saving. This is distinct from
// s3-no-lifecycle-policy, which targets buckets with no policy at all.
//
// Traceability: REQ-OPT-005.
const RuleIDS3WrongStorageClass optimize.RuleID = "s3-wrong-storage-class"

type ruleS3WrongStorageClass struct{}

func NewS3WrongStorageClassRule() FullRule { return ruleS3WrongStorageClass{} }

func (ruleS3WrongStorageClass) ID() optimize.RuleID { return RuleIDS3WrongStorageClass }

func (ruleS3WrongStorageClass) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDS3WrongStorageClass, Name: "S3 objects in the wrong storage class for their access pattern",
		Category: optimize.CategoryStorage, Action: optimize.ActionApplyS3Lifecycle,
		Description: "The bucket's estimated access pattern does not match its assigned storage class.",
		Kinds:       []cloud.Kind{cloud.KindS3Bucket}, Enabled: true,
	}
}

func (ruleS3WrongStorageClass) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindS3Bucket && r.Attr("storage_class", "") != ""
}

// s3RequestsPerObjectPerMonth estimates how often an average object in the
// bucket is accessed per month from the observed request rate, which is the
// only access-pattern signal this rule has without object-level Storage Lens
// data.
func s3RequestsPerObjectPerMonth(m ports.ResourceMetrics, objectCount int64) float64 {
	if m.Requests == nil || objectCount <= 0 {
		return -1 // unknown, not zero — callers must not treat this as "cold"
	}
	secondsPerMonth := core.HoursPerMonth * 3600
	return (m.Requests.Mean * secondsPerMonth) / float64(objectCount)
}

func decideS3WrongStorageClass(ctx EvalContext, r cloud.Resource) (from, to string, saving core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	if !found {
		return
	}
	freq := s3RequestsPerObjectPerMonth(m, r.Capacity.ObjectCount)
	if freq < 0 {
		return // no access-rate signal; never guess
	}
	class := r.Attr("storage_class", "standard")
	standardPrice, ok1 := ctx.Pricing.StoragePrice(r.Region, "standard")
	iaPrice, ok2 := ctx.Pricing.StoragePrice(r.Region, "standard_ia")
	if !ok1 || !ok2 {
		return
	}
	// Only the cold-data-in-Standard direction is priced here: moving churny
	// data out of an infrequent-access class back to Standard is real FinOps
	// advice, but its saving depends on the IA retrieval and early-deletion
	// fees this catalog does not carry a unit price for, and this rule would
	// rather report nothing than a saving it cannot defend with a real price.
	if class != "standard" || freq >= 0.1 {
		return
	}
	// Fewer than one access per object every ten months: cold data sitting
	// at Standard rates.
	saving = standardPrice.MustSub(iaPrice).Scale(r.Capacity.StorageGiB)
	from, to = "standard", "standard_ia"
	if saving.IsZero() || saving.IsNegative() {
		return "", "", core.Money{}, false
	}
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDS3WrongStorageClass, "min_monthly_saving", 2)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionApplyS3Lifecycle) {
		return "", "", core.Money{}, false
	}
	return from, to, saving, true
}

func (ruleS3WrongStorageClass) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	from, to, saving, ok := decideS3WrongStorageClass(ctx, r)
	if !ok {
		return nil, nil
	}
	m, _ := MetricsFor(ctx, r.ID)
	evidence := []optimize.Evidence{
		ConfigEvidence("current storage class", from),
		MetricEvidence("request rate", m.Requests, m.Window, "cloudwatch"),
	}
	summary := fmt.Sprintf("%s's access pattern does not match its %s storage class", r.DisplayName(), from)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleS3WrongStorageClass{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: fmt.Sprintf("Estimated access frequency supports moving to %s.", to),
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleS3WrongStorageClass) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, to, saving, ok := decideS3WrongStorageClass(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	// Thirty days is not a delay this rule wants — the data is already cold —
	// it is S3's own minimum age for a transition into Standard-IA. A rule
	// asking for a shorter one is rejected by the API, so the recommendation
	// states the soonest date the change can actually take effect.
	params, ok2 := s3TransitionParameters(RuleIDS3WrongStorageClass, r.NativeID, to, 30)
	if !ok2 {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionApplyS3Lifecycle,
		Parameters:    params,
		ProposedState: optimize.StateSnapshot{Attributes: map[string]string{"storage_class": to}, MonthlyCost: CostFor(ctx, r).MustSub(saving)},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Move %s to %s", r.DisplayName(), to),
		Rationale:     f.Detail,
	}
}
