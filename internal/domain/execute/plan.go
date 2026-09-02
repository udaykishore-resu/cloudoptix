// Package execute models the safe-change machinery: execution plans, state
// snapshots, rollback plans, post-change validation, and the savings lifecycle
// that tracks a recommendation from "potential" all the way to money that
// actually left the bill.
//
// The invariant the package exists to hold: nothing is executed that cannot be
// undone and verified. A plan is not executable until its rollback plan has
// been constructed and its precondition checks pass, and it is not finished
// until the validation window has closed with a verdict.
//
// Traceability: REQ-EXE-001..014, REQ-VAL-001..008, SPEC-AUTO-001.
package execute

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// PlanState is the execution plan lifecycle.
type PlanState string

const (
	PlanDraft            PlanState = "draft"
	PlanAwaitingApproval PlanState = "awaiting_approval"
	PlanApproved         PlanState = "approved"
	PlanScheduled        PlanState = "scheduled"
	PlanPreflight        PlanState = "preflight"
	PlanExecuting        PlanState = "executing"
	PlanExecuted         PlanState = "executed"
	PlanValidating       PlanState = "validating"
	PlanValidated        PlanState = "validated"
	PlanFailed           PlanState = "failed"
	PlanRollingBack      PlanState = "rolling_back"
	PlanRolledBack       PlanState = "rolled_back"
	PlanRollbackFailed   PlanState = "rollback_failed"
	PlanCancelled        PlanState = "cancelled"
)

// Terminal reports whether the plan has reached a final state.
func (s PlanState) Terminal() bool {
	switch s {
	case PlanValidated, PlanRolledBack, PlanCancelled, PlanRollbackFailed:
		return true
	}
	return false
}

// StepKind classifies what a step does, which decides whether a failure is
// recoverable by continuing or requires an immediate rollback.
type StepKind string

const (
	// StepPrecondition verifies an assumption still holds. Failing one aborts
	// the plan before anything has changed, which is always safe.
	StepPrecondition StepKind = "precondition"
	// StepSnapshot captures state needed for rollback. Failing one aborts:
	// CloudOptix will not make a change it cannot undo.
	StepSnapshot StepKind = "snapshot"
	// StepMutate changes AWS state.
	StepMutate StepKind = "mutate"
	// StepWait pauses for a resource to settle.
	StepWait StepKind = "wait"
	// StepVerify checks the mutation took effect as intended.
	StepVerify StepKind = "verify"
)

// StepState is a step's lifecycle.
type StepState string

const (
	StepPending    StepState = "pending"
	StepRunning    StepState = "running"
	StepSucceeded  StepState = "succeeded"
	StepFailed     StepState = "failed"
	StepSkipped    StepState = "skipped"
	StepRolledBack StepState = "rolled_back"
)

