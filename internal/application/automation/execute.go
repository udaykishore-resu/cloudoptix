package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const (
	// planLockTTL bounds how long one worker can hold the execution lock for
	// a plan. It must exceed planExecutionDeadline: a lock that expired
	// mid-execution would let a second worker start executing the same plan
	// concurrently, which is exactly what Locker exists to prevent.
	planLockTTL = 40 * time.Minute
	// planExecutionDeadline is the whole-plan budget. A plan whose steps have
	// not all finished within this window is abandoned mid-flight and rolled
	// back rather than left to run indefinitely against a customer account.
	planExecutionDeadline = 30 * time.Minute
	// stepTimeout bounds one Apply/Rollback call. A single AWS call hanging
	// must not be able to consume the entire plan deadline by itself.
	stepTimeout = 2 * time.Minute

	defaultStepMaxRetries = 3
	retryBaseDelay        = 500 * time.Millisecond
	retryMaxDelay         = 10 * time.Second
)

// Execute runs an approved plan step by step. It re-evaluates governance and
// the plan's approval state immediately before the first AWS call — not only
// trusting whatever state PlanExecution left behind — because an approval
// can expire, a maintenance window can close and an economic error budget
// can freeze in the time between approval and execution. Any step whose
// failure is marked AbortOnFailure (preconditions, snapshots and mutations
// always are) triggers an immediate rollback of everything that already
// landed; Execute never leaves a plan half-applied without attempting to
// undo it.
func (s *Service) Execute(ctx context.Context, tenant core.TenantID, planID core.ID, actor string) (execute.Plan, error) {
	now := s.d.Clock.Now()

	plan, err := s.d.Executions.GetPlan(ctx, tenant, planID)
	if err != nil {
		return execute.Plan{}, fmt.Errorf("automation: loading plan %s: %w", planID, err)
	}

	// Authorization is re-checked before the state-machine gate, not after:
	// a plan PlanExecution left in PlanAwaitingApproval is only promoted to
	// PlanApproved here, at the moment Execute confirms the attached
	// approval has actually been granted (or the fresh governance decision
	// is now AutoExecute outright). Nothing else in this package flips that
	// state on an approval being decided elsewhere — Execute is the single
	// place authorization and execution eligibility are reconciled, exactly
	// as ports.AutomationService.Execute's own doc comment describes.
	if err := s.reconfirmAuthorization(ctx, tenant, plan); err != nil {
		plan.State = execute.PlanFailed
		plan.StateReason = "authorization re-check failed immediately before execution: " + err.Error()
		if uerr := s.d.Executions.UpdatePlan(ctx, plan); uerr != nil {
			s.d.Logger.Warn("automation: persisting plan after authorization re-check failure", "plan", plan.ID, "error", uerr)
		}
		s.writeAudit(ctx, tenant, audit.ActionExecutionFailed, audit.OutcomeDenied, actor,
			plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
			"execution refused: "+err.Error(), nil, nil)
		return plan, err
	}
	if plan.State == execute.PlanAwaitingApproval {
		plan.State = execute.PlanApproved
		if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
			return plan, fmt.Errorf("automation: persisting plan after approval confirmation: %w", err)
		}
	}

	if ok, reason := plan.Executable(now); !ok {
		return plan, core.NewError(core.ErrPreconditionOff, "not_executable", "plan %s is not executable: %s", planID, reason)
	}

	release, err := s.d.Locker.Acquire(ctx, lockKeyForPlan(tenant, planID), planLockTTL)
	if err != nil {
		return plan, fmt.Errorf("automation: acquiring execution lock for plan %s: %w", planID, err)
	}
	defer release()

	// Re-load under the lock: another worker may have raced us up to the
	// point of acquiring it (e.g. two API calls to Execute the same plan
	// arriving together — the lock serializes from here on, but the state
	// read before it was taken could already be stale).
	plan, err = s.d.Executions.GetPlan(ctx, tenant, planID)
	if err != nil {
		return execute.Plan{}, fmt.Errorf("automation: reloading plan %s under lock: %w", planID, err)
	}
	if ok, reason := plan.Executable(now); !ok {
		return plan, core.NewError(core.ErrPreconditionOff, "not_executable", "plan %s is not executable: %s", planID, reason)
	}

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

	if err := executor.Preflight(ctx, session, plan); err != nil {
		return s.failPlan(ctx, tenant, plan, rec, actor, fmt.Sprintf("preflight failed: %v", err), now)
	}

	plan.State = execute.PlanExecuting
	startedAt := now
	plan.StartedAt = &startedAt
	if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
		return plan, fmt.Errorf("automation: persisting plan before execution: %w", err)
	}
	s.writeAudit(ctx, tenant, audit.ActionExecutionStarted, audit.OutcomeSuccess, actor,
		plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
		fmt.Sprintf("execution started for plan %s (%s), %d steps", plan.ID, plan.Action, len(plan.Steps)), nil, nil)

	// A wall-clock timeout, not Clock.Now()+deadline. The injected clock
	// exists so time-dependent *outputs* — savings windows, error budgets,
	// forecast horizons — are reproducible; context deadlines are measured
	// by the runtime against real time regardless of what any clock says. A
	// deadline built from a fixed test clock is already in the past the
	// moment real time passes it, and every execution then fails instantly
	// with "context deadline exceeded" — a failure that appears out of
	// nowhere hours after the code last worked, which is the worst kind.
	stepCtx, cancel := context.WithTimeout(ctx, planExecutionDeadline)
	defer cancel()

	for i := range plan.Steps {
		step := &plan.Steps[i]
		if step.State == execute.StepSucceeded || step.State == execute.StepSkipped {
			continue
		}
		step.State = execute.StepRunning
		stepStarted := s.d.Clock.Now()
		step.StartedAt = &stepStarted

		out, applyErr := s.applyStepWithRetry(stepCtx, executor, session, plan, step)
		stepFinished := s.d.Clock.Now()
		step.FinishedAt = &stepFinished

		if applyErr != nil {
			step.State = execute.StepFailed
			step.Error = applyErr.Error()
			if uerr := s.d.Executions.UpdatePlan(ctx, plan); uerr != nil {
				s.d.Logger.Warn("automation: persisting plan after step failure", "plan", plan.ID, "step", step.Name, "error", uerr)
			}
			s.writeAudit(ctx, tenant, audit.ActionExecutionStep, audit.OutcomeFailure, actor,
				plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
				fmt.Sprintf("step %s (%s) failed after %d attempt(s): %v", step.Name, step.Kind, step.Attempts, applyErr), nil, nil)

			if step.AbortOnFailure {
				return s.failAndRollback(ctx, tenant, plan, rec, res, account, executor, actor,
					fmt.Sprintf("step %q failed: %v", step.Name, applyErr), now)
			}
			// A non-aborting step (an advisory verify) failing is recorded
			// and the plan proceeds — the mutation already landed; refusing
			// to finish the plan over a verify step that could not confirm
			// it would not undo anything, only hide that it happened.
			continue
		}

		step.State = execute.StepSucceeded
		step.Output = out
		if uerr := s.d.Executions.UpdatePlan(ctx, plan); uerr != nil {
			s.d.Logger.Warn("automation: persisting plan after step success", "plan", plan.ID, "step", step.Name, "error", uerr)
		}
		s.writeAudit(ctx, tenant, audit.ActionExecutionStep, audit.OutcomeSuccess, actor,
			plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
			fmt.Sprintf("step %s (%s) succeeded after %d attempt(s)", step.Name, step.Kind, step.Attempts), nil, out)

		if step.Kind == execute.StepSnapshot {
			s.persistSnapshots(ctx, plan)
		}
	}

	plan.State = execute.PlanExecuted
	finishedAt := s.d.Clock.Now()
	plan.FinishedAt = &finishedAt
	if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
		return plan, fmt.Errorf("automation: persisting completed plan: %w", err)
	}

	s.touchSavings(ctx, tenant, rec, res, plan.ID, execute.StageExecuted, rec.EstimatedMonthlySaving,
		actor, "AWS mutation succeeded", finishedAt)

	rec.Status = optimize.StatusExecuted
	rec.UpdatedAt = finishedAt
	if err := s.d.Recommendations.Update(ctx, rec); err != nil {
		s.d.Logger.Warn("automation: updating recommendation status after execution", "recommendation", rec.ID, "error", err)
	}

	s.writeAudit(ctx, tenant, audit.ActionExecutionSucceeded, audit.OutcomeSuccess, actor,
		plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID,
		fmt.Sprintf("plan %s (%s) executed successfully", plan.ID, plan.Action), nil, nil)
	s.publish(ctx, ports.Event{
		Type: ports.EventOptimizationExecuted, TenantID: tenant, SubjectID: plan.ID,
		CorrelationID: string(plan.RecommendationID), Actor: actorLabel(actor),
		Payload: map[string]any{"plan_id": plan.ID, "action": plan.Action, "monthly_saving": rec.EstimatedMonthlySaving.Units()},
	})

	return plan, nil
}

