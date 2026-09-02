package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// sloDeclared reports whether a workload's SLO block carries any actual
// contract rather than being the zero value left by a tenant who never
// filled it in — an unset SLO must never be read as "no headroom risk" by
// omission alone; it is read as "we don't know", which this function
// reports as false so the caller simply skips the factor rather than
// asserting a headroom risk it cannot support.
func sloDeclared(s spec.WorkloadSLO) bool {
	return s.Availability > 0 || s.LatencyP95MS > 0 || s.LatencyP99MS > 0 || s.ErrorRateMax > 0
}

// This file computes optimize.RiskAssessment deterministically from
// structured facts about the action, the resource and its context — never
// from a model's judgement. Same inputs always yield the same score, the same
// level and the same factor list, which is what lets a policy rule key off
// MaxRiskLevel and mean the same thing every time it is evaluated.
//
// Traceability: REQ-OPT-007, SPEC-OPT-006.

// capacityReducingActions are the action types that shrink a resource's
// provisioned capacity, which is the set the SLO-headroom factor applies to:
// an action that does not touch capacity cannot regress latency or
// throughput no matter how tight the SLO is.
var capacityReducingActions = map[optimize.ActionType]bool{
	optimize.ActionResizeInstance:     true,
	optimize.ActionResizeRDS:          true,
	optimize.ActionResizeVolume:       true,
	optimize.ActionResizeNodeGroup:    true,
	optimize.ActionAdjustPodResources: true,
	optimize.ActionResizeLambdaMemory: true,
	optimize.ActionStopInstance:       true,
	optimize.ActionStopRDS:            true,
	optimize.ActionTerminateInstance:  true,
}

// storageTouchingActions carry a data-loss risk smaller than an outright
// deletion but larger than zero: a botched storage-type or size change can
// still corrupt or truncate data even though the action itself is not in
// optimize.ActionType.Destructive().
var storageTouchingActions = map[optimize.ActionType]bool{
	optimize.ActionResizeVolume:        true,
	optimize.ActionModifyVolumeType:    true,
	optimize.ActionModifyRDSStorage:    true,
	optimize.ActionResizeRDS:           true,
	optimize.ActionStopRDS:             true,
	optimize.ActionRemoveRDSReplica:    true,
	optimize.ActionSwitchDynamoBilling: true,
}

