package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDLambdaExcessiveTimeout flags a configured timeout far beyond the
// function's observed P99 duration. Lambda's timeout has no cost effect by
// itself — only actual duration is billed — but a timeout set this far
// beyond normal behaviour widens the blast radius of a hung invocation: it
// can run, and bill, for far longer than the workload ever needs before AWS
// kills it. This finding therefore always carries a zero saving and never
// contributes to a recommendation's priority score on the savings axis; its
// value is entirely in the risk/blast-radius signal.
//
// Traceability: REQ-OPT-008.
const RuleIDLambdaExcessiveTimeout optimize.RuleID = "lambda-excessive-timeout-advisory"

type ruleLambdaExcessiveTimeout struct{}

func NewLambdaExcessiveTimeoutRule() FullRule { return ruleLambdaExcessiveTimeout{} }

func (ruleLambdaExcessiveTimeout) ID() optimize.RuleID { return RuleIDLambdaExcessiveTimeout }

func (ruleLambdaExcessiveTimeout) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDLambdaExcessiveTimeout, Name: "Excessive Lambda timeout (blast radius, not cost)",
		Category: optimize.CategoryArchitecture, Action: optimize.ActionAdvisoryOnly,
		Description: "A timeout far beyond observed P99 duration widens the blast radius of a hung invocation.",
		Kinds:       []cloud.Kind{cloud.KindLambdaFunction}, Enabled: true,
	}
}

func (ruleLambdaExcessiveTimeout) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindLambdaFunction && r.State.Active() && r.Capacity.TimeoutSeconds > 0
}

func decideLambdaExcessiveTimeout(ctx EvalContext, r cloud.Resource) (ratio, p99Ms float64, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	if found && m.LatencyP99 != nil && m.LatencyP99.P99 > 0 {
		p99Ms = m.LatencyP99.P99
	} else {
		p99Ms = parseFloatAttr(r.Attr("avg_duration_ms", ""), -1)
	}
	if p99Ms <= 0 {
		return
	}
	timeoutMs := float64(r.Capacity.TimeoutSeconds) * 1000
	ratio = timeoutMs / p99Ms
	minRatio := ctx.Thresholds.Float(ctx.TenantID, RuleIDLambdaExcessiveTimeout, "timeout_to_duration_ratio_min", 8)
	if ratio < minRatio {
		return ratio, p99Ms, false
	}
	if ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return ratio, p99Ms, false
	}
	return ratio, p99Ms, true
}

func (ruleLambdaExcessiveTimeout) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	ratio, p99Ms, ok := decideLambdaExcessiveTimeout(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("configured timeout", fmt.Sprintf("%ds", r.Capacity.TimeoutSeconds)),
		ConfigEvidence("observed P99 duration (or fallback avg)", fmt.Sprintf("%.0fms", p99Ms)),
	}
	summary := fmt.Sprintf("%s's timeout is %.0fx its observed duration", r.DisplayName(), ratio)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleLambdaExcessiveTimeout{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "No cost effect by itself; a hung invocation can run this long before AWS terminates it.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: core.Money{},
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleLambdaExcessiveTimeout) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Tighten timeout on %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