// Step is one unit of an execution plan.
type Step struct {
	ID       core.ID  `json:"id"`
	Ordinal  int      `json:"ordinal"`
	Kind     StepKind `json:"kind"`
	Name     string   `json:"name"`
	Describe string   `json:"describe"`

	// AWSAction is the exact API call, recorded before execution so that the
	// approval screen shows precisely what will run — not a paraphrase.
	AWSAction  string         `json:"aws_action,omitempty"` // ec2:ModifyInstanceAttribute
	Target     string         `json:"target,omitempty"`     // i-0abc123
	Parameters map[string]any `json:"parameters,omitempty"`

	// IdempotencyKey makes a retried step safe. Every mutating AWS call
	// CloudOptix issues carries one, because a network timeout on a resize
	// must not produce two resizes.
	IdempotencyKey string `json:"idempotency_key"`

	State      StepState      `json:"state"`
	Attempts   int            `json:"attempts"`
	MaxRetries int            `json:"max_retries"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Error      string         `json:"error,omitempty"`
	Output     map[string]any `json:"output,omitempty"`
	// AbortOnFailure marks steps whose failure stops the plan. Preconditions
	// and snapshots always abort; a verify step may be advisory.
	AbortOnFailure bool `json:"abort_on_failure"`
}

// Snapshot is the captured pre-change state used to build a rollback. It is
// not an AWS snapshot: it is CloudOptix's record of every attribute the plan
// is about to modify, plus references to any AWS-side backups it created.
type Snapshot struct {
	ID          core.ID       `json:"id"`
	TenantID    core.TenantID `json:"tenant_id"`
	PlanID      core.ID       `json:"plan_id"`
	ResourceID  core.ID       `json:"resource_id"`
	ResourceARN core.ARN      `json:"resource_arn,omitempty"`
	CapturedAt  time.Time     `json:"captured_at"`
	// Attributes is the full prior state of every field the plan touches.
	Attributes map[string]any `json:"attributes"`
	// BackupRefs point at AWS-side artefacts created for this change: an EBS
	// snapshot id, an RDS snapshot identifier, an S3 lifecycle configuration
	// document.
	BackupRefs map[string]string `json:"backup_refs,omitempty"`
	Digest     string            `json:"digest"`
}

// RollbackPlan is the reverse plan, constructed before the forward plan runs.
type RollbackPlan struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	PlanID   core.ID       `json:"plan_id"`
	Steps    []Step        `json:"steps"`
	// Feasible records whether a full rollback is actually possible. A plan
	// whose rollback is infeasible is not automatically blocked — deleting an
	// unattached volume is irreversible and still worth doing — but it is
	// escalated and the approval screen states it in those words.
	Feasible          bool           `json:"feasible"`
	InfeasibleReason  string         `json:"infeasible_reason,omitempty"`
	EstimatedDuration time.Duration  `json:"estimated_duration"`
	DataLossRisk      core.RiskLevel `json:"data_loss_risk"`
	Summary           string         `json:"summary"`
	CreatedAt         time.Time      `json:"created_at"`
}

// Plan is an approved, executable change.
type Plan struct {
	ID               core.ID             `json:"id"`
	TenantID         core.TenantID       `json:"tenant_id"`
	RecommendationID core.ID             `json:"recommendation_id"`
	Action           optimize.ActionType `json:"action"`
	Title            string              `json:"title"`

	AccountID   core.AccountID   `json:"account_id"`
	Region      core.Region      `json:"region"`
	Environment core.Environment `json:"environment"`
	ResourceIDs []core.ID        `json:"resource_ids"`

	Steps      []Step         `json:"steps"`
	Snapshots  []Snapshot     `json:"snapshots,omitempty"`
	Rollback   *RollbackPlan  `json:"rollback,omitempty"`
	Validation ValidationPlan `json:"validation"`

	ExpectedMonthlySaving core.Money `json:"expected_monthly_saving"`
	BaselineMonthlyCost   core.Money `json:"baseline_monthly_cost"`

	State            PlanState  `json:"state"`
	StateReason      string     `json:"state_reason,omitempty"`
	ApprovalID       core.ID    `json:"approval_id,omitempty"`
	PolicyDecisionID core.ID    `json:"policy_decision_id,omitempty"`
	ScheduledFor     *time.Time `json:"scheduled_for,omitempty"`
	// DryRun executes every precondition and snapshot step and replaces each
	// mutating call with its AWS dry-run equivalent. It is how a tenant proves
	// the execute role works before trusting it with a real change.
	DryRun bool `json:"dry_run"`

	RequestedBy string     `json:"requested_by"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// Executable reports whether the plan may be started, and why not when it
// cannot. This is checked immediately before execution, not only at approval
// time, because approvals can be hours old.
func (p Plan) Executable(now time.Time) (bool, string) {
	switch p.State {
	case PlanApproved, PlanScheduled:
	default:
		return false, fmt.Sprintf("plan is in state %s", p.State)
	}
	if p.Rollback == nil {
		return false, "no rollback plan has been constructed"
	}
	if len(p.Steps) == 0 {
		return false, "plan has no steps"
	}
	if p.ScheduledFor != nil && now.Before(*p.ScheduledFor) {
		return false, fmt.Sprintf("scheduled for %s", p.ScheduledFor.Format(time.RFC3339))
	}
	hasSnapshot := false
	hasMutation := false
	for _, s := range p.Steps {
		if s.Kind == StepSnapshot {
			hasSnapshot = true
		}
		if s.Kind == StepMutate {
			hasMutation = true
		}
	}
	if hasMutation && !hasSnapshot {
		return false, "plan mutates state without capturing a snapshot first"
	}
	return true, ""
}

// Progress reports completed and total step counts.
func (p Plan) Progress() (done, total int) {
	for _, s := range p.Steps {
		total++
		if s.State == StepSucceeded || s.State == StepSkipped {
			done++
		}
	}
	return done, total
}

// ValidationPlan declares how the change will be judged after it lands. It is
// written before execution so the success criteria cannot be adjusted to fit
// the outcome.
type ValidationPlan struct {
	// ObservationWindow is how long CloudOptix watches before deciding.
	ObservationWindow time.Duration `json:"observation_window"`
	// BaselineWindow is the comparable period before the change.
	BaselineWindow time.Duration     `json:"baseline_window"`
	Checks         []ValidationCheck `json:"checks"`
	// AutoRollbackOn lists the check names whose failure triggers an
	// immediate automatic rollback rather than an alert.
	AutoRollbackOn []string `json:"auto_rollback_on,omitempty"`
	// MinSamples guards against declaring success on a quiet weekend.
	MinSamples int `json:"min_samples"`
}

// ValidationCheck is one post-change assertion.
type ValidationCheck struct {
	Name      string `json:"name"`
	Metric    string `json:"metric"`    // cpu_utilization, p99_latency_ms, error_rate, monthly_cost
	Statistic string `json:"statistic"` // p95, p99, avg, max, sum
	// Comparison is against the baseline: "no_worse_than_pct", "below_absolute",
	// "above_absolute", "improved_by_pct".
	Comparison string  `json:"comparison"`
	Threshold  float64 `json:"threshold"`
	Critical   bool    `json:"critical"`
	Reason     string  `json:"reason"`
}

// Verdict is the outcome of post-change validation.
type Verdict string

const (
	VerdictSuccess        Verdict = "success"
	VerdictPartialSuccess Verdict = "partial_success"
	VerdictFailure        Verdict = "failure"
	VerdictInconclusive   Verdict = "inconclusive"
)

// ValidationResult is the outcome of the observation window.
type ValidationResult struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	PlanID   core.ID       `json:"plan_id"`

	Verdict     Verdict `json:"verdict"`
	Explanation string  `json:"explanation"`

	BaselineWindow core.Period `json:"baseline_window"`
	ObservedWindow core.Period `json:"observed_window"`

	Checks []CheckOutcome `json:"checks"`

	// Realized savings are measured from billing after the change, not
	// assumed from the prediction. The gap between predicted and realized is
	// the single most valuable signal the learning loop consumes.
	PredictedMonthlySaving core.Money `json:"predicted_monthly_saving"`
	ObservedMonthlySaving  core.Money `json:"observed_monthly_saving"`
	SavingAccuracy         float64    `json:"saving_accuracy"`

	// UnattributedDelta is the part of the measured reduction that exceeded
	// what this change could credibly have caused, and AttributionNote says
	// why. Both are reported rather than dropped: a large unattributed delta
	// means something else moved in the window and is worth investigating.
	UnattributedDelta core.Money `json:"unattributed_delta,omitempty"`
	AttributionNote   string     `json:"attribution_note,omitempty"`

	RollbackTriggered bool      `json:"rollback_triggered"`
	RollbackReason    string    `json:"rollback_reason,omitempty"`
	EvaluatedAt       time.Time `json:"evaluated_at"`
}

