package governance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Deps is every dependency Service needs, expressed as ports so this package
// never imports an adapter. Policies, Approvals, Recommendations, Resources
// and Specs are required — NewService refuses to build without them, because
// every method here either evaluates or records a decision that depends on
// all five. Economics and Events are optional: their absence narrows what a
// call does (no economic-error-budget signal, no published events) rather
// than failing it, matching the rest of the application layer's convention
// that a downstream signal source degrading gracefully is safer than an
// evaluation call refusing to run at all.
type Deps struct {
	Policies        ports.PolicyRepository
	Approvals       ports.ApprovalRepository
	Recommendations ports.RecommendationRepository
	Resources       ports.ResourceRepository
	Specs           ports.SpecRepository
	Audit           ports.AuditRepository
	Economics       ports.EconomicsRepository // optional
	Events          ports.EventPublisher      // optional
	Clock           core.Clock
	Logger          *slog.Logger
}

// Service implements ports.GovernanceService.
type Service struct {
	d Deps
}

var _ ports.GovernanceService = (*Service)(nil)

// NewService validates the required dependencies and fills in defaults for
// the optional ones.
func NewService(d Deps) (*Service, error) {
	var missing []string
	if d.Policies == nil {
		missing = append(missing, "Policies")
	}
	if d.Approvals == nil {
		missing = append(missing, "Approvals")
	}
	if d.Recommendations == nil {
		missing = append(missing, "Recommendations")
	}
	if d.Resources == nil {
		missing = append(missing, "Resources")
	}
	if d.Specs == nil {
		missing = append(missing, "Specs")
	}
	if d.Audit == nil {
		missing = append(missing, "Audit")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("governance: NewService missing required dependencies: %v", missing)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d}, nil
}

// GetPolicy returns the tenant's active policy.
func (s *Service) GetPolicy(ctx context.Context, tenant core.TenantID) (govern.Policy, error) {
	return s.d.Policies.GetActive(ctx, tenant)
}

// ListPolicyVersions returns every saved version of a named policy, newest
// first — the version history a reviewer walks before activating a new one.
func (s *Service) ListPolicyVersions(ctx context.Context, tenant core.TenantID, name string) ([]govern.Policy, error) {
	return s.d.Policies.ListVersions(ctx, tenant, name)
}

// ValidatePolicy runs the domain package's deterministic checks. It is
// exposed standalone (rather than folded silently into SavePolicy) so the
// policy editor UI can show blocking issues as a tenant types, before they
// ever attempt to save.
func (s *Service) ValidatePolicy(ctx context.Context, p govern.Policy) core.ValidationResult {
	return p.Validate()
}

// SavePolicy validates a policy and persists it as a new, versioned,
// inactive draft. It never activates what it saves — see ActivatePolicy —
// so a tenant can review a saved version (including running Simulate against
// it) before it starts governing anything.
//
// The refusal to persist a policy with blocking issues is the governing
// principle applied at the earliest possible point: a policy that failed
// Policy.Validate can still be reasoned about in memory (Simulate accepts an
// unpersisted govern.Policy directly, for exactly this reason) but must never
// reach the version history a tenant could later activate by ID.
func (s *Service) SavePolicy(ctx context.Context, tenant core.TenantID, p govern.Policy, actor string) (govern.Policy, error) {
	if p.TenantID.IsZero() {
		p.TenantID = tenant
	}
	if p.TenantID != tenant {
		return govern.Policy{}, core.NewError(core.ErrTenantMismatch, "tenant_mismatch",
			"policy tenant %s does not match caller scope %s", p.TenantID, tenant)
	}
	if v := p.Validate(); v.HasBlocking() {
		return govern.Policy{}, v.Err()
	}

	now := s.d.Clock.Now()
	existing, err := s.d.Policies.ListVersions(ctx, tenant, p.Name)
	if err != nil {
		return govern.Policy{}, fmt.Errorf("governance: listing existing versions of %q: %w", p.Name, err)
	}
	nextVersion := 1
	for _, v := range existing {
		if v.Version >= nextVersion {
			nextVersion = v.Version + 1
		}
	}

	p.ID = core.NewID("pol")
	p.Version = nextVersion
	p.CreatedBy = actor
	p.CreatedAt = now
	p.Enabled = false
	p.ActivatedAt = time.Time{}
	p.Checksum = checksumPolicy(p)

	if err := s.d.Policies.Save(ctx, p); err != nil {
		return govern.Policy{}, fmt.Errorf("governance: saving policy: %w", err)
	}
	s.writeAudit(ctx, tenant, audit.ActionPolicyCreated, audit.OutcomeSuccess, actor, "policy", p.ID,
		fmt.Sprintf("policy %q v%d saved (%d rules, default %s)", p.Name, p.Version, len(p.Rules), p.DefaultEffect), nil)
	return p, nil
}

