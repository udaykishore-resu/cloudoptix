package awsaccounts

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/application/iampolicy"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Deps is every dependency Service needs, expressed as ports. Accounts,
// Tenants, Specs, Executions, Audit and Broker are required: every method
// either persists or reads an account, needs the tenant's gate or its
// current automation posture, needs to know whether a plan is in flight
// against the account, records what happened, or talks to AWS through the
// broker. Events is optional, matching the rest of the application layer's
// convention that a missing event bus narrows a call rather than failing
// it.
type Deps struct {
	Accounts   ports.AWSAccountRepository
	Tenants    ports.TenantRepository
	Specs      ports.SpecRepository
	Executions ports.ExecutionRepository
	Audit      ports.AuditRepository
	Broker     ports.AWSCredentialBroker
	Events     ports.EventPublisher // optional
	Clock      core.Clock
	Logger     *slog.Logger
}

// Service implements ports.AWSAccountService.
type Service struct {
	d Deps
}

var _ ports.AWSAccountService = (*Service)(nil)

// NewService validates the required dependencies and fills in defaults for
// the optional ones.
func NewService(d Deps) (*Service, error) {
	var missing []string
	if d.Accounts == nil {
		missing = append(missing, "Accounts")
	}
	if d.Tenants == nil {
		missing = append(missing, "Tenants")
	}
	if d.Specs == nil {
		missing = append(missing, "Specs")
	}
	if d.Executions == nil {
		missing = append(missing, "Executions")
	}
	if d.Audit == nil {
		missing = append(missing, "Audit")
	}
	if d.Broker == nil {
		missing = append(missing, "Broker")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("awsaccounts: NewService missing required dependencies: %v", missing)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d}, nil
}

// Register onboards a new AWS account: it enforces that the tenant has an
// approved specification and that simulated access is used only by the
// demo tenant, mints a fresh external id, validates the account, persists
// it pending verification, and returns the onboarding instructions for the
// IAM roles the customer still needs to create.
func (s *Service) Register(ctx context.Context, tenant core.TenantID, in ports.RegisterAccountInput) (cloud.AWSAccount, ports.AWSOnboardingInstructions, error) {
	_, actor, err := s.authorize(ctx, core.PermAWSConnect)
	if err != nil {
		return cloud.AWSAccount{}, ports.AWSOnboardingInstructions{}, err
	}

	t, err := s.d.Tenants.Get(ctx, tenant)
	if err != nil {
		return cloud.AWSAccount{}, ports.AWSOnboardingInstructions{}, err
	}
	if ok, reason := t.CanConnectAWS(); !ok {
		return cloud.AWSAccount{}, ports.AWSOnboardingInstructions{}, core.Invalid("cannot register an AWS account: %s", reason)
	}
	if in.AccessMode == cloud.AccessSimulated && !t.Demo {
		return cloud.AWSAccount{}, ports.AWSOnboardingInstructions{}, core.Forbidden(
			"simulated AWS access is reserved for the CloudOptix demo tenant; tenant %s is not a demo tenant", tenant)
	}

	now := s.d.Clock.Now()
	account := cloud.AWSAccount{
		ID: core.NewID("acct"), TenantID: tenant, AccountID: in.AccountID, Alias: in.Alias,
		Environment: in.Environment, Regions: in.Regions, AccessMode: in.AccessMode,
		RoleARNs: in.RoleARNs, IsPayer: in.IsPayer, CURBucket: in.CURBucket, CURPrefix: in.CURPrefix,
		State: cloud.ConnPending, CreatedAt: now, UpdatedAt: now,
	}
	if account.AccessMode == cloud.AccessAssumeRole {
		account.ExternalID = iampolicy.GenerateExternalID()
		account.SessionPrefix = "cloudoptix-" + tenant.String()
	}
	if err := account.Validate(); err != nil {
		return cloud.AWSAccount{}, ports.AWSOnboardingInstructions{}, err
	}

	if err := s.d.Accounts.Create(ctx, account); err != nil {
		return cloud.AWSAccount{}, ports.AWSOnboardingInstructions{}, err
	}

	s.writeAudit(ctx, tenant, audit.ActionAWSAccountRegistered, audit.OutcomeSuccess, actor, account.ID,
		fmt.Sprintf("AWS account %s registered (%s, %s)", account.AccountID, account.Environment, account.AccessMode), nil)

	instructions := s.buildInstructions(ctx, t, account)
	return account, instructions, nil
}

