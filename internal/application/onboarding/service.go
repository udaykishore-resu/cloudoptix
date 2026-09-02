package onboarding

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/application/iampolicy"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const openingMessage = "Let's get CloudOptix set up. What's your company called, and what does it do?"

// Service implements ports.OnboardingService.
//
// KEY DESIGN DECISION — the tenant-scope index. Every ports.Repositories
// method is tenant-guarded (core.GuardTenant reads the tenant out of ctx),
// but ports.OnboardingService's Send/State/Summarize/ApplyEdit/Cancel take
// only a conversation id, with no tenant argument and no assumption that
// the caller already holds an authenticated principal — deliberately so,
// since Start's very first message is sent before any tenant exists to be
// a principal member of. Service resolves that gap itself: Start mints the
// tenant scope a conversation will live under (a real Tenant row is not
// created until Approve; see Start's doc comment) and records
// conversation id -> tenant id in an in-process map, consulted by every
// other method before it builds the ctx a repository call needs. This is a
// pragmatic, in-memory stand-in for what a production deployment would
// carry as a short-lived, unauthenticated "onboarding session" claim in a
// cookie or bearer token; it does not survive a process restart, which is
// consistent with the memstore-backed demo deployment this codebase ships
// today.
type Service struct {
	uow      ports.UnitOfWork
	provider ports.LLMProvider
	events   ports.EventPublisher
	clock    func() time.Time

	mu         sync.RWMutex
	convTenant map[core.ID]core.TenantID
}

var _ ports.OnboardingService = (*Service)(nil)

// New builds a Service. events may be nil, in which case Approve simply does
// not publish (useful for tests that have no event bus wired).
func New(uow ports.UnitOfWork, provider ports.LLMProvider, events ports.EventPublisher) *Service {
	return &Service{
		uow: uow, provider: provider, events: events,
		clock:      func() time.Time { return time.Now().UTC() },
		convTenant: map[core.ID]core.TenantID{},
	}
}

func (s *Service) now() time.Time { return s.clock() }

func (s *Service) rememberTenant(conv core.ID, tenant core.TenantID) {
	s.mu.Lock()
	s.convTenant[conv] = tenant
	s.mu.Unlock()
}

func (s *Service) tenantFor(conv core.ID) (core.TenantID, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.convTenant[conv]
	return t, ok
}

// withPrincipal builds the internal service-account context every
// repository call needs. actor is recorded as the acting subject so the
// audit trail (see Approve) attributes writes to the real conversation
// participant rather than an anonymous system identity.
func withPrincipal(ctx context.Context, tenant core.TenantID, actor string) context.Context {
	if actor == "" {
		actor = "onboarding-agent"
	}
	return core.WithPrincipal(ctx, core.Principal{
		Subject: actor, TenantID: tenant, Roles: []core.Role{core.RoleTenantAdmin}, IssuedAt: time.Now().UTC(),
	})
}

