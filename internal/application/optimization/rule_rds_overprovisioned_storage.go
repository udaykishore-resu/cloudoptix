package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDRDSOverprovisionedStorage flags allocated RDS storage far above
// observed disk usage. RDS storage can only grow, never shrink in place, so
// the fix is a migration (snapshot, restore into a smaller allocation, or a
// blue/green cutover) rather than a single API call — the recommendation is
// still surfaced because the gap compounds every month it goes unaddressed.
//
// That is precisely why this rule is advisory rather than emitting
// modify_rds_storage. The modify_rds_storage executor exists and works, but
// rds:ModifyDBInstance can only increase AllocatedStorage; the executor
// checks the target against the current size and refuses a decrease outright
// (see internal/adapters/aws/executor/rds.go). A shrink recommendation
// wearing that action is a recommendation that clears policy and approval
// and then fails at the mutate step every single time. Renaming the
// parameter would not have helped: the shape was never the problem, the verb
// was.
//
// Traceability: REQ-OPT-005.
const RuleIDRDSOverprovisionedStorage optimize.RuleID = "rds-overprovisioned-storage"

type ruleRDSOverprovisionedStorage struct{}

func NewRDSOverprovisionedStorageRule() FullRule { return ruleRDSOverprovisionedStorage{} }

func (ruleRDSOverprovisionedStorage) ID() optimize.RuleID { return RuleIDRDSOverprovisionedStorage }

func (ruleRDSOverprovisionedStorage) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDRDSOverprovisionedStorage, Name: "RDS storage over-provisioned",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionAdvisoryOnly,
		Description: "Allocated storage far exceeds observed disk usage. RDS storage cannot be shrunk in place, " +
			"so realizing this needs a snapshot-and-restore or blue/green migration a human runs.",
		Kinds: []cloud.Kind{cloud.KindRDSInstance}, Enabled: true,
	}
}

func (ruleRDSOverprovisionedStorage) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindRDSInstance && r.State.Active() && r.Capacity.StorageGiB > 0
}

func decideRDSOverprovisionedStorage(ctx EvalContext, r cloud.Resource) (m ports.ResourceMetrics, targetGiB float64, saving core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSOverprovisionedStorage, "min_coverage", 0.4)
	if !found || m.DiskUsed == nil || m.Coverage < minCoverage {
		return
	}
	usedMax := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSOverprovisionedStorage, "used_fraction_max", 0.4)
	if m.DiskUsed.P95/100.0 > usedMax {
		return
	}
	targetGiB = maxFloat(20, r.Capacity.StorageGiB*(m.DiskUsed.P99/100.0)*1.3)
	if targetGiB >= r.Capacity.StorageGiB {
		return
	}
	storageClass := "rds_" + firstNonEmpty(r.Attr("storage_type", ""), "gp3")
	price, found := ctx.Pricing.StoragePrice(r.Region, storageClass)
	if !found {
		return
	}
	saving = price.Scale(r.Capacity.StorageGiB - targetGiB)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSOverprovisionedStorage, "min_monthly_saving", 10)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return ports.ResourceMetrics{}, 0, core.Money{}, false
	}
	return m, targetGiB, saving, true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (ruleRDSOverprovisionedStorage) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	m, targetGiB, saving, ok := decideRDSOverprovisionedStorage(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		MetricEvidence("disk usage", m.DiskUsed, m.Window, "cloudwatch"),
		ConfigEvidence("allocated storage", fmt.Sprintf("%.0f GiB", r.Capacity.StorageGiB)),
	}
	summary := fmt.Sprintf("%s allocates %.0f GiB but uses far less (target ~%.0f GiB)", r.DisplayName(), r.Capacity.StorageGiB, targetGiB)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleRDSOverprovisionedStorage{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "RDS storage cannot shrink in place; a migration is required to realize this saving.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleRDSOverprovisionedStorage) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	_, targetGiB, saving, ok := decideRDSOverprovisionedStorage(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type: optimize.ActionAdvisoryOnly,
		// Read by a person planning the migration, not by an executor.
		Parameters: map[string]any{
			"db_instance_id":                r.NativeID,
			"current_allocated_storage_gib": r.Capacity.StorageGiB,
			"recommended_storage_gib":       targetGiB,
			"migration":                     "snapshot and restore into a smaller allocation, or cut over via blue/green; rds:ModifyDBInstance cannot reduce allocated storage",
		},
		ProposedState: optimize.StateSnapshot{SizeGiB: targetGiB, MonthlyCost: CostFor(ctx, r).MustSub(saving)},
		Reversibility: optimize.ReversibilitySlow,
		Complexity:    optimize.ComplexityHigh,
		// Declared explicitly because advisory_only carries no default
		// domain: this advice competes with any other change to the same
		// instance's allocated storage (rds-gp2-to-gp3's storage-type
		// change), and composes with changes to its instance class.
		ConflictDomain: optimize.ConflictDomainRDSStorage,
		Title:          fmt.Sprintf("Reduce allocated storage on %s", r.DisplayName()),
		Rationale:      f.Detail,
	}
}