// Verify probes the account's roles through the credential broker and maps
// the result onto the account's connection state: ConnConnected when both
// the read and analyze scopes are granted, ConnDegraded when read works but
// an optional scope is missing, ConnFailed otherwise. The missing IAM
// actions the broker reports are stored verbatim so the customer's next
// step is a concrete list of what to add, not a guess.
func (s *Service) Verify(ctx context.Context, tenant core.TenantID, accountID core.ID) (cloud.AWSAccount, ports.ConnectionCheck, error) {
	_, actor, err := s.authorize(ctx, core.PermAWSConnect)
	if err != nil {
		return cloud.AWSAccount{}, ports.ConnectionCheck{}, err
	}

	account, err := s.d.Accounts.Get(ctx, tenant, accountID)
	if err != nil {
		return cloud.AWSAccount{}, ports.ConnectionCheck{}, err
	}

	now := s.d.Clock.Now()
	check, err := s.d.Broker.Verify(ctx, account)
	if err != nil {
		account.State = cloud.ConnFailed
		account.StateReason = err.Error()
		account.LastVerifiedAt = now
		account.UpdatedAt = now
		if updErr := s.d.Accounts.Update(ctx, account); updErr != nil {
			return cloud.AWSAccount{}, ports.ConnectionCheck{}, updErr
		}
		s.writeAudit(ctx, tenant, audit.ActionAWSAccountVerified, audit.OutcomeFailure, actor, account.ID,
			fmt.Sprintf("AWS account %s verification failed: %v", account.AccountID, err), nil)
		s.publish(ctx, ports.Event{Type: ports.EventAWSAccountFailed, TenantID: tenant, SubjectID: account.ID, Actor: actor})
		return account, ports.ConnectionCheck{}, err
	}

	account.GrantedScopes = check.GrantedScopes
	account.MissingActions = flattenMissing(check.MissingActions)
	account.IsPayer = check.IsPayer
	account.LastVerifiedAt = now
	account.UpdatedAt = now

	switch {
	case !check.Reachable:
		account.State = cloud.ConnFailed
		account.StateReason = "account is not reachable: " + firstOr(check.Errors, "the credential broker could not reach the account")
	case hasScope(check.GrantedScopes, cloud.ScopeRead) && hasScope(check.GrantedScopes, cloud.ScopeAnalyze):
		account.State = cloud.ConnConnected
		account.StateReason = ""
		if account.ConnectedAt.IsZero() {
			account.ConnectedAt = now
		}
	case hasScope(check.GrantedScopes, cloud.ScopeRead):
		account.State = cloud.ConnDegraded
		account.StateReason = "read access is granted but one or more optional permission tiers are missing"
	default:
		account.State = cloud.ConnFailed
		account.StateReason = "read access was not granted"
	}

	if err := s.d.Accounts.Update(ctx, account); err != nil {
		return cloud.AWSAccount{}, ports.ConnectionCheck{}, err
	}

	outcome := audit.OutcomeSuccess
	if account.State == cloud.ConnFailed {
		outcome = audit.OutcomeFailure
	}
	s.writeAudit(ctx, tenant, audit.ActionAWSAccountVerified, outcome, actor, account.ID,
		fmt.Sprintf("AWS account %s verification result: %s", account.AccountID, account.State),
		map[string]any{"missing_actions": account.MissingActions, "granted_scopes": account.GrantedScopes})

	if account.State == cloud.ConnFailed {
		s.publish(ctx, ports.Event{Type: ports.EventAWSAccountFailed, TenantID: tenant, SubjectID: account.ID, Actor: actor})
	} else {
		s.publish(ctx, ports.Event{Type: ports.EventAWSAccountConnected, TenantID: tenant, SubjectID: account.ID, Actor: actor})
	}

	return account, check, nil
}

