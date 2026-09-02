package automation

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Validate judges a completed change against the ValidationPlan PlanExecution
// declared before anything ran. Every ValidationCheck is evaluated against
// real data — billed cost from ports.CostRepository, utilisation percentiles
// from ports.MetricRepository — rather than the recommendation's own
// prediction; the whole point of validation is to catch a rule whose
// estimate did not hold, so it cannot grade itself against that estimate.
//
// A critical check failing when the plan's ValidationPlan named it in
// AutoRollbackOn (which PlanExecution only populates when the tenant's
// specification enables Automation.AutoRollback) triggers an immediate
// Rollback with the same rigor as any other rollback — Validate does not
// implement a second, lesser rollback path of its own.
func (s *Service) Validate(ctx context.Context, tenant core.TenantID, planID core.ID) (execute.ValidationResult, error) {
	now := s.d.Clock.Now()

	plan, err := s.d.Executions.GetPlan(ctx, tenant, planID)
	if err != nil {
		return execute.ValidationResult{}, fmt.Errorf("automation: loading plan %s: %w", planID, err)
	}
	switch plan.State {
	case execute.PlanExecuted, execute.PlanValidating:
	default:
		return execute.ValidationResult{}, core.NewError(core.ErrPreconditionOff, "not_validatable",
			"plan %s is in state %s and has nothing to validate", planID, plan.State)
	}
	if plan.FinishedAt == nil {
		return execute.ValidationResult{}, core.Invalid("plan %s has no execution finish time to validate from", planID)
	}

	rec, err := s.d.Recommendations.Get(ctx, tenant, plan.RecommendationID)
	if err != nil {
		return execute.ValidationResult{}, fmt.Errorf("automation: loading recommendation %s: %w", plan.RecommendationID, err)
	}
	res, err := s.d.Resources.Get(ctx, tenant, rec.Finding.ResourceID)
	if err != nil {
		return execute.ValidationResult{}, fmt.Errorf("automation: loading resource %s: %w", rec.Finding.ResourceID, err)
	}

	if plan.State == execute.PlanExecuted {
		plan.State = execute.PlanValidating
		if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
			s.d.Logger.Warn("automation: persisting plan before validation", "plan", plan.ID, "error", err)
		}
		s.writeAudit(ctx, tenant, audit.ActionValidationStarted, audit.OutcomeSuccess, "",
			plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
			fmt.Sprintf("validation started for plan %s over a %s observation window", plan.ID, plan.Validation.ObservationWindow), nil, nil)
	}

	baseline := core.NewPeriod(plan.StartedAt.Add(-plan.Validation.BaselineWindow), *plan.StartedAt)
	observedEnd := now
	if windowClose := plan.FinishedAt.Add(plan.Validation.ObservationWindow); windowClose.Before(observedEnd) {
		observedEnd = windowClose
	}
	observed := core.NewPeriod(*plan.FinishedAt, observedEnd)

	checks := make([]execute.CheckOutcome, 0, len(plan.Validation.Checks))
	for _, check := range plan.Validation.Checks {
		checks = append(checks, s.evaluateCheck(ctx, tenant, rec, res, check, observed))
	}

	result := execute.ValidationResult{
		ID: core.NewID("val"), TenantID: tenant, PlanID: plan.ID,
		BaselineWindow: baseline, ObservedWindow: observed, Checks: checks,
		PredictedMonthlySaving: rec.EstimatedMonthlySaving, ObservedMonthlySaving: rec.EstimatedMonthlySaving,
		EvaluatedAt: now,
	}
	if costOutcome, ok := findCheck(checks, "monthly_cost"); ok && costOutcome.Samples > 0 {
		result.ObservedMonthlySaving = core.USDollars(costOutcome.Baseline - costOutcome.Observed)
	}
	result.Decide(plan.Validation.MinSamples)

	// The measured reduction is what the bill did; how much of it this change
	// may be credited with is a separate question. Crediting an optimization
	// with every dollar that left the bill during its window silently absorbs
	// concurrent changes and traffic troughs into CloudOptix's own realized-
	// savings figure — the exact over-claiming the product exists to correct.
	attributed, unattributed, reason := execute.AttributableSaving(
		result.PredictedMonthlySaving, result.ObservedMonthlySaving)
	if !unattributed.IsZero() {
		result.UnattributedDelta = unattributed
		result.AttributionNote = reason
		s.d.Logger.Info("automation: capping saving attribution",
			"plan", plan.ID, "measured", result.ObservedMonthlySaving.Format(),
			"attributed", attributed.Format(), "unattributed", unattributed.Format())
	}

	if err := s.d.Executions.SaveValidation(ctx, result); err != nil {
		s.d.Logger.Warn("automation: persisting validation result", "plan", plan.ID, "error", err)
	}
	s.writeAudit(ctx, tenant, audit.ActionValidationResult, outcomeFor(result.Verdict), "",
		plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
		fmt.Sprintf("plan %s validation: %s — %s", plan.ID, result.Verdict, result.Explanation), nil,
		map[string]any{"verdict": result.Verdict, "observed_monthly_saving": result.ObservedMonthlySaving.Units()})

	var rbErr error
	switch result.Verdict {
	case execute.VerdictSuccess, execute.VerdictPartialSuccess:
		plan.State = execute.PlanValidated
		if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
			s.d.Logger.Warn("automation: persisting validated plan", "plan", plan.ID, "error", err)
		}
		s.touchSavings(ctx, tenant, rec, res, plan.ID, execute.StageValidated, attributed,
			"", "observation window closed with verdict "+string(result.Verdict), now)
		if result.Verdict == execute.VerdictSuccess {
			// A full success was judged against real measured cost/metric
			// data in this same call — that measurement IS the billing
			// confirmation StageRealized calls for, not a restated
			// prediction, so it is safe to advance straight to Realized
			// rather than waiting for a separate reconciliation pass.
			s.touchSavings(ctx, tenant, rec, res, plan.ID, execute.StageRealized, attributed,
				"", "all validation checks passed against measured data", now)
			s.publish(ctx, ports.Event{Type: ports.EventSavingsRealized, TenantID: tenant, SubjectID: plan.ID,
				CorrelationID: string(rec.ID), Payload: map[string]any{"monthly_saving": attributed.Units()}})
		}
		rec.Status = optimize.StatusValidated
		rec.UpdatedAt = now
		if err := s.d.Recommendations.Update(ctx, rec); err != nil {
			s.d.Logger.Warn("automation: updating recommendation after validation", "recommendation", rec.ID, "error", err)
		}
		s.publish(ctx, ports.Event{Type: ports.EventOptimizationValidated, TenantID: tenant, SubjectID: plan.ID,
			CorrelationID: string(rec.ID), Payload: map[string]any{"verdict": string(result.Verdict)}})

	case execute.VerdictFailure:
		if autoRollbackTriggered(plan.Validation.AutoRollbackOn, checks) {
			result.RollbackTriggered = true
			result.RollbackReason = result.Explanation
			if err := s.d.Executions.SaveValidation(ctx, result); err != nil {
				s.d.Logger.Warn("automation: persisting validation after triggering rollback", "plan", plan.ID, "error", err)
			}
			_, rbErr = s.Rollback(ctx, tenant, plan.ID,
				"auto-rollback: critical validation check failed: "+result.Explanation, systemActor)
		} else {
			plan.State = execute.PlanFailed
			plan.StateReason = "validation failed: " + result.Explanation
			if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
				s.d.Logger.Warn("automation: persisting failed-validation plan", "plan", plan.ID, "error", err)
			}
			s.loseSavings(ctx, tenant, plan.RecommendationID, "validation failed: "+result.Explanation, now)
			rec.Status = optimize.StatusFailed
			rec.StatusReason = result.Explanation
			rec.UpdatedAt = now
			if err := s.d.Recommendations.Update(ctx, rec); err != nil {
				s.d.Logger.Warn("automation: updating recommendation after failed validation", "recommendation", rec.ID, "error", err)
			}
		}

	case execute.VerdictInconclusive:
		// Neither success nor failure: the plan stays PlanValidating so a
		// later call (once more telemetry has accumulated) can reach a real
		// verdict. Declaring success on insufficient data would be exactly
		// the "quiet weekend" failure mode ValidationPlan.MinSamples exists
		// to prevent. No outcome is recorded yet either — there is nothing
		// judged to learn from.
	}

	// Every judged verdict (everything but Inconclusive) is fed to the
	// learning loop as a real, measured execute.Outcome — recorded here
	// because Validate is the one place that has both the prediction (from
	// the recommendation) and the measurement (from this call) in hand at
	// the same time. See internal/application/learning's package doc for
	// what happens to it downstream: never anything that touches a rule,
	// a policy, or a safety guard.
	if result.Verdict != execute.VerdictInconclusive {
		s.recordOutcome(ctx, tenant, rec, res, result)
	}

	if rbErr != nil {
		return result, fmt.Errorf("automation: validation failed and auto-rollback also failed: %w", rbErr)
	}
	return result, nil
}