// Start opens a new onboarding conversation. See the Service doc comment
// for how conversations are tenant-scoped before a Tenant row exists.
func (s *Service) Start(ctx context.Context, in ports.StartOnboardingInput) (ports.OnboardingState, error) {
	now := s.now()
	var result ports.OnboardingState

	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		tenant := in.ExistingTenant
		var draft spec.Spec
		var specID core.ID

		if tenant.IsZero() {
			tenant = core.TenantID(core.NewID("tenant"))
			draft = newDraftSpec()
			specID = core.NewID("spec")
		} else {
			// Revising an already-onboarded tenant: start from its active
			// specification rather than a blank one, so re-running
			// onboarding to change one thing does not throw away
			// everything already known.
			ctx = withPrincipal(ctx, tenant, in.Actor)
			if active, err := repos.Specs.GetActive(ctx, tenant); err == nil {
				draft = active.Spec
				specID = active.SpecID
			} else {
				draft = newDraftSpec()
				specID = core.NewID("spec")
			}
		}
		ctx = withPrincipal(ctx, tenant, in.Actor)

		convID := core.NewID("conv")
		conv := ports.Conversation{
			ID: convID, TenantID: tenant, Kind: ports.ConversationOnboarding,
			Title: "Onboarding", Actor: in.Actor, SpecID: specID, State: "active",
			CreatedAt: now, UpdatedAt: now,
		}

		reply := openingMessage
		degraded := false
		if strings.TrimSpace(in.InitialMessage) != "" {
			turnReply, wasDegraded := s.processTurn(ctx, &draft, nil, in.InitialMessage)
			reply = turnReply
			degraded = wasDegraded
			conv.Turns = append(conv.Turns, ports.Turn{
				ID: core.NewID("turn"), Role: ports.RoleUser, Content: in.InitialMessage, At: now,
			})
		}
		conv.Turns = append(conv.Turns, ports.Turn{
			ID: core.NewID("turn"), Role: ports.RoleAssistant, Content: reply, At: now, Degraded: degraded,
		})

		if err := repos.Conversations.Create(ctx, conv); err != nil {
			return err
		}
		if err := repos.Specs.SaveDraft(ctx, spec.Version{
			ID: core.NewID("specver"), TenantID: tenant, SpecID: specID, Status: spec.StatusDraft,
			Spec: draft, CreatedBy: in.Actor, CreatedAt: now, ConversationID: convID,
		}); err != nil {
			return err
		}

		s.rememberTenant(convID, tenant)
		result = buildOnboardingState(conv, draft, reply, currentStage(draft), degraded, defaultSuggestions())
		return nil
	})
	return result, err
}

// Send processes one user message against the most recent draft.
func (s *Service) Send(ctx context.Context, conversationID core.ID, message string) (ports.OnboardingState, error) {
	tenant, ok := s.tenantFor(conversationID)
	if !ok {
		return ports.OnboardingState{}, core.NotFound("conversation", conversationID)
	}
	now := s.now()
	var result ports.OnboardingState

	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		ctx = withPrincipal(ctx, tenant, "")
		conv, err := repos.Conversations.Get(ctx, tenant, conversationID)
		if err != nil {
			return err
		}
		if conv.State != "active" {
			return core.Conflict("conversation %s is %s and cannot accept new messages", conversationID, conv.State)
		}
		active, err := repos.Specs.GetLatest(ctx, tenant, conv.SpecID)
		if err != nil {
			return err
		}
		draft := active.Spec

		history := toPortsMessages(conv.Turns)
		reply, degraded := s.processTurn(ctx, &draft, history, message)

		conv.Turns = append(conv.Turns,
			ports.Turn{ID: core.NewID("turn"), Role: ports.RoleUser, Content: message, At: now},
			ports.Turn{ID: core.NewID("turn"), Role: ports.RoleAssistant, Content: reply, At: now, Degraded: degraded},
		)
		conv.UpdatedAt = now
		if err := repos.Conversations.Update(ctx, conv); err != nil {
			return err
		}
		if err := repos.Specs.SaveDraft(ctx, spec.Version{
			ID: core.NewID("specver"), TenantID: tenant, SpecID: conv.SpecID, Status: spec.StatusDraft,
			Spec: draft, CreatedBy: conv.Actor, CreatedAt: now, ConversationID: conv.ID,
		}); err != nil {
			return err
		}

		result = buildOnboardingState(conv, draft, reply, currentStage(draft), degraded, defaultSuggestions())
		return nil
	})
	return result, err
}

// State returns the current state without sending a message.
func (s *Service) State(ctx context.Context, conversationID core.ID) (ports.OnboardingState, error) {
	tenant, ok := s.tenantFor(conversationID)
	if !ok {
		return ports.OnboardingState{}, core.NotFound("conversation", conversationID)
	}
	var result ports.OnboardingState
	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		ctx = withPrincipal(ctx, tenant, "")
		conv, err := repos.Conversations.Get(ctx, tenant, conversationID)
		if err != nil {
			return err
		}
		active, err := repos.Specs.GetLatest(ctx, tenant, conv.SpecID)
		if err != nil {
			return err
		}
		result = buildOnboardingState(conv, active.Spec, "", currentStage(active.Spec), false, nil)
		return nil
	})
	return result, err
}