// reconfirmAuthorization re-runs governance and, when the fresh decision
// still demands a human approval, checks that the approval attached to this
// plan is actually in the approved state. It is deliberately stricter than
// trusting plan.State == PlanApproved: that state was set once, possibly
// hours ago, and this is the platform's last chance to notice that the
// world changed before an AWS API call that cannot be un-sent.
func (s *Service) reconfirmAuthorization(ctx context.Context, tenant core.TenantID, plan execute.Plan) error {
	decision, err := s.d.Governance.Evaluate(ctx, tenant, plan.RecommendationID)
	if err != nil {
		return fmt.Errorf("re-evaluating governance: %w", err)
	}
	if decision.Effect == govern.EffectProhibit {
		return core.NewError(core.ErrForbidden, "change_prohibited", "governance now prohibits this change: %s", decision.Reason)
	}
	if decision.Effect == govern.EffectAutoExecute {
		return nil
	}
	if plan.ApprovalID.IsZero() {
		return core.NewError(core.ErrPreconditionOff, "no_approval", "governance requires approval but the plan has no approval request attached")
	}
	appr, err := s.d.Approvals.Get(ctx, tenant, plan.ApprovalID)
	if err != nil {
		return fmt.Errorf("loading approval %s: %w", plan.ApprovalID, err)
	}
	if appr.State != govern.ApprovalApproved {
		return core.NewError(core.ErrPreconditionOff, "not_approved", "approval %s is %s, not approved", appr.ID, appr.State)
	}
	return nil
}