// List returns every AWS account registered for the tenant.
func (s *Service) List(ctx context.Context, tenant core.TenantID) ([]cloud.AWSAccount, error) {
	return s.d.Accounts.List(ctx, tenant)
}

// Get returns one AWS account.
func (s *Service) Get(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.AWSAccount, error) {
	return s.d.Accounts.Get(ctx, tenant, id)
}

// Suspend disables an account without deleting its record — discovery and
// automation both refuse to run against a suspended account, but its
// history (resources, cost, savings) stays intact.
func (s *Service) Suspend(ctx context.Context, tenant core.TenantID, id core.ID, reason string) error {
	_, actor, err := s.authorize(ctx, core.PermAWSConnect)
	if err != nil {
		return err
	}
	account, err := s.d.Accounts.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	account.State = cloud.ConnSuspended
	account.StateReason = reason
	account.UpdatedAt = s.d.Clock.Now()
	if err := s.d.Accounts.Update(ctx, account); err != nil {
		return err
	}
	s.writeAudit(ctx, tenant, audit.ActionAWSAccountVerified, audit.OutcomeSuccess, actor, account.ID,
		fmt.Sprintf("AWS account %s suspended: %s", account.AccountID, reason), nil)
	return nil
}

// nonTerminalPlanStates are every execute.PlanState for which Remove must
// refuse: everything execute.PlanState.Terminal reports false for. Listed
// explicitly, rather than derived by negation at call time, because the set
// Remove cares about is a property of the domain's state machine, not of
// this package, and a reviewer should be able to see it without cross
// -referencing execute.PlanState.Terminal's switch statement.
var nonTerminalPlanStates = []execute.PlanState{
	execute.PlanDraft, execute.PlanAwaitingApproval, execute.PlanApproved, execute.PlanScheduled,
	execute.PlanPreflight, execute.PlanExecuting, execute.PlanExecuted, execute.PlanValidating,
	execute.PlanFailed, execute.PlanRollingBack,
}

// Remove deletes an account's record. It refuses while an execution plan
// against the account is still in flight: deleting the account out from
// under a plan that has not yet reached a terminal state would strand a
// change that may have already mutated AWS with no CloudOptix record of
// which account it belongs to, and no way to roll it back through the
// normal path.
func (s *Service) Remove(ctx context.Context, tenant core.TenantID, id core.ID) error {
	_, actor, err := s.authorize(ctx, core.PermAWSConnect)
	if err != nil {
		return err
	}
	account, err := s.d.Accounts.Get(ctx, tenant, id)
	if err != nil {
		return err
	}

	inFlight, err := s.inFlightPlan(ctx, tenant, account.AccountID)
	if err != nil {
		return fmt.Errorf("awsaccounts: checking for in-flight execution plans: %w", err)
	}
	if inFlight != nil {
		return core.Conflict(
			"AWS account %s cannot be removed while execution plan %s (state %s) is still in flight against it; "+
				"wait for it to finish or cancel it first",
			account.AccountID, inFlight.ID, inFlight.State)
	}

	if err := s.d.Accounts.Delete(ctx, tenant, id); err != nil {
		return err
	}
	s.writeAudit(ctx, tenant, audit.ActionAWSAccountVerified, audit.OutcomeSuccess, actor, account.ID,
		fmt.Sprintf("AWS account %s removed", account.AccountID), nil)
	return nil
}

