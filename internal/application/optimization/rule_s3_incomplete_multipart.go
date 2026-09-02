package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDS3IncompleteMultipart flags abandoned multipart upload parts, which
// bill for storage with no object ever becoming visible. Aborting them is a
// zero-risk, zero-downtime cleanup with no effect on any completed object.
//
// Traceability: REQ-OPT-005.
const RuleIDS3IncompleteMultipart optimize.RuleID = "s3-incomplete-multipart-uploads"

type ruleS3IncompleteMultipart struct{}

func NewS3IncompleteMultipartRule() FullRule { return ruleS3IncompleteMultipart{} }

func (ruleS3IncompleteMultipart) ID() optimize.RuleID { return RuleIDS3IncompleteMultipart }

func (ruleS3IncompleteMultipart) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDS3IncompleteMultipart, Name: "Incomplete multipart uploads",
		Category: optimize.CategoryWaste, Action: optimize.ActionAbortMultipartUploads,
		Description: "Abandoned multipart upload parts bill for storage with no object ever becoming visible.",
		Kinds:       []cloud.Kind{cloud.KindS3Bucket}, Enabled: true,
	}
}

func (ruleS3IncompleteMultipart) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindS3Bucket && parseFloatAttr(r.Attr("incomplete_multipart_count", ""), 0) > 0
}

func decideS3IncompleteMultipart(ctx EvalContext, r cloud.Resource) (count int, gib float64, saving core.Money, ok bool) {
	count = parseIntAttr(r.Attr("incomplete_multipart_count", ""), 0)
	gib = parseFloatAttr(r.Attr("incomplete_multipart_gib", ""), 0)
	minGiB := ctx.Thresholds.Float(ctx.TenantID, RuleIDS3IncompleteMultipart, "min_gib", 1)
	if count == 0 || gib < minGiB {
		return 0, 0, core.Money{}, false
	}
	price, found := ctx.Pricing.StoragePrice(r.Region, "standard")
	if !found {
		return 0, 0, core.Money{}, false
	}
	saving = price.Scale(gib)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDS3IncompleteMultipart, "min_monthly_saving", 0.25)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAbortMultipartUploads) {
		return 0, 0, core.Money{}, false
	}
	return count, gib, saving, true
}

func (ruleS3IncompleteMultipart) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	count, gib, saving, ok := decideS3IncompleteMultipart(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("incomplete multipart uploads", fmt.Sprintf("%d uploads, %.1f GiB", count, gib)),
	}
	summary := fmt.Sprintf("%s has %d abandoned multipart uploads (%.1f GiB)", r.DisplayName(), count, gib)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleS3IncompleteMultipart{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "Aborting incomplete multipart uploads has no effect on any completed object.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleS3IncompleteMultipart) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAbortMultipartUploads,
		Parameters:    map[string]any{"bucket": r.NativeID, "older_than_days": 7},
		ProposedState: optimize.StateSnapshot{},
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Abort incomplete multipart uploads on %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
