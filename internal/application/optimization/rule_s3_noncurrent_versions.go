package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDS3NoncurrentVersions flags a versioned bucket accumulating a material
// volume of non-current object versions with no lifecycle policy to expire
// them.
//
// Traceability: REQ-OPT-005.
const RuleIDS3NoncurrentVersions optimize.RuleID = "s3-noncurrent-versions"

type ruleS3NoncurrentVersions struct{}

func NewS3NoncurrentVersionsRule() FullRule { return ruleS3NoncurrentVersions{} }

func (ruleS3NoncurrentVersions) ID() optimize.RuleID { return RuleIDS3NoncurrentVersions }

func (ruleS3NoncurrentVersions) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDS3NoncurrentVersions, Name: "Non-current object versions accumulating with no expiry",
		Category: optimize.CategoryDataLifecycle, Action: optimize.ActionApplyS3Lifecycle,
		Description: "A versioned bucket with no expiry policy for non-current versions is " +
			"paying Standard rates for every overwritten or deleted object's history.",
		Kinds: []cloud.Kind{cloud.KindS3Bucket}, Enabled: true,
	}
}

func (ruleS3NoncurrentVersions) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindS3Bucket && r.AttrBool("versioning_enabled", false) && !r.AttrBool("has_lifecycle_policy", false)
}

func decideS3NoncurrentVersions(ctx EvalContext, r cloud.Resource) (gib float64, saving core.Money, ok bool) {
	gib = parseFloatAttr(r.Attr("non_current_version_gib", ""), 0)
	minGiB := ctx.Thresholds.Float(ctx.TenantID, RuleIDS3NoncurrentVersions, "min_noncurrent_gib", 10)
	if gib < minGiB {
		return 0, core.Money{}, false
	}
	price, found := ctx.Pricing.StoragePrice(r.Region, "standard")
	if !found {
		return 0, core.Money{}, false
	}
	saving = price.Scale(gib)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDS3NoncurrentVersions, "min_monthly_saving", 1)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionApplyS3Lifecycle) {
		return 0, core.Money{}, false
	}
	return gib, saving, true
}

func (ruleS3NoncurrentVersions) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	gib, saving, ok := decideS3NoncurrentVersions(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("non-current version storage", fmt.Sprintf("%.1f GiB", gib)),
		ConfigEvidence("versioning", "enabled"),
	}
	summary := fmt.Sprintf("%s has %.1f GiB of non-current versions with no expiry policy", r.DisplayName(), gib)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleS3NoncurrentVersions{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "A non-current-version expiration rule reclaims this storage without disabling versioning.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleS3NoncurrentVersions) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type: optimize.ActionApplyS3Lifecycle,
		// Its own rule_id, distinct from the tiering rules': a
		// non-current-version expiry and a storage-class transition are two
		// lifecycle rules on one bucket, not two competing versions of one.
		// Sharing the default id is what previously made whichever
		// recommendation was applied second silently delete the first.
		Parameters: map[string]any{
			"bucket":                     r.NativeID,
			"rule_id":                    s3LifecycleRuleID(RuleIDS3NoncurrentVersions),
			"noncurrent_expiration_days": 30,
		},
		ProposedState: optimize.StateSnapshot{Attributes: map[string]string{"noncurrent_version_expiration_days": "30"}},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityTrivial,
		// The non-current versions are a byte pool disjoint from the bucket's
		// current objects, so this composes with a storage-class change on
		// the same bucket rather than competing with it — hence its own
		// domain rather than the action's default.
		ConflictDomain: optimize.ConflictDomainNoncurrentVersions,
		Title:          fmt.Sprintf("Expire non-current versions on %s", r.DisplayName()),
		Rationale:      f.Detail,
	}
}
