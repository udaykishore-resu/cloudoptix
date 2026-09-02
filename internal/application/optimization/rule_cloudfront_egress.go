package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDCloudFrontEgress compares the blended cost of serving a workload's
// internet egress through CloudFront against direct egress at the observed
// monthly GB-out volume, and advises whichever is cheaper. The comparison
// covers the GB-out data-transfer charge only — CloudFront's per-request
// charge is not included, because request counts are not reliably
// attributable to a single S3 bucket or distribution in this model — so the
// result is a lower bound on CloudFront's true cost and the rule only fires
// when the GB-out volume alone already crosses a material threshold.
// Architectural change, so advisory only.
//
// Traceability: REQ-OPT-007.
const RuleIDCloudFrontEgress optimize.RuleID = "cloudfront-vs-direct-egress"

type ruleCloudFrontEgress struct{}

func NewCloudFrontEgressRule() FullRule { return ruleCloudFrontEgress{} }

func (ruleCloudFrontEgress) ID() optimize.RuleID { return RuleIDCloudFrontEgress }

func (ruleCloudFrontEgress) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDCloudFrontEgress, Name: "CloudFront-vs-direct-egress break-even", Category: optimize.CategoryArchitecture,
		Action: optimize.ActionAdvisoryOnly,
		Description: "Compares CloudFront's data-out rate against direct internet egress at the observed volume and " +
			"advises whichever is cheaper.",
		Kinds: []cloud.Kind{cloud.KindS3Bucket, cloud.KindCloudFront}, Enabled: true,
	}
}

func (ruleCloudFrontEgress) Applies(r cloud.Resource) bool {
	return (r.Kind == cloud.KindS3Bucket || r.Kind == cloud.KindCloudFront) && r.State.Active()
}

// decideCloudFrontEgress returns the cheaper of the two serving paths.
// cheaperIsCloudFront is only meaningful when ok is true.
func decideCloudFrontEgress(ctx EvalContext, r cloud.Resource) (gbOut float64, directCost, cloudfrontCost core.Money, cheaperIsCloudFront bool, ok bool) {
	gbOut = parseFloatAttr(r.Attr("gb_out_month", ""), -1)
	if gbOut < 0 {
		return
	}
	minGB := ctx.Thresholds.Float(ctx.TenantID, RuleIDCloudFrontEgress, "min_gb_out_month", 500)
	if gbOut < minGB {
		return gbOut, core.Money{}, core.Money{}, false, false
	}
	directPrice, ok1 := ctx.Pricing.DataTransferPrice(r.Region, "internet_out")
	cfPrice, ok2 := ctx.Pricing.ServicePrice(r.Region, "cloudfront", "gb_out")
	if !ok1 || !ok2 {
		return gbOut, core.Money{}, core.Money{}, false, false
	}
	directCost = directPrice.Scale(gbOut)
	cloudfrontCost = cfPrice.Scale(gbOut)
	var saving core.Money
	if r.Kind == cloud.KindS3Bucket {
		// Currently serving direct; is CloudFront cheaper?
		if !cloudfrontCost.LessThan(directCost) {
			return gbOut, directCost, cloudfrontCost, false, false
		}
		saving = directCost.MustSub(cloudfrontCost)
		cheaperIsCloudFront = true
	} else {
		// Currently paying CloudFront; is direct cheaper?
		if !directCost.LessThan(cloudfrontCost) {
			return gbOut, directCost, cloudfrontCost, true, false
		}
		saving = cloudfrontCost.MustSub(directCost)
		cheaperIsCloudFront = false
	}
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDCloudFrontEgress, "min_monthly_saving", 20)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return gbOut, directCost, cloudfrontCost, cheaperIsCloudFront, false
	}
	return gbOut, directCost, cloudfrontCost, cheaperIsCloudFront, true
}

func (ruleCloudFrontEgress) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	gbOut, directCost, cfCost, _, ok := decideCloudFrontEgress(ctx, r)
	if !ok {
		return nil, nil
	}
	current, saving := directCost, directCost.MustSub(cfCost)
	verb := "adding CloudFront in front of"
	if r.Kind == cloud.KindCloudFront {
		current, saving, verb = cfCost, cfCost.MustSub(directCost), "serving directly instead of through"
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("observed egress volume", fmt.Sprintf("%.0f GB/month", gbOut)),
		CostEvidence("direct vs CloudFront data-transfer cost", fmt.Sprintf("%s vs %s", directCost.Format(), cfCost.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s: %s this resource is cheaper at the observed volume", r.DisplayName(), verb)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleCloudFrontEgress{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "Comparison covers GB-out data transfer only; CloudFront's per-request charge is not included.",
		Evidence: evidence, CurrentCost: current, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleCloudFrontEgress) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilitySlow,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Evaluate egress path for %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