// inFlightPlan returns the first non-terminal execution plan targeting
// accountID, or nil when none exists.
func (s *Service) inFlightPlan(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (*execute.Plan, error) {
	page, err := s.d.Executions.ListPlans(ctx, tenant, nonTerminalPlanStates, ports.ListOptions{Limit: 500})
	if err != nil {
		return nil, err
	}
	for i := range page.Items {
		if page.Items[i].AccountID == accountID {
			return &page.Items[i], nil
		}
	}
	return nil, nil
}

// Instructions regenerates the onboarding guidance for an already-registered
// account, reusing its persisted external id (the customer's trust policy
// already requires it) and including the execute tier only when the
// tenant's active specification has automation enabled.
func (s *Service) Instructions(ctx context.Context, tenant core.TenantID, accountID core.ID) (ports.AWSOnboardingInstructions, error) {
	account, err := s.d.Accounts.Get(ctx, tenant, accountID)
	if err != nil {
		return ports.AWSOnboardingInstructions{}, err
	}
	t, err := s.d.Tenants.Get(ctx, tenant)
	if err != nil {
		return ports.AWSOnboardingInstructions{}, err
	}
	return s.buildInstructions(ctx, t, account), nil
}

// buildInstructions is the single path Register and Instructions both go
// through, so a customer registering an account and one asking to see the
// instructions again get an identical answer for an identical account.
func (s *Service) buildInstructions(ctx context.Context, t tenancy.Tenant, account cloud.AWSAccount) ports.AWSOnboardingInstructions {
	if account.AccessMode != cloud.AccessAssumeRole {
		return ports.AWSOnboardingInstructions{
			Instructions: []string{"This account uses simulated AWS access; no IAM roles need to be created."},
		}
	}
	includeExecute := false
	if active, err := s.d.Specs.GetActive(ctx, t.ID); err == nil {
		includeExecute = active.Spec.Automation.Enabled
	}
	return iampolicy.BuildInstructions(t.Slug, account.ExternalID, includeExecute)
}

func hasScope(scopes []cloud.RoleScope, want cloud.RoleScope) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

func firstOr(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	return items[0]
}

// flattenMissing lists every missing IAM action across every scope, sorted
// by scope name for determinism, without altering the action strings
// themselves — "verbatim" is the point, since these are copy-pasted
// straight into a customer's IAM policy editor.
func flattenMissing(m map[string][]string) []string {
	if len(m) == 0 {
		return nil
	}
	scopes := make([]string, 0, len(m))
	for k := range m {
		scopes = append(scopes, k)
	}
	sort.Strings(scopes)
	var out []string
	for _, sc := range scopes {
		out = append(out, m[sc]...)
	}
	return out
}

// authorize resolves the caller principal, checks perm, and returns the
// label to attribute the resulting audit record to.
func (s *Service) authorize(ctx context.Context, perm core.Permission) (core.Principal, string, error) {
	principal, ok := core.PrincipalFrom(ctx)
	if !ok {
		return core.Principal{}, "", core.NewError(core.ErrUnauthenticated, "no_principal", "request has no authenticated principal")
	}
	if err := principal.Authorize(perm); err != nil {
		return core.Principal{}, "", err
	}
	return principal, principal.Describe(), nil
}

const systemActor = "cloudoptix/awsaccounts"

func actorLabel(actor string) string {
	if actor == "" {
		return systemActor
	}
	return actor
}

// writeAudit is best-effort: the operation it documents has already
// succeeded by the time this runs, so a logging failure here must not turn
// into an error returned to the caller — the same convention
// internal/application/governance's Service uses.
func (s *Service) writeAudit(ctx context.Context, tenant core.TenantID, action audit.Action, outcome audit.Outcome, actor string, subjectID core.ID, message string, metadata map[string]any) {
	rec := audit.Record{
		TenantID: tenant, Action: action, Outcome: outcome,
		Actor: actorLabel(actor), ActorMachine: actor == "",
		SubjectKind: "aws_account", SubjectID: subjectID,
		Message: message, Metadata: metadata, At: s.d.Clock.Now(),
	}
	if _, err := s.d.Audit.Append(ctx, rec); err != nil {
		s.d.Logger.Warn("awsaccounts: writing audit record failed", "action", action, "error", err)
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
		s.d.Logger.Warn("awsaccounts: publishing event failed", "type", e.Type, "error", err)
	}
}
