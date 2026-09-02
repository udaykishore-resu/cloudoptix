package optimization

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDDynamoBillingMode flags a provisioned-capacity DynamoDB table whose
// observed consumption sits far below what it pays to provision.
//
// The reverse direction the YAML pack's description mentions — an on-demand
// table whose steady, predictable throughput would cost less under
// provisioned capacity — is not implemented here: this simulator's telemetry
// (see awssim's discoverDynamoDB) reports a single combined
// "consumed capacity units/s" series with no read/write split, so pricing an
// on-demand table's *provisioned* equivalent would require guessing a
// read/write mix from nothing. Going the other way — provisioned to
// on-demand — only needs the combined consumed total against the *known*
// provisioned read/write split, which is a defensible estimate rather than a
// fabricated one (see decideDynamoBillingMode). CloudOptix would rather cover
// one direction exactly than both directions approximately.
//
// Traceability: REQ-OPT-006.
const RuleIDDynamoBillingMode optimize.RuleID = "dynamodb-billing-mode-mismatch"

type ruleDynamoBillingMode struct{}

func NewDynamoBillingModeRule() FullRule { return ruleDynamoBillingMode{} }

func (ruleDynamoBillingMode) ID() optimize.RuleID { return RuleIDDynamoBillingMode }

func (ruleDynamoBillingMode) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDDynamoBillingMode, Name: "DynamoDB provisioned capacity far above consumption",
		Category: optimize.CategoryCommitment, Action: optimize.ActionSwitchDynamoBilling,
		Description: "A provisioned-capacity table consuming far less than it provisions is paying for idle read/write capacity units.",
		Kinds:       []cloud.Kind{cloud.KindDynamoDBTable}, Enabled: true,
	}
}

func (ruleDynamoBillingMode) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindDynamoDBTable && r.State.Active() && r.Attr("billing_mode", "") == "provisioned"
}

func decideDynamoBillingMode(ctx EvalContext, r cloud.Resource) (ratio float64, provisionedCost, onDemandCost, saving core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDDynamoBillingMode, "min_coverage", 0.5)
	minWindow := ctx.Thresholds.Duration(ctx.TenantID, RuleIDDynamoBillingMode, "min_window_hours", time.Hour, 168*time.Hour)
	if !found || m.Requests == nil || !HasSufficientData(m, minCoverage, minWindow) {
		return
	}
	// Capacity.WriteCapacityRCU/ReadCapacityWCU carry the table's provisioned
	// RCU/WCU counts respectively — the domain model's field names are
	// swapped relative to their JSON tags (write_capacity holds RCU,
	// read_capacity holds WCU); this reads them by what they actually hold,
	// as populated by the discovery adapter, not by their misleading names.
	provisionedRCU := r.Capacity.WriteCapacityRCU
	provisionedWCU := r.Capacity.ReadCapacityWCU
	provisionedTotal := provisionedRCU + provisionedWCU
	if provisionedTotal <= 0 {
		return
	}
	consumedP95 := m.Requests.P95
	ratio = consumedP95 / provisionedTotal
	maxRatio := ctx.Thresholds.Float(ctx.TenantID, RuleIDDynamoBillingMode, "provisioned_consumed_ratio_max", 0.3)
	if ratio > maxRatio {
		return 0, core.Money{}, core.Money{}, core.Money{}, false
	}
	rcuPrice, ok1 := ctx.Pricing.ServicePrice(r.Region, "dynamodb", "rcu_hour")
	wcuPrice, ok2 := ctx.Pricing.ServicePrice(r.Region, "dynamodb", "wcu_hour")
	onDemandReadPrice, ok3 := ctx.Pricing.ServicePrice(r.Region, "dynamodb", "on_demand_read")
	onDemandWritePrice, ok4 := ctx.Pricing.ServicePrice(r.Region, "dynamodb", "on_demand_write")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return 0, core.Money{}, core.Money{}, core.Money{}, false
	}
	provisionedCost = rcuPrice.Scale(provisionedRCU * core.HoursPerMonth).MustAdd(wcuPrice.Scale(provisionedWCU * core.HoursPerMonth))

	// Estimate the on-demand equivalent by splitting the single observed
	// consumed-units series across read/write in the same proportion as the
	// table's own provisioned split — the only read/write ratio the
	// available telemetry supports. on_demand_read/on_demand_write are
	// priced per 1,000 request units (see pricing.ServicePrice's doc
	// comment).
	readShare := provisionedRCU / provisionedTotal
	writeShare := provisionedWCU / provisionedTotal
	secondsPerMonth := core.HoursPerMonth * 3600
	readUnitsPerMonth := consumedP95 * readShare * secondsPerMonth
	writeUnitsPerMonth := consumedP95 * writeShare * secondsPerMonth
	onDemandCost = onDemandReadPrice.Scale(readUnitsPerMonth / 1000).MustAdd(onDemandWritePrice.Scale(writeUnitsPerMonth / 1000))
	if !onDemandCost.LessThan(provisionedCost) {
		return ratio, provisionedCost, onDemandCost, core.Money{}, false
	}
	saving = provisionedCost.MustSub(onDemandCost)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDDynamoBillingMode, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionSwitchDynamoBilling) {
		return ratio, provisionedCost, onDemandCost, core.Money{}, false
	}
	return ratio, provisionedCost, onDemandCost, saving, true
}

func (ruleDynamoBillingMode) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	ratio, provisionedCost, onDemandCost, saving, ok := decideDynamoBillingMode(ctx, r)
	if !ok {
		return nil, nil
	}
	m, _ := MetricsFor(ctx, r.ID)
	evidence := []optimize.Evidence{
		ConfigEvidence("billing mode", "provisioned"),
		MetricEvidence("consumed vs provisioned capacity ratio", &core.Percentiles{P95: ratio}, m.Window, "cloudwatch"),
		CostEvidence("provisioned vs estimated on-demand cost", fmt.Sprintf("%s vs %s", provisionedCost.Format(), onDemandCost.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s consumes only %.0f%% of its provisioned DynamoDB capacity", r.DisplayName(), ratio*100)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleDynamoBillingMode{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "On-demand cost is estimated by splitting observed consumption across read/write in the table's provisioned proportion.",
		Evidence: evidence, CurrentCost: provisionedCost, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleDynamoBillingMode) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, _, onDemandCost, _, ok := decideDynamoBillingMode(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionSwitchDynamoBilling,
		Parameters:    map[string]any{"table_id": r.NativeID, "target_billing_mode": "on_demand"},
		ProposedState: optimize.StateSnapshot{MonthlyCost: onDemandCost, Attributes: map[string]string{"billing_mode": "on_demand"}},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Switch %s to on-demand billing", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