// Summarize produces the pre-approval review packet.
func (s *Service) Summarize(ctx context.Context, conversationID core.ID) (ports.OnboardingSummary, error) {
	tenant, ok := s.tenantFor(conversationID)
	if !ok {
		return ports.OnboardingSummary{}, core.NotFound("conversation", conversationID)
	}
	var result ports.OnboardingSummary
	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		ctx = withPrincipal(ctx, tenant, "")
		conv, err := repos.Conversations.Get(ctx, tenant, conversationID)
		if err != nil {
			return err
		}
		active, err := repos.Specs.GetLatest(ctx, tenant, conv.SpecID)
		if err != nil {
			return err
		}
		draft := active.Spec
		completeness := draft.AssessCompleteness()
		validation := draft.Validate()
		yamlBytes, err := RenderYAML(draft)
		if err != nil {
			return err
		}

		collected, inferred, unknown, needsConfirm := buildFieldStates(draft)
		all := append(append(append(append([]ports.FieldState{}, collected...), inferred...), unknown...), needsConfirm...)
		sections := []ports.SummarySection{
			{Title: "Organization & Application", Fields: filterByPrefix(all, "organization.", "application.")},
			{Title: "AWS", Fields: filterByPrefix(all, "aws.", "security.")},
			{Title: "Business", Fields: filterByPrefix(all, "business.")},
			{Title: "Objectives", Fields: filterByPrefix(all, "objectives.")},
			{Title: "Governance & Risk", Fields: filterByPrefix(all, "optimization.", "governance.")},
		}

		var blocking []string
		for _, iss := range validation.Issues {
			if isPreConnectionIssue(iss) {
				continue
			}
			if iss.Severity.Order() >= core.SeverityHigh.Order() {
				blocking = append(blocking, iss.Path+": "+iss.Message)
			}
		}
		blocking = append(blocking, completeness.BlockingGaps...)

		result = ports.OnboardingSummary{
			ConversationID: conv.ID, Spec: draft, SpecYAML: string(yamlBytes),
			Completeness: completeness, Validation: validation, Sections: sections,
			WhatHappensNext: []string{
				"The specification is frozen as an approved version and becomes the configuration every CloudOptix engine reads.",
				"A tenant is created from it, with a baseline require-approval policy already active — no optimization executes automatically until you say so.",
				"You'll receive AWS onboarding instructions: an external ID and least-privilege IAM policy documents, one per permission tier, to create in your account(s).",
				"Nothing in your AWS estate changes until you connect an account and CloudOptix completes discovery — approval creates the specification, not a connection.",
			},
			CanApprove:      len(blocking) == 0,
			BlockingReasons: blocking,
		}
		return nil
	})
	return result, err
}

// ApplyEdit applies a direct patch made in the review UI, using the same
// extraction interpreter Send uses so a review-screen edit and a
// conversational one ("change X to Y") are always applied identically.
func (s *Service) ApplyEdit(ctx context.Context, conversationID core.ID, patch map[string]any, actor string) (ports.OnboardingState, error) {
	tenant, ok := s.tenantFor(conversationID)
	if !ok {
		return ports.OnboardingState{}, core.NotFound("conversation", conversationID)
	}
	now := s.now()
	var result ports.OnboardingState
	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		ctx = withPrincipal(ctx, tenant, actor)
		conv, err := repos.Conversations.Get(ctx, tenant, conversationID)
		if err != nil {
			return err
		}
		active, err := repos.Specs.GetLatest(ctx, tenant, conv.SpecID)
		if err != nil {
			return err
		}
		draft := active.Spec
		applyExtraction(&draft, patch)
		runInference(&draft)
		pruneAnsweredQuestions(&draft)

		if err := repos.Specs.SaveDraft(ctx, spec.Version{
			ID: core.NewID("specver"), TenantID: tenant, SpecID: conv.SpecID, Status: spec.StatusDraft,
			Spec: draft, CreatedBy: actor, CreatedAt: now, ConversationID: conv.ID,
		}); err != nil {
			return err
		}
		result = buildOnboardingState(conv, draft, "Updated.", currentStage(draft), false, nil)
		return nil
	})
	return result, err
}