// CheckOutcome is one validation check's result.
type CheckOutcome struct {
	Name      string  `json:"name"`
	Metric    string  `json:"metric"`
	Baseline  float64 `json:"baseline"`
	Observed  float64 `json:"observed"`
	Threshold float64 `json:"threshold"`
	ChangePct float64 `json:"change_pct"`
	Passed    bool    `json:"passed"`
	Critical  bool    `json:"critical"`
	Samples   int     `json:"samples"`
	Detail    string  `json:"detail"`
}

// Decide computes the overall verdict from the check outcomes.
//
// The asymmetry here is intentional: a failed critical check is a failure even
// if the money was saved, because CloudOptix's promise is that optimization
// does not degrade the service. Saving money while breaking latency is not a
// partial success.
func (r *ValidationResult) Decide(minSamples int) {
	if len(r.Checks) == 0 {
		r.Verdict = VerdictInconclusive
		r.Explanation = "no validation checks were configured"
		return
	}
	criticalFailed, nonCriticalFailed, insufficient := 0, 0, 0
	for _, c := range r.Checks {
		if c.Samples < minSamples {
			insufficient++
			continue
		}
		if !c.Passed {
			if c.Critical {
				criticalFailed++
			} else {
				nonCriticalFailed++
			}
		}
	}
	switch {
	case criticalFailed > 0:
		r.Verdict = VerdictFailure
		r.Explanation = fmt.Sprintf("%d critical check(s) failed after the change", criticalFailed)
	case insufficient == len(r.Checks):
		r.Verdict = VerdictInconclusive
		r.Explanation = "insufficient telemetry in the observation window to judge the change"
	case nonCriticalFailed > 0:
		r.Verdict = VerdictPartialSuccess
		r.Explanation = fmt.Sprintf("%d non-critical check(s) regressed; no critical check failed", nonCriticalFailed)
	default:
		r.Verdict = VerdictSuccess
		r.Explanation = "all validation checks passed and the change is holding"
	}
	if !r.PredictedMonthlySaving.IsZero() {
		r.SavingAccuracy = r.ObservedMonthlySaving.Ratio(r.PredictedMonthlySaving)
	}
}