// checksumPolicy fingerprints the content a tenant actually authored — name,
// rules and default effect — deliberately excluding CreatedAt/ID/Checksum
// itself, so two policy documents with identical rules always produce the
// same checksum regardless of when or by whom they were saved. That is what
// lets Simulate compare a proposed policy against the active one by content
// rather than by identity.
func checksumPolicy(p govern.Policy) string {
	type checksumShape struct {
		Name          string        `yaml:"name"`
		DefaultEffect govern.Effect `yaml:"default_effect"`
		Rules         []govern.Rule `yaml:"rules"`
	}
	raw, err := yaml.Marshal(checksumShape{Name: p.Name, DefaultEffect: p.DefaultEffect, Rules: p.Rules})
	if err != nil {
		// Marshalling a policy's own rule set cannot fail in practice (every
		// field is a plain value type); falling back to a distinct, stable
		// string keeps the checksum a checksum rather than panicking a save.
		raw = []byte("unmarshalable:" + err.Error())
	}
	return govern.Checksum(string(raw))
}

// ActivatePolicy makes a saved, validated policy version the one policy
// decisions are evaluated against for the tenant, atomically superseding
// whichever version was previously active. It re-validates immediately
// before activation rather than trusting the validation SavePolicy already
// ran: a policy can sit unsaved-but-loaded for a long time (through
// ListPolicyVersions, an old export, a rollback to a prior version id), and
// re-checking closes the gap between "was safe when written" and "is safe
// right now" the same way Execute re-checks policy immediately before
// touching AWS.
func (s *Service) ActivatePolicy(ctx context.Context, tenant core.TenantID, id core.ID, actor string) error {
	p, err := s.d.Policies.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	if v := p.Validate(); v.HasBlocking() {
		return v.Err()
	}
	if err := s.d.Policies.Activate(ctx, tenant, id, actor); err != nil {
		return err
	}
	s.writeAudit(ctx, tenant, audit.ActionPolicyActivated, audit.OutcomeSuccess, actor, "policy", id,
		fmt.Sprintf("policy %q v%d activated", p.Name, p.Version), nil)
	return nil
}

// LoadPolicyYAML parses a policy document — one of the shipped packs in
// policies/, or a tenant-supplied document uploaded through the policy
// editor — into a govern.Policy ready for SavePolicy. It is not part of
// ports.GovernanceService: YAML is a transport concern the HTTP and CLI
// layers own, and this method exists so both can share one parsing and
// validation path rather than each hand-rolling a yaml.Unmarshal call that
// skips validation.
func (s *Service) LoadPolicyYAML(ctx context.Context, tenant core.TenantID, data []byte, actor string) (govern.Policy, error) {
	var p govern.Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return govern.Policy{}, core.Invalid("policy document is not valid YAML: %v", err)
	}
	p.TenantID = tenant
	return s.SavePolicy(ctx, tenant, p, actor)
}

// writeAudit is the shared audit-record constructor every governance
// mutation goes through, so every record carries the same shape (machine vs
// human actor, subject kind, message) and a failure to write it never fails
// the caller's own operation — the operation already happened; the audit
// trail is best-effort exactly the way discovery's own audit writes are.
func (s *Service) writeAudit(ctx context.Context, tenant core.TenantID, action audit.Action, outcome audit.Outcome, actor, subjectKind string, subjectID core.ID, message string, metadata map[string]any) {
	rec := audit.Record{
		TenantID: tenant, Action: action, Outcome: outcome,
		Actor: actorLabel(actor), ActorMachine: actor == "" || actor == systemActor,
		SubjectKind: subjectKind, SubjectID: subjectID,
		Message: message, Metadata: metadata, At: s.d.Clock.Now(),
	}
	if _, err := s.d.Audit.Append(ctx, rec); err != nil {
		s.d.Logger.Warn("governance: writing audit record failed", "action", action, "error", err)
	}
}

const systemActor = "cloudoptix/governance"

func actorLabel(actor string) string {
	if actor == "" {
		return systemActor
	}
	return actor
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
		s.d.Logger.Warn("governance: publishing event failed", "type", e.Type, "error", err)
	}
}
