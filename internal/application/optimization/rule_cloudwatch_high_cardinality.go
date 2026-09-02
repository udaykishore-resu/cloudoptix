package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDCWHighCardinality flags high-cardinality custom metrics. CloudWatch
// bills per metric-per-dimension-combination, which scales combinatorially
// with cardinality (a per-request or per-container-instance dimension is the
// classic runaway case). CloudOptix's closed Kind enum has no "CloudWatch
// metric" resource of its own — custom metrics are application-emitted, not
// independently discovered — so this rule reads the emitting compute
// resource's declared custom-metric count (an attribute a metrics-aware
// discovery adapter attaches) and prices it directly from the catalog's
// metric-month rate. No executor removes an application's own metric
// emission, so this is advisory.
//
// Traceability: REQ-OPT-010.
const RuleIDCWHighCardinality optimize.RuleID = "cloudwatch-high-cardinality-metrics"

type ruleCWHighCardinality struct{}

func NewCWHighCardinalityRule() FullRule { return ruleCWHighCardinality{} }

func (ruleCWHighCardinality) ID() optimize.RuleID { return RuleIDCWHighCardinality }

func (ruleCWHighCardinality) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDCWHighCardinality, Name: "High-cardinality custom metrics", Category: optimize.CategoryObservability,
		Action: optimize.ActionAdvisoryOnly,
		Description: "Custom metrics bill per metric-per-dimension-combination; a high combinatorial count is a runaway " +
			"cardinality dimension.",
		Kinds:   []cloud.Kind{cloud.KindEC2Instance, cloud.KindECSService, cloud.KindLambdaFunction, cloud.KindEKSNodeGroup},
		Enabled: true,
	}
}

func (ruleCWHighCardinality) Applies(r cloud.Resource) bool {
	return r.State.Active()
}

func decideCWHighCardinality(ctx EvalContext, r cloud.Resource) (count float64, cost core.Money, ok bool) {
	count = parseFloatAttr(r.Attr("custom_metric_count", ""), -1)
	if count < 0 {
		return
	}
	minCount := ctx.Thresholds.Float(ctx.TenantID, RuleIDCWHighCardinality, "min_metric_count", 500)
	if count < minCount {
		return count, core.Money{}, false
	}
	metricPrice, found := ctx.Pricing.ServicePrice(r.Region, "cloudwatch", "metric_month")
	if !found {
		return count, core.Money{}, false
	}
	cost = metricPrice.Scale(count)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDCWHighCardinality, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return count, core.Money{}, false
	}
	return count, cost, true
}

func (ruleCWHighCardinality) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	count, cost, ok := decideCWHighCardinality(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("declared custom-metric count", fmt.Sprintf("%.0f", count)),
	}
	summary := fmt.Sprintf("%s emits %.0f custom metric/dimension combinations", r.DisplayName(), count)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleCWHighCardinality{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "No executor removes an application's own metric emission; advisory only.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleCWHighCardinality) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Reduce custom-metric cardinality on %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
