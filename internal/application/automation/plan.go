package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// defaultObservationWindow and defaultMinValidationSamples are used when a
// tenant's specification does not declare a validation window, so Validate
// always has something concrete to wait for rather than defaulting to zero
// (which would validate immediately, against no data).
const (
	defaultObservationWindow    = 30 * time.Minute
	defaultMinValidationSamples = 1
)

// PlanExecution looks up the executor for the recommendation's action,
// assumes a scoped AWS session, and asks the executor to build the forward
// steps, the snapshot steps and the rollback plan. It performs no mutation:
// every step it produces is Kind precondition, snapshot, mutate or verify —
// none of them have run yet.
//
// Governance is consulted here too, not only later in Execute, because
// PlanExecution is also the point at which an approval request must be
// created for anything the current policy does not already clear for
// unattended execution: a plan a human never gets a chance to review is not
// a plan this platform will build.
func (s *Service) PlanExecution(ctx context.Context, tenant core.TenantID, recommendationID core.ID, in ports.PlanOptions) (execute.Plan, error) {
	now := s.d.Clock.Now()

	rec, err := s.d.Recommendations.Get(ctx, tenant, recommendationID)
	if err != nil {
		return execute.Plan{}, fmt.Errorf("automation: loading recommendation %s: %w", recommendationID, err)
	}
	if rec.Status.Terminal() {
		return execute.Plan{}, core.NewError(core.ErrPreconditionOff, "recommendation_terminal",
			"recommendation %s is already %s and cannot be planned", rec.ID, rec.Status)
	}
	if rec.Action == optimize.ActionAdvisoryOnly {
		return execute.Plan{}, core.Invalid("recommendation %s is advisory-only and has no executor", rec.ID)
	}

	res, err := s.d.Resources.Get(ctx, tenant, rec.Finding.ResourceID)
	if err != nil {
		return execute.Plan{}, fmt.Errorf("automation: loading resource %s: %w", rec.Finding.ResourceID, err)
	}
	account, err := s.resolveAccount(ctx, tenant, res.AccountID)
	if err != nil {
		return execute.Plan{}, err
	}
	executor, err := s.executorFor(rec.Action)
	if err != nil {
		return execute.Plan{}, err
	}

	decision, err := s.d.Governance.Evaluate(ctx, tenant, recommendationID)
	if err != nil {
		return execute.Plan{}, fmt.Errorf("automation: evaluating governance for %s: %w", recommendationID, err)
	}
	if decision.Effect == govern.EffectProhibit {
		return execute.Plan{}, core.NewError(core.ErrForbidden, "change_prohibited",
			"recommendation %s: %s", recommendationID, decision.Reason)
	}

	session, err := s.d.Credentials.Assume(ctx, account, cloud.ScopeExecute)
	if err != nil {
		return execute.Plan{}, fmt.Errorf("automation: assuming execute-scoped session for account %s: %w", account.AccountID, err)
	}

	plan, err := executor.Plan(ctx, ports.ExecutionPlanInput{
		TenantID: tenant, Recommendation: rec, Resource: res, Account: account,
		Session: session, DryRun: in.DryRun, RequestedBy: in.RequestedBy,
	})
	if err != nil {
		return execute.Plan{}, fmt.Errorf("automation: building plan for %s: %w", rec.Action, err)
	}

	// Defense in depth: the executor contract already requires this, but the
	// orchestration layer must never trust a plan it is about to persist and
	// hand to Execute without checking for itself. See doc.go.
	if plan.Rollback == nil {
		return execute.Plan{}, core.Invalid("automation: executor for %s produced a plan with no rollback plan", rec.Action)
	}
	hasSnapshot, hasMutation := false, false
	for _, step := range plan.Steps {
		hasSnapshot = hasSnapshot || step.Kind == execute.StepSnapshot
		hasMutation = hasMutation || step.Kind == execute.StepMutate
	}
	if hasMutation && !hasSnapshot {
		return execute.Plan{}, core.Invalid("automation: executor for %s produced a plan that mutates without a preceding snapshot step", rec.Action)
	}

	sp, err := s.loadActiveSpec(ctx, tenant)
	if err != nil {
		return execute.Plan{}, err
	}
	plan.Validation = buildValidationPlan(rec, res, sp)
	plan.RecommendationID = rec.ID
	plan.PolicyDecisionID = decision.ID
	plan.ScheduledFor = in.ScheduledFor
	plan.RequestedBy = in.RequestedBy
	plan.CreatedAt = now

	switch {
	case decision.Effect == govern.EffectAutoExecute:
		// A policy that reached AutoExecute has already authorised this
		// change; no human approval step is needed. See doc.go and the
		// governance package's own doc comment for why this branch is
		// currently unreachable through any policy that has passed
		// govern.Policy.Validate — it is implemented correctly regardless,
		// both because ProcessAutonomous depends on it and because the
		// unreachability is a defect to fix upstream, not a reason to leave
		// this path wrong.
		plan.State = execute.PlanApproved
	default:
		// RequireApproval and Advisory both stop short of unattended
		// execution: Advisory is guidance, not authorisation, and treating
		// it as anything less strict than RequireApproval here would let a
		// policy author's intent to merely flag something become an
		// accidental green light. A human must explicitly decide either way.
		approval, err := s.requestApprovalFor(ctx, tenant, rec, res, plan, decision, in.RequestedBy, now)
		if err != nil {
			return execute.Plan{}, fmt.Errorf("automation: requesting approval for plan: %w", err)
		}
		plan.ApprovalID = approval.ID
		plan.State = execute.PlanAwaitingApproval
	}

	if err := s.d.Executions.CreatePlan(ctx, plan); err != nil {
		return execute.Plan{}, fmt.Errorf("automation: persisting plan: %w", err)
	}

	s.touchSavings(ctx, tenant, rec, res, plan.ID, execute.StagePlanned, rec.EstimatedMonthlySaving,
		in.RequestedBy, "execution plan constructed with a rollback plan", now)

	rec.Status = optimize.StatusScheduled
	if plan.State == execute.PlanAwaitingApproval {
		rec.Status = optimize.StatusUnderReview
	}
	rec.UpdatedAt = now
	if err := s.d.Recommendations.Update(ctx, rec); err != nil {
		s.d.Logger.Warn("automation: updating recommendation status after planning failed", "recommendation", rec.ID, "error", err)
	}

	s.writeAudit(ctx, tenant, audit.ActionPlanCreated, audit.OutcomeSuccess, in.RequestedBy,
		plan.ID, rec.ID, plan.ApprovalID, decision.ID,
		fmt.Sprintf("execution plan for %q (%s) created, state=%s, %d steps, rollback feasible=%v",
			rec.Title, rec.Action, plan.State, len(plan.Steps), plan.Rollback.Feasible), nil, nil)

	return plan, nil
}

