package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDRDSMultiAZNonProd flags Multi-AZ enabled on a non-production
// database. Multi-AZ roughly doubles the instance charge for standby
// failover capacity that a non-production database rarely needs. No
// executor in this catalogue toggles Multi-AZ, so the recommendation is
// advisory.
//
// Traceability: REQ-OPT-005.
const RuleIDRDSMultiAZNonProd optimize.RuleID = "rds-multiaz-nonprod"

type ruleRDSMultiAZNonProd struct{}

func NewRDSMultiAZNonProdRule() FullRule { return ruleRDSMultiAZNonProd{} }

func (ruleRDSMultiAZNonProd) ID() optimize.RuleID { return RuleIDRDSMultiAZNonProd }

func (ruleRDSMultiAZNonProd) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDRDSMultiAZNonProd, Name: "Multi-AZ enabled on a non-production database",
		Category: optimize.CategoryWaste, Action: optimize.ActionAdvisoryOnly,
		Description: "Multi-AZ doubles the instance charge for standby capacity a non-production database rarely needs.",
		Kinds:       []cloud.Kind{cloud.KindRDSInstance}, Enabled: true,
	}
}

func (ruleRDSMultiAZNonProd) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindRDSInstance && r.State.Active() && !r.Environment.IsProduction() && r.AttrBool("multi_az", false)
}

func decideRDSMultiAZNonProd(ctx EvalContext, r cloud.Resource) (singleAZ, saving core.Money, ok bool) {
	multiAZPrice, ok1 := ctx.Pricing.DatabasePrice(r.Region, r.InstanceType, r.Engine, true)
	singleAZ, ok2 := ctx.Pricing.DatabasePrice(r.Region, r.InstanceType, r.Engine, false)
	if !ok1 || !ok2 || !singleAZ.LessThan(multiAZPrice) {
		return core.Money{}, core.Money{}, false
	}
	saving = multiAZPrice.MustSub(singleAZ).Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSMultiAZNonProd, "min_monthly_saving", 10)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return core.Money{}, core.Money{}, false
	}
	return singleAZ, saving, true
}

func (ruleRDSMultiAZNonProd) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	singleAZ, saving, ok := decideRDSMultiAZNonProd(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("environment", string(r.Environment)),
		CostEvidence("single-AZ equivalent price", fmt.Sprintf("%s/hr", singleAZ.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s (%s) runs Multi-AZ outside production", r.DisplayName(), r.Environment)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleRDSMultiAZNonProd{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Standby failover capacity is rarely needed outside production; disabling Multi-AZ has no executor in this catalogue.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleRDSMultiAZNonProd) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Disable Multi-AZ on %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