// recordOutcome persists the measured result of a judged validation as an
// execute.Outcome. A failure to save it is logged and swallowed, matching
// this package's audit-write convention: the validation judgment already
// happened and has already been persisted and returned to the caller — a
// downstream feedback-loop write failing must not retroactively make that
// look like an error.
func (s *Service) recordOutcome(ctx context.Context, tenant core.TenantID, rec optimize.Recommendation, res cloud.Resource, result execute.ValidationResult) {
	o := execute.Outcome{
		TenantID: tenant, RuleID: rec.Finding.RuleID, Action: rec.Action, ResourceKind: string(res.Kind),
		Environment: res.Environment, PredictedMonthlySaving: rec.EstimatedMonthlySaving,
		ActualMonthlySaving: result.ObservedMonthlySaving, PredictedConfidence: rec.Confidence,
		PredictedRisk: rec.Risk.Level, Verdict: result.Verdict, RolledBack: result.RollbackTriggered,
		ObservedAt: result.EvaluatedAt,
	}
	if !o.PredictedMonthlySaving.IsZero() {
		o.SavingRatio = o.ActualMonthlySaving.Ratio(o.PredictedMonthlySaving)
	}
	if err := s.d.Savings.SaveOutcome(ctx, o); err != nil {
		s.d.Logger.Warn("automation: recording outcome for the learning loop failed", "recommendation", rec.ID, "error", err)
	}
}