// ComputeRisk assesses one proposed action against one resource.
func ComputeRisk(ctx EvalContext, r cloud.Resource, action optimize.ActionType, reversibility optimize.Reversibility, blast optimize.BlastRadius) optimize.RiskAssessment {
	var factors []optimize.RiskFactor

	actionScore := 0.0
	switch {
	case action.Destructive():
		actionScore = 0.30
		factors = append(factors, optimize.RiskFactor{Name: "action_class", Contribution: actionScore,
			Explanation: fmt.Sprintf("%s is destructive: it cannot be undone through the AWS API", action)})
	case action.Mutating():
		actionScore = 0.10
		factors = append(factors, optimize.RiskFactor{Name: "action_class", Contribution: actionScore,
			Explanation: fmt.Sprintf("%s mutates live infrastructure but is reversible", action)})
	default:
		factors = append(factors, optimize.RiskFactor{Name: "action_class", Contribution: 0,
			Explanation: "advisory only: no infrastructure is mutated"})
	}

	envScore := 0.0
	switch {
	case r.Environment.IsProduction():
		envScore = 0.20
	case r.Environment == core.EnvStaging:
		envScore = 0.08
	}
	if envScore > 0 {
		factors = append(factors, optimize.RiskFactor{Name: "environment", Contribution: envScore,
			Explanation: fmt.Sprintf("target environment is %s", r.Environment)})
	}

	critScore := r.Criticality.Weight() * 0.15
	factors = append(factors, optimize.RiskFactor{Name: "criticality", Contribution: critScore,
		Explanation: fmt.Sprintf("resource criticality %s", nonEmpty(string(r.Criticality), "UNSET"))})

	revScore := (1 - reversibility.Factor()) * 0.15
	if revScore > 0.01 {
		factors = append(factors, optimize.RiskFactor{Name: "reversibility", Contribution: revScore,
			Explanation: fmt.Sprintf("reversibility rated %q", reversibility)})
	}

	dataLossScore := 0.0
	switch {
	case action.Destructive():
		dataLossScore = 0.25
	case storageTouchingActions[action]:
		dataLossScore = 0.08
	}
	if dataLossScore > 0 {
		factors = append(factors, optimize.RiskFactor{Name: "data_loss_potential", Contribution: dataLossScore,
			Explanation: "action touches persisted data or a data-serving replica"})
	}

	sloScore := 0.0
	if capacityReducingActions[action] {
		if w, ok := matchedWorkload(ctx.Spec, r); ok && sloDeclared(w.SLO) {
			sloScore = 0.15 * r.Criticality.Weight()
			if sloScore > 0.02 {
				factors = append(factors, optimize.RiskFactor{Name: "slo_headroom", Contribution: sloScore,
					Explanation: fmt.Sprintf("workload %q declares an SLO; capacity reduction risks headroom", w.Name)})
			}
		}
	}

	blastScore := blast.Score * 0.25
	factors = append(factors, optimize.RiskFactor{Name: "blast_radius", Contribution: blastScore,
		Explanation: blast.Explanation})

	total := actionScore + envScore + critScore + revScore + dataLossScore + sloScore + blastScore
	score := clamp01(total)

	assessment := optimize.RiskAssessment{
		Score:            score,
		Level:            core.RiskLevelFromScore(score),
		AvailabilityRisk: core.RiskLevelFromScore(clamp01(actionScore + blastScore + sloScore)),
		PerformanceRisk:  core.RiskLevelFromScore(clamp01(sloScore + revScore)),
		SecurityRisk:     core.RiskLevelFromScore(securityScore(r, action)),
		DataLossRisk:     core.RiskLevelFromScore(clamp01(dataLossScore / 0.25 * 0.7)),
		Factors:          factors,
	}
	assessment.Mitigations = mitigations(ctx, r, action, reversibility, blast, assessment)
	return assessment
}

func securityScore(r cloud.Resource, action optimize.ActionType) float64 {
	if (r.Kind == cloud.KindKMSKey || r.Kind == cloud.KindSecret) && action != optimize.ActionAdvisoryOnly {
		return 0.5
	}
	return 0
}

func mitigations(ctx EvalContext, r cloud.Resource, action optimize.ActionType, reversibility optimize.Reversibility, blast optimize.BlastRadius, a optimize.RiskAssessment) []string {
	var out []string
	if action.Destructive() {
		out = append(out, "Confirm no other resource still depends on this before executing; the action cannot be undone through the API.")
	}
	if reversibility == optimize.ReversibilitySlow || reversibility == optimize.ReversibilityNone {
		out = append(out, "Take a fresh backup or snapshot immediately before executing.")
	}
	if r.Environment.IsProduction() {
		if _, ok := inMaintenanceWindow(ctx.Spec, r.Environment, ctx.Now()); !ok {
			out = append(out, "Schedule execution inside an approved maintenance window; none is currently active.")
		}
	}
	if blast.Score >= 0.4 {
		out = append(out, fmt.Sprintf("Review the %d affected downstream resource(s) in the blast radius before approving.", blast.ResourcesAffected))
	}
	if blast.Completeness < 0.5 {
		out = append(out, "The dependency graph for this resource is incompletely observed; treat the blast radius as a lower bound.")
	}
	if r.Criticality == core.CriticalityTier0 || r.Criticality == core.CriticalityTier1 {
		out = append(out, "Require a second approver given this resource's business criticality.")
	}
	if a.Level.AtLeast(core.RiskHigh) {
		out = append(out, "Stage this change in a non-production environment first if an equivalent resource exists there.")
	}
	return out
}