// requestApprovalFor builds the full govern.ApprovalContext a reviewer needs
// and routes it through governance, so the approval screen shows the actual
// rollback and validation plans this specific execution constructed rather
// than a generic description of the recommendation.
func (s *Service) requestApprovalFor(ctx context.Context, tenant core.TenantID, rec optimize.Recommendation, res cloud.Resource, plan execute.Plan, decision govern.Decision, actor string, now time.Time) (govern.Request, error) {
	rollbackSummary := "not reversible: " + plan.Rollback.InfeasibleReason
	if plan.Rollback.Feasible {
		rollbackSummary = plan.Rollback.Summary
	}
	affected := make([]string, 0, len(plan.ResourceIDs))
	for range plan.ResourceIDs {
		affected = append(affected, res.NativeID)
	}
	req := govern.Request{
		TenantID: tenant, SubjectKind: govern.SubjectExecutionPlan, SubjectID: plan.ID,
		Title:   rec.Title,
		Summary: fmt.Sprintf("%s on %s (%s)", rec.Action, res.NativeID, res.Environment),
		Context: govern.ApprovalContext{
			MonthlySaving: rec.EstimatedMonthlySaving, AnnualSaving: rec.EstimatedAnnualSaving,
			Confidence: rec.Confidence, RiskLevel: rec.Risk.Level, Environment: res.Environment,
			AffectedResources: affected, RollbackPlan: rollbackSummary,
			ValidationPlan: fmt.Sprintf("%d checks over a %s observation window", len(plan.Validation.Checks), plan.Validation.ObservationWindow),
			PolicyReason:   decision.Reason,
		},
		PolicyDecisionID: decision.ID, RequiredRoles: decision.Approvers,
		MinApprovals: decision.MinApprovals, RequireDistinctApprover: decision.RequireDistinctApprover,
		RequestedBy: actor, RequestedAt: now,
	}
	return s.d.Governance.RequestApproval(ctx, req)
}

