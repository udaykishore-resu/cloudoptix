package automation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Learner is the seam between this package and the calibration/outcome loop
// in internal/application/learning. It is declared here, not imported from
// there, so automation never depends on learning's package (or vice versa —
// neither application package needs to know the other exists): a composition
// root that wants the richer learning.Service wires it in because
// learning.Service happens to implement these two methods, and a composition
// root that does not care about RAG-corpus feedback can leave it nil, in
// which case Learn falls back to running execute.Calibrate directly against
// ports.SavingsRepository, which is already a required dependency of this
// service. Either way Learn always does something real; the optional
// dependency only decides whether outcomes also get written back to the
// knowledge base.
type Learner interface {
	Recalibrate(ctx context.Context, tenant core.TenantID) (ports.LearningResult, error)
}

// Deps is every dependency Service needs, expressed as ports. Repositories
// covering recommendations, resources, accounts, policy/approval state,
// specifications, executions and savings are required: every method here
// touches several of them, and a service missing one would fail confusingly
// deep inside a call rather than at construction. Credentials, Executors,
// Locker and Governance are likewise required — this package's entire
// purpose is to broker AWS access through a credential broker, hand off to a
// per-action executor, hold a distributed lock while it does, and never act
// without an up-to-date governance decision. Metrics, Costs, Events and
// Learner are optional: their absence narrows what a call can measure or
// announce (Validate degrades to whatever checks do not need the missing
// source; ProcessAutonomous still runs without publishing events) rather
// than refusing to run at all.
type Deps struct {
	Executions      ports.ExecutionRepository
	Recommendations ports.RecommendationRepository
	Resources       ports.ResourceRepository
	AWSAccounts     ports.AWSAccountRepository
	Policies        ports.PolicyRepository
	Approvals       ports.ApprovalRepository
	Savings         ports.SavingsRepository
	Specs           ports.SpecRepository
	Audit           ports.AuditRepository

	Metrics ports.MetricRepository // optional
	Costs   ports.CostRepository   // optional

	Credentials ports.AWSCredentialBroker
	// Executors maps each action type to the executor that performs it. A
	// recommendation whose action has no entry fails PlanExecution closed
	// with a clear "no executor registered" error rather than silently
	// falling back to some default behaviour — there is no safe default for
	// "mutate a cloud resource".
	Executors  map[optimize.ActionType]ports.Executor
	Locker     ports.Locker
	Governance ports.GovernanceService

	Events  ports.EventPublisher // optional
	Learner Learner              // optional

	Clock  core.Clock
	Logger *slog.Logger
}

// Service implements ports.AutomationService.
type Service struct{ d Deps }

var _ ports.AutomationService = (*Service)(nil)

// NewService validates the required dependencies and fills in defaults for
// the optional ones.
func NewService(d Deps) (*Service, error) {
	var missing []string
	req := map[string]bool{
		"Executions": d.Executions == nil, "Recommendations": d.Recommendations == nil,
		"Resources": d.Resources == nil, "AWSAccounts": d.AWSAccounts == nil,
		"Policies": d.Policies == nil, "Approvals": d.Approvals == nil,
		"Savings": d.Savings == nil, "Specs": d.Specs == nil, "Audit": d.Audit == nil,
		"Credentials": d.Credentials == nil, "Locker": d.Locker == nil, "Governance": d.Governance == nil,
	}
	for name, isMissing := range req {
		if isMissing {
			missing = append(missing, name)
		}
	}
	if len(d.Executors) == 0 {
		missing = append(missing, "Executors")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("automation: NewService missing required dependencies: %v", missing)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d}, nil
}

const systemActor = "cloudoptix/automation"

func actorLabel(actor string) string {
	if actor == "" {
		return systemActor
	}
	return actor
}

// writeAudit is the shared audit-record constructor every automation
// mutation goes through. A failure to write it never fails the caller's own
// operation — the operation already happened — matching governance's and
// discovery's own audit-write convention: the audit trail is best-effort,
// the change it describes is not.
func (s *Service) writeAudit(ctx context.Context, tenant core.TenantID, action audit.Action, outcome audit.Outcome, actor string, planID, recID, approvalID, decisionID core.ID, message string, before, after map[string]any) {
	rec := audit.Record{
		TenantID: tenant, Action: action, Outcome: outcome,
		Actor: actorLabel(actor), ActorMachine: actor == "" || actor == systemActor,
		SubjectKind: "execution_plan", SubjectID: planID,
		RecommendationID: recID, PlanID: planID, ApprovalID: approvalID, PolicyDecisionID: decisionID,
		Message: message, Before: before, After: after, At: s.d.Clock.Now(),
	}
	if _, err := s.d.Audit.Append(ctx, rec); err != nil {
		s.d.Logger.Warn("automation: writing audit record failed", "action", action, "error", err)
	}
}

func (s *Service) publish(ctx context.Context, e ports.Event) {
	if s.d.Events == nil {
		return
	}
	if e.ID == "" {
		e.ID = string(core.NewID("evt"))
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = s.d.Clock.Now()
	}
	if err := s.d.Events.Publish(ctx, e); err != nil {
		s.d.Logger.Warn("automation: publishing event failed", "type", e.Type, "error", err)
	}
}

// loadActiveSpec returns the tenant's approved specification, failing closed
// with a plain "no active specification" error rather than falling back to a
// zero-value spec.Spec — a zero-value Automation.Enabled is false, which
// happens to be safe here too, but relying on that coincidence is exactly
// the kind of implicit safety property the rest of this package refuses to
// depend on elsewhere.
func (s *Service) loadActiveSpec(ctx context.Context, tenant core.TenantID) (spec.Spec, error) {
	v, err := s.d.Specs.GetActive(ctx, tenant)
	if err != nil {
		return spec.Spec{}, fmt.Errorf("automation: loading active specification: %w", err)
	}
	return v.Spec, nil
}

