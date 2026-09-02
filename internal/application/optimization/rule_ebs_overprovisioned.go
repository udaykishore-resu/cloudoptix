package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEBSOverprovisioned flags an attached volume whose observed disk
// usage is far below its allocated size, or whose observed IOPS is far below
// its provisioned IOPS (io1/io2/gp3 only — gp2's IOPS is size-derived and not
// separately priced, so there is nothing to reclaim there). Both dimensions
// are evaluated and summed independently; a volume can be over-provisioned on
// one axis and not the other.
//
// Traceability: REQ-OPT-004.
const RuleIDEBSOverprovisioned optimize.RuleID = "ebs-overprovisioned"

type ruleEBSOverprovisioned struct{}

func NewEBSOverprovisionedRule() FullRule { return ruleEBSOverprovisioned{} }

func (ruleEBSOverprovisioned) ID() optimize.RuleID { return RuleIDEBSOverprovisioned }

func (ruleEBSOverprovisioned) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEBSOverprovisioned, Name: "Over-provisioned EBS volume (size and/or IOPS)",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionResizeVolume,
		Description: "Allocated size far exceeds observed disk usage, or provisioned IOPS far " +
			"exceeds observed IOPS.",
		Kinds: []cloud.Kind{cloud.KindEBSVolume}, Enabled: true,
	}
}

func (ruleEBSOverprovisioned) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEBSVolume && r.State == cloud.StateInUse
}

type ebsOverprovDecision struct {
	ok                     bool
	m                      ports.ResourceMetrics
	targetGiB              float64
	targetIOPS             int64
	sizeSaving, iopsSaving core.Money
	totalSaving            core.Money
}

func decideEBSOverprovisioned(ctx EvalContext, r cloud.Resource) ebsOverprovDecision {
	m, found := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSOverprovisioned, "min_coverage", 0.4)
	if !found || m.Coverage < minCoverage {
		return ebsOverprovDecision{}
	}

	targetGiB := r.Capacity.StorageGiB
	sizeSaving := core.ZeroUSD()
	if m.DiskUsed != nil {
		usedMax := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSOverprovisioned, "used_fraction_max", 0.4)
		if m.DiskUsed.P95/100.0 <= usedMax && r.Capacity.StorageGiB > 0 {
			t := maxFloat(10, r.Capacity.StorageGiB*(m.DiskUsed.P99/100.0)*1.3)
			if t < r.Capacity.StorageGiB {
				targetGiB = t
				if price, ok := ctx.Pricing.StoragePrice(r.Region, r.InstanceType); ok {
					sizeSaving = price.Scale(r.Capacity.StorageGiB - targetGiB)
				}
			}
		}
	}

	targetIOPS := r.Capacity.ProvisionedIOPS
	iopsSaving := core.ZeroUSD()
	if m.IOPS != nil && r.Capacity.ProvisionedIOPS > 0 {
		if iopsPrice, ok := ctx.Pricing.IOPSPrice(r.Region, r.InstanceType); ok {
			iopsMax := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSOverprovisioned, "iops_used_fraction_max", 0.5)
			if m.IOPS.P95/float64(r.Capacity.ProvisionedIOPS) <= iopsMax {
				t := int64(maxFloat(3000, m.IOPS.P99*1.3))
				if t < r.Capacity.ProvisionedIOPS {
					targetIOPS = t
					iopsSaving = iopsPrice.Scale(float64(r.Capacity.ProvisionedIOPS - targetIOPS))
				}
			}
		}
	}

	total := sizeSaving.MustAdd(iopsSaving)
	if total.IsZero() {
		return ebsOverprovDecision{}
	}
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEBSOverprovisioned, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, total) || ExcludedBySpec(ctx.Spec, r, optimize.ActionResizeVolume) {
		return ebsOverprovDecision{}
	}
	return ebsOverprovDecision{
		ok: true, m: m, targetGiB: targetGiB, targetIOPS: targetIOPS,
		sizeSaving: sizeSaving, iopsSaving: iopsSaving, totalSaving: total,
	}
}

func (ruleEBSOverprovisioned) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	d := decideEBSOverprovisioned(ctx, r)
	if !d.ok {
		return nil, nil
	}
	var evidence []optimize.Evidence
	if d.m.DiskUsed != nil {
		evidence = append(evidence, MetricEvidence("disk usage", d.m.DiskUsed, d.m.Window, "cloudwatch"))
	}
	if d.m.IOPS != nil {
		evidence = append(evidence, MetricEvidence("provisioned-IOPS utilization", d.m.IOPS, d.m.Window, "cloudwatch"))
	}
	evidence = append(evidence, ConfigEvidence("allocated", fmt.Sprintf("%.0f GiB, %d IOPS", r.Capacity.StorageGiB, r.Capacity.ProvisionedIOPS)))
	summary := fmt.Sprintf("%s is over-provisioned: %.0f GiB allocated (target ~%.0f GiB), %d IOPS provisioned (target ~%d)",
		r.DisplayName(), r.Capacity.StorageGiB, d.targetGiB, r.Capacity.ProvisionedIOPS, d.targetIOPS)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEBSOverprovisioned{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "Observed disk usage and/or provisioned-IOPS consumption are far below what is allocated.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: d.totalSaving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleEBSOverprovisioned) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	d := decideEBSOverprovisioned(ctx, r)
	if !d.ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	complexity := optimize.ComplexityLow
	reversibility := optimize.ReversibilityFast
	if d.targetGiB < r.Capacity.StorageGiB {
		// AWS cannot shrink a volume in place; a size reduction requires a
		// snapshot-and-recreate migration, which is slower and harder to
		// undo than a pure IOPS/throughput change.
		complexity = optimize.ComplexityMedium
		reversibility = optimize.ReversibilitySlow
	}
	return RuleAction{
		Type: optimize.ActionResizeVolume,
		Parameters: map[string]any{
			"volume_id": r.NativeID, "size_gib": d.targetGiB, "iops": d.targetIOPS,
		},
		ProposedState: optimize.StateSnapshot{SizeGiB: d.targetGiB, IOPS: d.targetIOPS, MonthlyCost: CostFor(ctx, r).MustSub(d.totalSaving)},
		Reversibility: reversibility,
		Complexity:    complexity,
		Title:         fmt.Sprintf("Right-size volume %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