// buildValidationPlan declares, before anything executes, how the change
// will be judged. Checks are conservative defaults derived from the
// recommendation's own risk assessment rather than the tenant's specific
// SLOs (this package has no general-purpose SLO evaluator of its own — that
// is econ.CostSLO's job, consulted separately by governance) — but every one
// of them is a real, computable comparison against data Validate actually
// gathers, not a placeholder.
func buildValidationPlan(rec optimize.Recommendation, res cloud.Resource, sp spec.Spec) execute.ValidationPlan {
	window := time.Duration(sp.Automation.ValidationWindowMinutes) * time.Minute
	if window <= 0 {
		window = defaultObservationWindow
	}
	checks := []execute.ValidationCheck{
		{
			Name: "cost_not_worse", Metric: "monthly_cost", Statistic: "avg",
			Comparison: "no_worse_than_pct", Threshold: 5, Critical: false,
			Reason: "the change should not increase this resource's cost by more than 5%",
		},
		{
			Name: "error_rate_bounded", Metric: "error_rate", Statistic: "p99",
			Comparison: "below_absolute", Threshold: 0.05, Critical: true,
			Reason: "the optimization must not push the error rate above 5%",
		},
	}
	if rec.Reversibility == optimize.ReversibilityNone || rec.Reversibility == optimize.ReversibilitySlow ||
		rec.Risk.Level == core.RiskHigh || rec.Risk.Level == core.RiskCritical {
		// A change that is hard or impossible to undo, or that the rule
		// itself flagged as risky, is watched more closely: capacity
		// headroom is checked too, not only that requests are erroring.
		checks = append(checks, execute.ValidationCheck{
			Name: "capacity_headroom", Metric: "cpu_utilization", Statistic: "p99",
			Comparison: "below_absolute", Threshold: 95, Critical: true,
			Reason: "a resize or downsize must not leave the resource saturated",
		})
	}
	autoRollbackOn := []string(nil)
	if sp.Automation.AutoRollback {
		for _, c := range checks {
			if c.Critical {
				autoRollbackOn = append(autoRollbackOn, c.Name)
			}
		}
	}
	return execute.ValidationPlan{
		ObservationWindow: window, BaselineWindow: window,
		Checks: checks, AutoRollbackOn: autoRollbackOn, MinSamples: defaultMinValidationSamples,
	}
}

// GetPlan returns one execution plan.
func (s *Service) GetPlan(ctx context.Context, tenant core.TenantID, planID core.ID) (execute.Plan, error) {
	return s.d.Executions.GetPlan(ctx, tenant, planID)
}

// ListPlans lists execution plans, optionally filtered by state.
func (s *Service) ListPlans(ctx context.Context, tenant core.TenantID, states []execute.PlanState, opts ports.ListOptions) (ports.Page[execute.Plan], error) {
	return s.d.Executions.ListPlans(ctx, tenant, states, opts)
}

// Cancel abandons a plan that has not started executing. It is refused once
// the plan has moved past PlanScheduled, because a plan that is executing,
// executed or validating has already touched (or is touching) AWS, and
// "cancel" is not a safe description of what Rollback would need to do.
func (s *Service) Cancel(ctx context.Context, tenant core.TenantID, planID core.ID, reason, actor string) error {
	now := s.d.Clock.Now()
	plan, err := s.d.Executions.GetPlan(ctx, tenant, planID)
	if err != nil {
		return err
	}
	switch plan.State {
	case execute.PlanDraft, execute.PlanAwaitingApproval, execute.PlanApproved, execute.PlanScheduled:
	default:
		return core.NewError(core.ErrPreconditionOff, "cannot_cancel",
			"plan %s is in state %s and can no longer be cancelled; use Rollback instead", planID, plan.State)
	}
	plan.State = execute.PlanCancelled
	plan.StateReason = reason
	if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
		return fmt.Errorf("automation: cancelling plan: %w", err)
	}
	s.loseSavings(ctx, tenant, plan.RecommendationID, "plan cancelled: "+reason, now)
	s.writeAudit(ctx, tenant, audit.ActionExecutionFailed, audit.OutcomeDenied, actor,
		plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
		fmt.Sprintf("plan %s cancelled: %s", plan.ID, reason), nil, nil)
	return nil
}
