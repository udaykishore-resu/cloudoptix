package optimization

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDLambdaUnusedProvisionedConcurrency flags provisioned concurrency
// billed whether or not it is used: a function whose observed
// concurrent-execution P95 sits far below its provisioned level over a full
// window is paying for standby capacity nobody calls into.
//
// Traceability: REQ-OPT-008.
const RuleIDLambdaUnusedProvisionedConcurrency optimize.RuleID = "lambda-unused-provisioned-concurrency"

type ruleLambdaUnusedProvisionedConcurrency struct{}

func NewLambdaUnusedProvisionedConcurrencyRule() FullRule {
	return ruleLambdaUnusedProvisionedConcurrency{}
}

func (ruleLambdaUnusedProvisionedConcurrency) ID() optimize.RuleID {
	return RuleIDLambdaUnusedProvisionedConcurrency
}

func (ruleLambdaUnusedProvisionedConcurrency) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDLambdaUnusedProvisionedConcurrency, Name: "Unused Lambda provisioned concurrency",
		Category: optimize.CategoryWaste, Action: optimize.ActionRemoveProvisionedConcurrency,
		Description: "Provisioned concurrency bills whether or not it is used; a function whose concurrent " +
			"executions never approach it is over-provisioned.",
		Kinds: []cloud.Kind{cloud.KindLambdaFunction}, Enabled: true,
	}
}

func (ruleLambdaUnusedProvisionedConcurrency) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindLambdaFunction && r.State.Active() && r.Capacity.Concurrency > 0
}

func decideLambdaUnusedProvisionedConcurrency(ctx EvalContext, r cloud.Resource) (utilPct float64, cost core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	minWindow := ctx.Thresholds.Duration(ctx.TenantID, RuleIDLambdaUnusedProvisionedConcurrency, "min_window_hours", time.Hour, 168*time.Hour)
	if !found || m.Concurrency == nil || !HasSufficientData(m, 0.5, minWindow) {
		return
	}
	utilPct = (m.Concurrency.P95 / float64(r.Capacity.Concurrency)) * 100
	maxUtil := ctx.Thresholds.Float(ctx.TenantID, RuleIDLambdaUnusedProvisionedConcurrency, "max_utilization_pct", 15)
	if utilPct > maxUtil {
		return utilPct, core.Money{}, false
	}
	pcPrice, found := ctx.Pricing.ServicePrice(r.Region, "lambda", "provisioned_concurrency_gb_second")
	if !found {
		return utilPct, core.Money{}, false
	}
	memoryGB := float64(r.Capacity.MemoryMB) / 1024.0
	secondsPerMonth := core.HoursPerMonth * 3600
	cost = pcPrice.Scale(memoryGB * float64(r.Capacity.Concurrency) * secondsPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDLambdaUnusedProvisionedConcurrency, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionRemoveProvisionedConcurrency) {
		return utilPct, core.Money{}, false
	}
	return utilPct, cost, true
}

func (ruleLambdaUnusedProvisionedConcurrency) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	utilPct, cost, ok := decideLambdaUnusedProvisionedConcurrency(ctx, r)
	if !ok {
		return nil, nil
	}
	m, _ := MetricsFor(ctx, r.ID)
	evidence := []optimize.Evidence{
		MetricEvidence("concurrent executions", m.Concurrency, m.Window, "cloudwatch"),
		ConfigEvidence("provisioned concurrency", fmt.Sprintf("%d", r.Capacity.Concurrency)),
	}
	summary := fmt.Sprintf("%s uses only %.0f%% of its provisioned concurrency", r.DisplayName(), utilPct)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleLambdaUnusedProvisionedConcurrency{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "Provisioned concurrency is billed continuously regardless of use.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleLambdaUnusedProvisionedConcurrency) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionRemoveProvisionedConcurrency,
		Parameters:    map[string]any{"function_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{Count: 0},
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Remove unused provisioned concurrency on %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