// resolveAccount loads the AWS account a resource lives in, by AccountID
// rather than by internal ID, because that is the only identifier a
// cloud.Resource itself carries.
func (s *Service) resolveAccount(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (cloud.AWSAccount, error) {
	acct, err := s.d.AWSAccounts.GetByAccountID(ctx, tenant, accountID)
	if err != nil {
		return cloud.AWSAccount{}, fmt.Errorf("automation: loading AWS account %s: %w", accountID, err)
	}
	return acct, nil
}

// executorFor looks up the executor for an action, failing closed with a
// specific, actionable message — "no executor is registered" is a
// deployment gap, not a resource problem, and the two must not be
// confusable in an error a customer sees.
func (s *Service) executorFor(action optimize.ActionType) (ports.Executor, error) {
	ex, ok := s.d.Executors[action]
	if !ok {
		return nil, core.NewError(core.ErrNotImplemented, "no_executor",
			"no executor is registered for action %q", action)
	}
	return ex, nil
}

// lockKeyForPlan scopes the distributed lock to one tenant's one plan, which
// is the smallest unit two workers could otherwise race on: two calls to
// Execute for the same plan, or an Execute racing a Rollback for the same
// plan.
func lockKeyForPlan(tenant core.TenantID, planID core.ID) string {
	return fmt.Sprintf("automation:plan:%s:%s", tenant, planID)
}

// touchSavings loads (or lazily creates, at StagePotential) the savings
// record for a recommendation and advances it to the given stage. Creating
// it lazily here — rather than requiring some earlier stage of the pipeline
// to have already created a StagePotential row — is a deliberate defensive
// choice: this package must never lose a saving's history because an
// upstream service that is not this package's responsibility to depend on
// forgot to seed one.
func (s *Service) touchSavings(ctx context.Context, tenant core.TenantID, rec optimize.Recommendation, res cloud.Resource, planID core.ID, to execute.SavingsStage, amount core.Money, actor, reason string, now time.Time) {
	sr, err := s.d.Savings.Get(ctx, tenant, rec.ID)
	if err != nil {
		sr = execute.SavingsRecord{
			ID: core.NewID("sav"), TenantID: tenant, RecommendationID: rec.ID,
			RuleID: rec.Finding.RuleID, Action: rec.Action, ResourceID: rec.Finding.ResourceID,
			ApplicationID: res.ApplicationID, Environment: rec.Finding.Environment,
			Stage: execute.StagePotential, PotentialMonthly: rec.EstimatedMonthlySaving,
			CreatedAt: now, UpdatedAt: now,
		}
		s.retireConflictingSavings(ctx, tenant, rec, now)
	}
	if !planID.IsZero() {
		sr.PlanID = planID
	}
	sr.Advance(to, amount, actorLabel(actor), reason, now)
	if err := s.d.Savings.Save(ctx, sr); err != nil {
		s.d.Logger.Warn("automation: advancing savings record failed", "recommendation", rec.ID, "stage", to, "error", err)
	}
}

// retireConflictingSavings marks the savings records of a recommendation's
// conflict-group siblings lost, at the moment that recommendation first
// enters the execution pipeline.
//
// This is what keeps the funnel's potential stage honest. Every other
// aggregate excludes alternatives up front by reading
// Recommendation.CountsTowardTotal, but the funnel is built from savings
// records, and a record only exists once a change is actually in flight — at
// which point the reviewer has already chosen, and it may well be the
// alternative they chose rather than the primary CloudOptix suggested. So
// the funnel cannot filter on "is this the primary"; it has to record that
// the choice was made. Retiring the siblings here is that record: whichever
// member entered the pipeline keeps its full potential, and the ones it
// mechanically excluded show up in the funnel's leakage report with a reason
// rather than silently adding a second claim on the same dollars.
//
// It is best-effort in the same way loseSavings is: a sibling with no record
// yet has nothing to retire, and a sibling that has already advanced past
// potential was itself executed, which is a condition for the plan-level
// conflict check to catch, not for a bookkeeping helper to fail a plan over.
func (s *Service) retireConflictingSavings(ctx context.Context, tenant core.TenantID, rec optimize.Recommendation, now time.Time) {
	if !rec.MutuallyExclusive {
		return
	}
	reason := fmt.Sprintf("mutually exclusive with recommendation %s, which entered the execution pipeline first", rec.ID)
	for _, altID := range rec.AlternativeIDs {
		s.loseSavings(ctx, tenant, altID, reason, now)
	}
}

// loseSavings marks a savings record lost — a rollback, a validation
// failure, an autonomous run that could not schedule a change — rather than
// leaving it to advance no further with no explanation. A silently-stalled
// record is indistinguishable from one still in flight; MarkLost is what
// lets execute.Funnel's leakage report say why.
func (s *Service) loseSavings(ctx context.Context, tenant core.TenantID, recommendationID core.ID, reason string, now time.Time) {
	sr, err := s.d.Savings.Get(ctx, tenant, recommendationID)
	if err != nil {
		return // nothing to mark lost — the record was never created
	}
	sr.MarkLost(reason, now)
	if err := s.d.Savings.Save(ctx, sr); err != nil {
		s.d.Logger.Warn("automation: marking savings record lost failed", "recommendation", recommendationID, "error", err)
	}
}
