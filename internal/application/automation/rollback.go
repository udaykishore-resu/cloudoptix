package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Rollback reverses an executed plan on demand — a human decided after the
// fact that a change should be undone, independent of whether Validate ever
// ran or what it said. It is refused for a plan whose rollback was already
// attempted and failed (PlanRollbackFailed is terminal: see doc.go and
// runRollback — a failed rollback needs a human working directly against
// AWS, not another automatic attempt) and for a plan that never executed or
// was already rolled back, because "reverse" only means something for a
// change that actually landed.
func (s *Service) Rollback(ctx context.Context, tenant core.TenantID, planID core.ID, reason, actor string) (execute.Plan, error) {
	now := s.d.Clock.Now()
	plan, err := s.d.Executions.GetPlan(ctx, tenant, planID)
	if err != nil {
		return execute.Plan{}, fmt.Errorf("automation: loading plan %s: %w", planID, err)
	}
	switch plan.State {
	case execute.PlanExecuted, execute.PlanValidating, execute.PlanValidated, execute.PlanFailed:
	default:
		return plan, core.NewError(core.ErrPreconditionOff, "cannot_rollback",
			"plan %s is in state %s and cannot be rolled back through this call", planID, plan.State)
	}
	if plan.Rollback == nil || !plan.Rollback.Feasible {
		reasonText := "no rollback plan was recorded"
		if plan.Rollback != nil {
			reasonText = plan.Rollback.InfeasibleReason
		}
		return plan, core.NewError(core.ErrPreconditionOff, "rollback_infeasible",
			"plan %s has no feasible rollback: %s", planID, reasonText)
	}

	release, err := s.d.Locker.Acquire(ctx, lockKeyForPlan(tenant, planID), planLockTTL)
	if err != nil {
		return plan, fmt.Errorf("automation: acquiring rollback lock for plan %s: %w", planID, err)
	}
	defer release()

	rec, err := s.d.Recommendations.Get(ctx, tenant, plan.RecommendationID)
	if err != nil {
		return plan, fmt.Errorf("automation: loading recommendation %s: %w", plan.RecommendationID, err)
	}
	res, err := s.d.Resources.Get(ctx, tenant, rec.Finding.ResourceID)
	if err != nil {
		return plan, fmt.Errorf("automation: loading resource %s: %w", rec.Finding.ResourceID, err)
	}
	account, err := s.resolveAccount(ctx, tenant, res.AccountID)
	if err != nil {
		return plan, err
	}
	executor, err := s.executorFor(plan.Action)
	if err != nil {
		return plan, err
	}
	session, err := s.d.Credentials.Assume(ctx, account, cloud.ScopeExecute)
	if err != nil {
		return plan, fmt.Errorf("automation: assuming execute-scoped session for account %s: %w", account.AccountID, err)
	}

	return s.runRollback(ctx, tenant, plan, rec, executor, session, actor, reason, now)
}