// persistSnapshots saves the plan's pre-change snapshots once its snapshot
// step has succeeded. They were captured by the executor at Plan time (see
// awssim's genericExecutor.Plan), not re-captured here, which is what lets
// Rollback restore exactly the state the plan was built against even if the
// live resource has drifted further since.
func (s *Service) persistSnapshots(ctx context.Context, plan execute.Plan) {
	for _, snap := range plan.Snapshots {
		if err := s.d.Executions.SaveSnapshot(ctx, snap); err != nil {
			s.d.Logger.Warn("automation: persisting snapshot failed", "plan", plan.ID, "resource", snap.ResourceID, "error", err)
		}
	}
}

// applyStepWithRetry calls Apply with exponential backoff on retryable
// errors, up to step.MaxRetries (defaulted when unset). Every attempt
// carries the exact same step.IdempotencyKey and step.Parameters — retrying
// is safe only because the executor contract requires Apply to be
// idempotent on that key, not because this loop trusts the network.
func (s *Service) applyStepWithRetry(ctx context.Context, executor ports.Executor, session ports.AWSSession, plan execute.Plan, step *execute.Step) (map[string]any, error) {
	if step.MaxRetries <= 0 {
		step.MaxRetries = defaultStepMaxRetries
	}
	var lastErr error
	for attempt := 0; attempt <= step.MaxRetries; attempt++ {
		step.Attempts++
		callCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		out, err := executor.Apply(callCtx, session, plan, *step)
		cancel()
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !core.Retryable(err) || attempt == step.MaxRetries {
			return nil, lastErr
		}
		if werr := s.wait(ctx, backoffDuration(attempt)); werr != nil {
			return nil, werr
		}
	}
	return nil, lastErr
}