// Approve validates and freezes the specification, creates the tenant and
// organization it describes, seeds a safe-by-default policy, writes the
// audit trail, publishes events, and returns AWS onboarding instructions.
// Every step runs inside one UnitOfWork: a failure at any point — including
// validation failing before anything is written — leaves no partial tenant
// behind.
func (s *Service) Approve(ctx context.Context, in ports.ApproveOnboardingInput) (ports.OnboardingResult, error) {
	tenant, ok := s.tenantFor(in.ConversationID)
	if !ok {
		return ports.OnboardingResult{}, core.NotFound("conversation", in.ConversationID)
	}
	now := s.now()
	var result ports.OnboardingResult

	err := s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		ctx = withPrincipal(ctx, tenant, in.Actor)
		conv, err := repos.Conversations.Get(ctx, tenant, in.ConversationID)
		if err != nil {
			return err
		}
		if conv.State == "completed" {
			return core.Conflict("conversation %s was already approved", in.ConversationID)
		}
		active, err := repos.Specs.GetLatest(ctx, tenant, conv.SpecID)
		if err != nil {
			return err
		}
		draft := active.Spec

		validation := draft.Validate()
		if approvalBlocking(validation) {
			return core.Invalid("specification has blocking validation issues and cannot be approved").
				WithDetail("issues", validation.Issues)
		}
		completeness := draft.AssessCompleteness()
		if !completeness.ReadyForReview {
			return core.Invalid("specification is missing required fields: %s", strings.Join(completeness.BlockingGaps, ", "))
		}

		version := active
		version.Status = spec.StatusPendingReview
		version.Validation = filterForApproval(validation)
		version.Completeness = completeness
		version.Checksum = spec.ComputeChecksum(draft)
		approvalID := core.NewID("appr")
		if err := version.Approve(in.Actor, approvalID, now); err != nil {
			return err
		}
		version.ConversationID = conv.ID
		if err := repos.Specs.Approve(ctx, tenant, version); err != nil {
			return err
		}

		plan := in.Plan
		if plan == "" {
			plan = tenancy.PlanTrial
		}
		activatedAt := now
		tenantRec := tenancy.Tenant{
			ID: tenant, Slug: in.TenantSlug, Name: in.TenantName,
			Plan: plan, Quotas: tenancy.QuotasFor(plan), State: tenancy.StateActive,
			Demo:   in.Demo,
			SpecID: conv.SpecID, ActiveSpecVersion: version.Version,
			PrimaryContact: in.ActorEmail, CreatedAt: now, ActivatedAt: &activatedAt, UpdatedAt: now,
		}
		if err := tenantRec.Validate(); err != nil {
			return err
		}
		if err := repos.Tenants.Create(ctx, tenantRec); err != nil {
			return err
		}
		if err := repos.Tenants.CreateOrganization(ctx, tenancy.Organization{
			ID: core.NewID("org"), TenantID: tenant, Name: draft.Organization.Name,
			Industry: draft.Organization.Industry, Size: draft.Organization.Size,
			BusinessRegions: draft.Organization.Regions, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}

		policy := govern.Policy{
			ID: core.NewID("pol"), TenantID: tenant, Name: "baseline", Version: 1,
			DefaultEffect: govern.EffectRequireApproval, Enabled: true,
			CreatedBy: in.Actor, CreatedAt: now,
		}
		policy.Checksum = govern.Checksum(fmt.Sprintf("%s:%d:%s", policy.Name, policy.Version, policy.DefaultEffect))
		if err := repos.Policies.Save(ctx, policy); err != nil {
			return err
		}
		if err := repos.Policies.Activate(ctx, tenant, policy.ID, in.Actor); err != nil {
			return err
		}
		tenantRec.ActivePolicyID = policy.ID
		if err := repos.Tenants.Update(ctx, tenantRec); err != nil {
			return err
		}

		if _, err := repos.Audit.Append(ctx, audit.Record{
			TenantID: tenant, Action: audit.ActionTenantCreated, Outcome: audit.OutcomeSuccess,
			Actor: in.Actor, SubjectKind: "tenant", SubjectID: core.ID(tenant), SubjectName: tenantRec.Name,
			Message:   fmt.Sprintf("tenant %q created from onboarding conversation %s", tenantRec.Name, conv.ID),
			IPAddress: in.IPAddress, UserAgent: in.UserAgent, At: now,
		}); err != nil {
			return err
		}
		if _, err := repos.Audit.Append(ctx, audit.Record{
			TenantID: tenant, Action: audit.ActionSpecApproved, Outcome: audit.OutcomeSuccess,
			Actor: in.Actor, SubjectKind: "spec_version", SubjectID: version.ID, SpecVersionID: version.ID,
			ApprovalID: approvalID, Message: fmt.Sprintf("specification v%d approved", version.Version), At: now,
		}); err != nil {
			return err
		}

		conv.State = "completed"
		conv.UpdatedAt = now
		if err := repos.Conversations.Update(ctx, conv); err != nil {
			return err
		}

		if s.events != nil {
			_ = s.events.Publish(ctx, ports.Event{
				ID: string(core.NewID("evt")), Type: ports.EventTenantCreated, TenantID: tenant,
				OccurredAt: now, Actor: in.Actor,
			})
			_ = s.events.Publish(ctx, ports.Event{
				ID: string(core.NewID("evt")), Type: ports.EventSpecApproved, TenantID: tenant,
				SubjectID: version.ID, OccurredAt: now, Actor: in.Actor,
			})
		}

		result = ports.OnboardingResult{
			Tenant: tenantRec, SpecVersion: version,
			NextSteps: buildInstructions(tenantRec, draft),
		}
		return nil
	})
	return result, err
}