// runRollback executes the reverse plan step by step, with the same retry
// rigor as forward execution. It never calls itself again on failure — a
// step that exhausts its own retries and still fails ends the whole attempt
// as PlanRollbackFailed rather than looping the reverse plan from the top,
// because retrying a rollback blindly risks the exact same failure mode
// (an action half-applied, half-reversed) that made a careful rollback
// necessary in the first place. The remaining steps still run: a rollback
// with three independent steps where one fails is better reported as "two
// undone, one needs a human" than abandoned entirely after the first
// failure.
func (s *Service) runRollback(ctx context.Context, tenant core.TenantID, plan execute.Plan, rec optimize.Recommendation, executor ports.Executor, session ports.AWSSession, actor, reason string, now time.Time) (execute.Plan, error) {
	plan.State = execute.PlanRollingBack
	plan.StateReason = reason
	if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
		s.d.Logger.Warn("automation: persisting plan before rollback", "plan", plan.ID, "error", err)
	}
	s.writeAudit(ctx, tenant, audit.ActionRollbackStarted, audit.OutcomeSuccess, actor,
		plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
		fmt.Sprintf("rollback started for plan %s: %s", plan.ID, reason), nil, nil)

	var failedSteps []string
	for i := range plan.Rollback.Steps {
		step := &plan.Rollback.Steps[i]
		step.State = execute.StepRunning
		started := s.d.Clock.Now()
		step.StartedAt = &started

		err := s.applyRollbackStepWithRetry(ctx, executor, session, plan, step)
		finished := s.d.Clock.Now()
		step.FinishedAt = &finished

		if err != nil {
			step.State = execute.StepFailed
			step.Error = err.Error()
			failedSteps = append(failedSteps, step.Name)
			s.writeAudit(ctx, tenant, audit.ActionRollbackFailed, audit.OutcomeFailure, actor,
				plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
				fmt.Sprintf("rollback step %s failed after %d attempt(s): %v", step.Name, step.Attempts, err), nil, nil)
			continue
		}
		step.State = execute.StepRolledBack
		s.writeAudit(ctx, tenant, audit.ActionExecutionStep, audit.OutcomeSuccess, actor,
			plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
			fmt.Sprintf("rollback step %s succeeded after %d attempt(s)", step.Name, step.Attempts), nil, nil)
	}

	finishedAt := s.d.Clock.Now()
	if len(failedSteps) == 0 {
		plan.State = execute.PlanRolledBack
		plan.FinishedAt = &finishedAt
		if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
			return plan, fmt.Errorf("automation: persisting rolled-back plan: %w", err)
		}
		s.loseSavings(ctx, tenant, plan.RecommendationID, "rolled back: "+reason, finishedAt)
		rec.Status = optimize.StatusRolledBack
		rec.UpdatedAt = finishedAt
		if err := s.d.Recommendations.Update(ctx, rec); err != nil {
			s.d.Logger.Warn("automation: updating recommendation status after rollback", "recommendation", rec.ID, "error", err)
		}
		s.writeAudit(ctx, tenant, audit.ActionRollbackCompleted, audit.OutcomeSuccess, actor,
			plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
			fmt.Sprintf("plan %s fully rolled back: %s", plan.ID, reason), nil, nil)
		s.publish(ctx, ports.Event{
			Type: ports.EventOptimizationRolledBack, TenantID: tenant, SubjectID: plan.ID,
			CorrelationID: string(plan.RecommendationID), Actor: actorLabel(actor),
			Payload: map[string]any{"plan_id": plan.ID, "success": true, "reason": reason},
		})
		return plan, nil
	}

	// One or more rollback steps failed: stop here. This plan needs a human
	// looking directly at the AWS account, not another automatic attempt —
	// see the function doc comment and doc.go.
	plan.State = execute.PlanRollbackFailed
	plan.FinishedAt = &finishedAt
	if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
		s.d.Logger.Warn("automation: persisting rollback-failed plan", "plan", plan.ID, "error", err)
	}
	s.writeAudit(ctx, tenant, audit.ActionRollbackFailed, audit.OutcomeFailure, actor,
		plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
		fmt.Sprintf("plan %s rollback FAILED on step(s) %v — manual remediation required in AWS account %s", plan.ID, failedSteps, plan.AccountID), nil, nil)
	s.publish(ctx, ports.Event{
		Type: ports.EventOptimizationRolledBack, TenantID: tenant, SubjectID: plan.ID,
		CorrelationID: string(plan.RecommendationID), Actor: actorLabel(actor),
		// success:false plus critical:true is how the notify dispatcher
		// (internal/adapters/notify) distinguishes a routine rollback
		// confirmation from the page-someone-now case: a rollback that
		// itself failed leaves a customer's account in a state only a human
		// can now fix.
		Payload: map[string]any{"plan_id": plan.ID, "success": false, "critical": true, "failed_steps": failedSteps},
	})
	return plan, core.NewError(core.ErrUnavailable, "rollback_failed",
		"rollback of plan %s failed on step(s) %v; manual intervention is required", plan.ID, failedSteps)
}