// applyRollbackStepWithRetry mirrors applyStepWithRetry for the reverse
// direction. Rollback steps get the same retry rigor as forward steps: a
// throttled DescribeInstances during rollback is no more a reason to give up
// on undoing a change than it would be to give up on making one.
func (s *Service) applyRollbackStepWithRetry(ctx context.Context, executor ports.Executor, session ports.AWSSession, plan execute.Plan, step *execute.Step) error {
	if step.MaxRetries <= 0 {
		step.MaxRetries = defaultStepMaxRetries
	}
	var lastErr error
	for attempt := 0; attempt <= step.MaxRetries; attempt++ {
		step.Attempts++
		callCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := executor.Rollback(callCtx, session, plan, *step)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !core.Retryable(err) || attempt == step.MaxRetries {
			return lastErr
		}
		if werr := s.wait(ctx, backoffDuration(attempt)); werr != nil {
			return werr
		}
	}
	return lastErr
}

func (s *Service) wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backoffDuration is a plain exponential backoff, capped, with no jitter.
// Jitter matters most when many independent workers might retry the same
// throttled endpoint at once; a single execution plan's step retries are
// serialized by the plan lock already, so the cap alone is enough to keep a
// retried step from ever looking like a tight loop against AWS.
func backoffDuration(attempt int) time.Duration {
	d := retryBaseDelay
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= retryMaxDelay {
			return retryMaxDelay
		}
	}
	return d
}

// failPlan marks a plan failed without attempting a rollback — used when
// nothing has mutated yet (a Preflight failure precedes every mutation),
// so there is nothing to undo.
func (s *Service) failPlan(ctx context.Context, tenant core.TenantID, plan execute.Plan, rec optimize.Recommendation, actor, reason string, now time.Time) (execute.Plan, error) {
	plan.State = execute.PlanFailed
	plan.StateReason = reason
	if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
		s.d.Logger.Warn("automation: persisting failed plan", "plan", plan.ID, "error", err)
	}
	s.loseSavings(ctx, tenant, plan.RecommendationID, reason, now)
	s.writeAudit(ctx, tenant, audit.ActionExecutionFailed, audit.OutcomeFailure, actor,
		plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID, reason, nil, nil)
	return plan, errors.New("automation: " + reason)
}

// failAndRollback is the mid-plan-failure path: at least one mutating (or
// snapshot, or precondition) step failed after exhausting its retries, so
// everything the plan already did must be undone with the same rigor it was
// applied with. It is not exported — the only way a plan reaches this path
// from the outside is by having Execute fail partway through; a human
// invoking Rollback later on an already-PlanFailed plan is a different,
// exported path (see rollback.go) with its own state checks.
func (s *Service) failAndRollback(ctx context.Context, tenant core.TenantID, plan execute.Plan, rec optimize.Recommendation, res cloud.Resource, account cloud.AWSAccount, executor ports.Executor, actor, reason string, now time.Time) (execute.Plan, error) {
	plan.State = execute.PlanFailed
	plan.StateReason = reason
	if err := s.d.Executions.UpdatePlan(ctx, plan); err != nil {
		s.d.Logger.Warn("automation: persisting failed plan before rollback", "plan", plan.ID, "error", err)
	}
	s.writeAudit(ctx, tenant, audit.ActionExecutionFailed, audit.OutcomeFailure, actor,
		plan.ID, plan.RecommendationID, plan.ApprovalID, plan.PolicyDecisionID, reason, nil, nil)

	session, err := s.d.Credentials.Assume(ctx, account, cloud.ScopeExecute)
	if err != nil {
		// Cannot even get a session to attempt the rollback: this is exactly
		// the "stop, do not retry blindly, escalate" case — the plan stays
		// PlanFailed (not PlanRollbackFailed, since no rollback was even
		// attempted) and the caller sees both the original failure and this
		// one.
		s.loseSavings(ctx, tenant, plan.RecommendationID, reason+"; rollback could not be attempted: "+err.Error(), now)
		return plan, fmt.Errorf("automation: %s (rollback could not be attempted: %w)", reason, err)
	}

	rolledBack, rbErr := s.runRollback(ctx, tenant, plan, rec, executor, session, actor,
		fmt.Sprintf("automatic rollback after execution failure: %s", reason), now)
	if rbErr != nil {
		return rolledBack, fmt.Errorf("automation: %s; %w", reason, rbErr)
	}
	return rolledBack, fmt.Errorf("automation: %s (rolled back successfully)", reason)
}