func outcomeFor(v execute.Verdict) audit.Outcome {
	switch v {
	case execute.VerdictSuccess, execute.VerdictPartialSuccess:
		return audit.OutcomeSuccess
	case execute.VerdictFailure:
		return audit.OutcomeFailure
	default:
		return audit.OutcomePartial
	}
}

func findCheck(checks []execute.CheckOutcome, metric string) (execute.CheckOutcome, bool) {
	for _, c := range checks {
		if c.Metric == metric {
			return c, true
		}
	}
	return execute.CheckOutcome{}, false
}

// autoRollbackTriggered reports whether any check named in AutoRollbackOn
// actually failed. AutoRollbackOn only ever names critical checks (see
// buildValidationPlan), so this deliberately does not re-check Critical
// here — a name appearing in the list is the authorization by itself.
func autoRollbackTriggered(autoRollbackOn []string, checks []execute.CheckOutcome) bool {
	if len(autoRollbackOn) == 0 {
		return false
	}
	names := make(map[string]bool, len(autoRollbackOn))
	for _, n := range autoRollbackOn {
		names[n] = true
	}
	for _, c := range checks {
		if names[c.Name] && !c.Passed {
			return true
		}
	}
	return false
}

// evaluateCheck reads whatever real data source the check's metric maps to
// and compares it against the check's declared threshold. A check whose
// source is unavailable (no Costs or Metrics dependency wired, or no data
// yet in the observation window) is not silently skipped: it is reported
// with Samples 0, which ValidationResult.Decide treats as insufficient
// evidence for that specific check rather than as a pass.
func (s *Service) evaluateCheck(ctx context.Context, tenant core.TenantID, rec optimize.Recommendation, res cloud.Resource, check execute.ValidationCheck, observed core.Period) execute.CheckOutcome {
	out := execute.CheckOutcome{Name: check.Name, Metric: check.Metric, Threshold: check.Threshold, Critical: check.Critical}

	switch check.Metric {
	case "monthly_cost":
		out.Baseline = rec.Finding.CurrentMonthlyCost.Units()
		out.Observed = rec.ProposedState.MonthlyCost.Units()
		if s.d.Costs != nil {
			if total, err := s.d.Costs.Total(ctx, tenant, ports.CostFilter{Period: observed, ResourceIDs: []core.ID{res.ID}}); err == nil {
				if hours := observed.End.Sub(observed.Start).Hours(); hours > 0 {
					out.Observed = total.Div(hours / 24).Monthly().Units()
					out.Samples = 1
				}
			}
		}
		if out.Samples == 0 {
			out.Detail = "no billed cost data available yet for the observation window; compared against the predicted post-change cost"
		}
	case "cpu_utilization", "error_rate", "latency_p99_ms":
		if s.d.Metrics != nil {
			if summary, err := s.d.Metrics.GetSummary(ctx, tenant, res.ID); err == nil {
				if p := percentilesFor(summary, check.Metric); p != nil {
					out.Observed = statisticValue(*p, check.Statistic)
					out.Samples = p.Samples
				}
			}
		}
		if out.Samples == 0 {
			out.Detail = "no utilisation telemetry available yet for this resource"
		}
	default:
		out.Detail = "unrecognized validation metric " + check.Metric
	}

	out.Passed, out.ChangePct = evaluateComparison(check.Comparison, out.Baseline, out.Observed, check.Threshold)
	return out
}

