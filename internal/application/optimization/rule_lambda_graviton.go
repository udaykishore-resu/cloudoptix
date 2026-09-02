package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDLambdaGraviton flags an x86_64 Lambda function as an arm64
// (Graviton) migration candidate: arm64 GB-second pricing is materially
// cheaper than x86_64 at equal memory and duration. This rule prices both
// architectures at the function's own observed duration — it does not
// assume Graviton also runs faster (a real but workload-dependent effect
// this model has no telemetry to confirm), so the saving reported is a
// conservative floor, not the full benefit a compatible function would see.
// Runtime compatibility cannot be verified from telemetry alone, so this is
// left to the reviewer.
//
// Traceability: REQ-OPT-008.
const RuleIDLambdaGraviton optimize.RuleID = "lambda-graviton-migration"

type ruleLambdaGraviton struct{}

func NewLambdaGravitonRule() FullRule { return ruleLambdaGraviton{} }

func (ruleLambdaGraviton) ID() optimize.RuleID { return RuleIDLambdaGraviton }

func (ruleLambdaGraviton) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDLambdaGraviton, Name: "Lambda x86 -> arm64 (Graviton) migration", Category: optimize.CategoryRightsizing,
		Action:      optimize.ActionSwitchLambdaArch,
		Description: "arm64 Lambda pricing is materially cheaper per GB-second than x86_64 at equal memory.",
		Kinds:       []cloud.Kind{cloud.KindLambdaFunction}, Enabled: true,
	}
}

func (ruleLambdaGraviton) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindLambdaFunction && r.State.Active() && r.Attr("architecture", "x86_64") != "arm64"
}

func decideLambdaGraviton(ctx EvalContext, r cloud.Resource) (x86Cost, armCost, saving core.Money, ok bool) {
	durationMs := parseFloatAttr(r.Attr("avg_duration_ms", ""), -1)
	invocations := parseFloatAttr(r.Attr("invocations_per_month", ""), -1)
	if durationMs <= 0 || invocations < 0 {
		return
	}
	x86Price, ok1 := ctx.Pricing.ServicePrice(r.Region, "lambda", "gb_second")
	armPrice, ok2 := ctx.Pricing.ServicePrice(r.Region, "lambda", "arm_gb_second")
	requestPrice, ok3 := ctx.Pricing.ServicePrice(r.Region, "lambda", "request")
	if !ok1 || !ok2 || !ok3 {
		return
	}
	x86Cost = LambdaMonthlyCost(r.Capacity.MemoryMB, durationMs, invocations, x86Price, requestPrice)
	armCost = LambdaMonthlyCost(r.Capacity.MemoryMB, durationMs, invocations, armPrice, requestPrice)
	if !armCost.LessThan(x86Cost) {
		return x86Cost, armCost, core.Money{}, false
	}
	saving = x86Cost.MustSub(armCost)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDLambdaGraviton, "min_monthly_saving", 1)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionSwitchLambdaArch) {
		return x86Cost, armCost, core.Money{}, false
	}
	return x86Cost, armCost, saving, true
}

func (ruleLambdaGraviton) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	x86Cost, armCost, saving, ok := decideLambdaGraviton(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("current architecture", r.Attr("architecture", "x86_64")),
		CostEvidence("x86_64 vs arm64 monthly cost at equal duration", fmt.Sprintf("%s vs %s", x86Cost.Format(), armCost.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s can migrate to arm64 (Graviton)", r.DisplayName())
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleLambdaGraviton{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "Saving is priced at the function's current observed duration; Graviton's own speed advantage would add more, unverifiable from telemetry.",
		Evidence: evidence, CurrentCost: x86Cost, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleLambdaGraviton) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, armCost, _, ok := decideLambdaGraviton(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionSwitchLambdaArch,
		Parameters:    map[string]any{"function_id": r.NativeID, "target_architecture": "arm64"},
		ProposedState: optimize.StateSnapshot{MonthlyCost: armCost, Attributes: map[string]string{"architecture": "arm64"}},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityMedium,
		Title:         fmt.Sprintf("Migrate %s to arm64", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
