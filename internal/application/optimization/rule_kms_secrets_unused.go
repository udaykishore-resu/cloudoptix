package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDKMSSecretsUnused flags a customer-managed KMS key or a Secrets
// Manager secret with no recorded use in at least the idle-day guard: both
// bill a flat monthly charge regardless of use. Deleting either is
// destructive — neither can be un-deleted after its recovery window — so
// this is advisory only, never auto-executed regardless of the tenant's risk
// tolerance.
//
// Traceability: REQ-OPT-010.
const RuleIDKMSSecretsUnused optimize.RuleID = "kms-secrets-unused"

type ruleKMSSecretsUnused struct{}

func NewKMSSecretsUnusedRule() FullRule { return ruleKMSSecretsUnused{} }

func (ruleKMSSecretsUnused) ID() optimize.RuleID { return RuleIDKMSSecretsUnused }

func (ruleKMSSecretsUnused) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDKMSSecretsUnused, Name: "Unused KMS keys and Secrets Manager secrets", Category: optimize.CategoryWaste,
		Action:      optimize.ActionAdvisoryOnly,
		Description: "A key or secret with no recorded use over the idle-day guard is paying its flat monthly charge for nothing.",
		Kinds:       []cloud.Kind{cloud.KindKMSKey, cloud.KindSecret}, Enabled: true,
	}
}

func (ruleKMSSecretsUnused) Applies(r cloud.Resource) bool {
	return (r.Kind == cloud.KindKMSKey || r.Kind == cloud.KindSecret) && r.State.Active()
}

func decideKMSSecretsUnused(ctx EvalContext, r cloud.Resource) (idleDays float64, cost core.Money, ok bool) {
	idleDays = parseFloatAttr(r.Attr("days_since_last_use", ""), -1)
	if idleDays < 0 {
		return
	}
	minIdleDays := ctx.Thresholds.Float(ctx.TenantID, RuleIDKMSSecretsUnused, "min_idle_days", 90)
	if idleDays < minIdleDays {
		return idleDays, core.Money{}, false
	}
	dimension := "key_month"
	service := "kms"
	if r.Kind == cloud.KindSecret {
		service, dimension = "secretsmanager", "secret_month"
	}
	price, found := ctx.Pricing.ServicePrice(r.Region, service, dimension)
	if !found {
		return idleDays, core.Money{}, false
	}
	cost = price
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDKMSSecretsUnused, "min_monthly_saving", 1)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return idleDays, core.Money{}, false
	}
	return idleDays, cost, true
}

func (ruleKMSSecretsUnused) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	idleDays, cost, ok := decideKMSSecretsUnused(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("days since last recorded use", fmt.Sprintf("%.0f", idleDays)),
	}
	summary := fmt.Sprintf("%s has had no recorded use for %.0f days", r.DisplayName(), idleDays)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleKMSSecretsUnused{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Deletion is destructive and cannot be undone after the recovery window; advisory only.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleKMSSecretsUnused) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Confirm and delete unused %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