func percentilesFor(summary ports.ResourceMetrics, metric string) *core.Percentiles {
	switch metric {
	case "cpu_utilization":
		return summary.CPU
	case "error_rate":
		return summary.ErrorRate
	case "latency_p99_ms":
		return summary.LatencyP99
	}
	return nil
}

func statisticValue(p core.Percentiles, statistic string) float64 {
	switch statistic {
	case "p50":
		return p.P50
	case "p90":
		return p.P90
	case "p95":
		return p.P95
	case "p99":
		return p.P99
	case "max":
		return p.Max
	case "avg", "mean":
		return p.Mean
	default:
		return p.P95
	}
}

// evaluateComparison applies one of ValidationCheck's four comparison
// vocabularies. "no_worse_than_pct" and "improved_by_pct" are relative to
// baseline; "below_absolute" and "above_absolute" ignore baseline entirely,
// which is what lets a check like error_rate_bounded work with no
// pre-change measurement at all — only a threshold the post-change value
// must respect.
func evaluateComparison(comparison string, baseline, observed, threshold float64) (passed bool, changePct float64) {
	switch comparison {
	case "no_worse_than_pct":
		changePct = pctChange(baseline, observed)
		return changePct <= threshold, changePct
	case "improved_by_pct":
		changePct = pctChange(observed, baseline) // baseline relative to observed, i.e. how much observed improved on baseline
		return changePct >= threshold, changePct
	case "below_absolute":
		return observed <= threshold, observed - threshold
	case "above_absolute":
		return observed >= threshold, threshold - observed
	default:
		return false, 0
	}
}

func pctChange(baseline, observed float64) float64 {
	if baseline == 0 {
		if observed == 0 {
			return 0
		}
		return 100
	}
	return ((observed - baseline) / baseline) * 100
}