// FavourableVarianceTolerance bounds how much better than predicted a change
// may be credited for. A change landing 25% better than modelled is ordinary
// estimation variance; a change appearing to save twice what it touched is
// almost always something else moving at the same time.
const FavourableVarianceTolerance = 1.25

// AttributableSaving splits a measured cost reduction into the part this
// change may be credited with and the part that must stay unattributed.
//
// The measurement itself is never adjusted — ObservedMonthlySaving records what
// the bill actually did, because that is a fact and the learning loop needs it
// unfiltered. What is bounded is *attribution*: crediting an optimization with
// every dollar that happened to leave the bill during its observation window is
// how a platform ends up claiming savings it did not cause. A concurrent
// change, a traffic trough or a rightsizing someone did by hand all land in the
// same window, and a tool that scoops them all into its own realized-savings
// figure is committing exactly the inflation CloudOptix exists to correct.
//
// Anything above the tolerance band is returned as the unattributed remainder
// with a stated reason, so the number is visible rather than quietly dropped.
//
// Traceability: REQ-SAV-005, SPEC-AUTO-005.
func AttributableSaving(predicted, measured core.Money) (attributed, unattributed core.Money, reason string) {
	if measured.IsNegative() {
		// The bill went up. Attribute nothing; the validation verdict, not
		// this function, is what reports that as a problem.
		return core.ZeroUSD(), core.ZeroUSD(), ""
	}
	if predicted.IsZero() {
		return measured, core.ZeroUSD(), ""
	}
	ceiling := predicted.Scale(FavourableVarianceTolerance)
	if !measured.GreaterThan(ceiling) {
		return measured, core.ZeroUSD(), ""
	}
	excess := measured.MustSub(ceiling)
	return ceiling, excess, fmt.Sprintf(
		"measured reduction of %s exceeds the %s predicted by more than the %.0f%% variance tolerance; "+
			"%s is left unattributed pending corroboration that this change caused it",
		measured.Format(), predicted.Format(), (FavourableVarianceTolerance-1)*100, excess.Format())
}
