package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEC2StoppedStorage flags a long-stopped EC2 instance whose attached
// EBS volumes keep billing even though the compute charge has stopped.
//
// Traceability: REQ-OPT-003.
const RuleIDEC2StoppedStorage optimize.RuleID = "ec2-stopped-still-billing-storage"

type ruleEC2StoppedStorage struct{}

func NewEC2StoppedStorageRule() FullRule { return ruleEC2StoppedStorage{} }

func (ruleEC2StoppedStorage) ID() optimize.RuleID { return RuleIDEC2StoppedStorage }

func (ruleEC2StoppedStorage) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEC2StoppedStorage, Name: "Stopped EC2 instance still billing attached storage",
		Category: optimize.CategoryWaste, Action: optimize.ActionAdvisoryOnly,
		Description: "A stopped instance's compute charge has stopped but its attached EBS " +
			"volumes have not; long-stopped instances are candidates to terminate or resume.",
		Kinds: []cloud.Kind{cloud.KindEC2Instance}, Enabled: true,
	}
}

func (ruleEC2StoppedStorage) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEC2Instance && r.State == cloud.StateStopped
}

func decideEC2StoppedStorage(ctx EvalContext, r cloud.Resource) (days float64, storageCost core.Money, ok bool) {
	stoppedAt := r.Attr("stopped_at", "")
	if stoppedAt == "" {
		return 0, core.Money{}, false
	}
	t, err := parseDateAttr(stoppedAt)
	if err != nil {
		return 0, core.Money{}, false
	}
	days = daysSince(t, ctx.Now())
	minDays := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2StoppedStorage, "min_stopped_days", 14)
	if days < minDays {
		return 0, core.Money{}, false
	}
	// The instance's own MonthlyCost, once stopped, IS the storage cost: the
	// awssim and real-world compute charge for a stopped instance is zero, so
	// whatever is still attributed here is the attached-volume charge.
	storageCost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2StoppedStorage, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, storageCost) {
		return 0, core.Money{}, false
	}
	if ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return 0, core.Money{}, false
	}
	return days, storageCost, true
}

func (ruleEC2StoppedStorage) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	days, cost, ok := decideEC2StoppedStorage(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("stopped since", r.Attr("stopped_at", "")),
		CostEvidence("attributed monthly cost while stopped", cost.Format(), "cost_engine"),
	}
	summary := fmt.Sprintf("%s has been stopped for %.0f days and is still billing attached storage", r.DisplayName(), days)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2StoppedStorage{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Review whether this instance should be terminated (after a final snapshot) or resumed.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEC2StoppedStorage) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Review long-stopped instance %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