// Cancel abandons the conversation.
func (s *Service) Cancel(ctx context.Context, conversationID core.ID, reason string) error {
	tenant, ok := s.tenantFor(conversationID)
	if !ok {
		return core.NotFound("conversation", conversationID)
	}
	now := s.now()
	return s.uow.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		ctx = withPrincipal(ctx, tenant, "")
		conv, err := repos.Conversations.Get(ctx, tenant, conversationID)
		if err != nil {
			return err
		}
		conv.State = "abandoned"
		conv.UpdatedAt = now
		conv.Turns = append(conv.Turns, ports.Turn{
			ID: core.NewID("turn"), Role: ports.RoleAssistant,
			Content: "Onboarding cancelled: " + reason, At: now,
		})
		return repos.Conversations.Update(ctx, conv)
	})
}

// buildInstructions renders the AWS onboarding guidance returned by
// Approve: the external id, the trust and least-privilege policy documents
// for each permission tier the specification calls for, and plain-language
// steps. The construction itself lives in internal/application/iampolicy,
// shared with internal/application/awsaccounts — see that package's doc
// comment. The external id minted here is advisory: it previews what
// account registration will generate, not the id a later
// AWSAccountService.Register call actually persists, since no AWS account
// exists yet at the point onboarding approval runs.
func buildInstructions(t tenancy.Tenant, draft spec.Spec) ports.AWSOnboardingInstructions {
	return iampolicy.BuildInstructions(t.Slug, iampolicy.GenerateExternalID(), draft.Automation.Enabled)
}

func toPortsMessages(turns []ports.Turn) []ports.Message {
	out := make([]ports.Message, 0, len(turns))
	for _, t := range turns {
		out = append(out, ports.Message{Role: t.Role, Content: t.Content})
	}
	return out
}

func filterByPrefix(fields []ports.FieldState, prefixes ...string) []ports.FieldState {
	var out []ports.FieldState
	for _, f := range fields {
		for _, p := range prefixes {
			if strings.HasPrefix(f.Path, p) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func defaultSuggestions() []string {
	return []string{"Show me what you know", "I don't know"}
}
