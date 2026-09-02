package specsvc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Deps is every dependency Service needs, expressed as ports. Specs, Tenants
// and Audit are required — every method either reads or writes a
// specification version, resolves the tenant's current spec id, or records
// what happened. UoW is required because Approve must freeze a version and
// supersede its predecessor atomically. Events is optional: a missing event
// bus narrows Approve to not publishing rather than failing it, matching the
// rest of the application layer's convention for a downstream signal source.
type Deps struct {
	Specs   ports.SpecRepository
	Tenants ports.TenantRepository
	Audit   ports.AuditRepository
	UoW     ports.UnitOfWork
	Events  ports.EventPublisher // optional
	Clock   core.Clock
	Logger  *slog.Logger
}

// Service implements ports.SpecService.
type Service struct {
	d Deps
}

var _ ports.SpecService = (*Service)(nil)

// NewService validates the required dependencies and fills in defaults for
// the optional ones.
func NewService(d Deps) (*Service, error) {
	var missing []string
	if d.Specs == nil {
		missing = append(missing, "Specs")
	}
	if d.Tenants == nil {
		missing = append(missing, "Tenants")
	}
	if d.Audit == nil {
		missing = append(missing, "Audit")
	}
	if d.UoW == nil {
		missing = append(missing, "UoW")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("specsvc: NewService missing required dependencies: %v", missing)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d}, nil
}

// Get returns one specification version by id.
func (s *Service) Get(ctx context.Context, tenant core.TenantID, id core.ID) (spec.Version, error) {
	return s.d.Specs.Get(ctx, tenant, id)
}

// GetActive returns the tenant's currently approved specification version.
func (s *Service) GetActive(ctx context.Context, tenant core.TenantID) (spec.Version, error) {
	return s.d.Specs.GetActive(ctx, tenant)
}

// ListVersions returns every version of the tenant's specification, oldest
// first.
func (s *Service) ListVersions(ctx context.Context, tenant core.TenantID) ([]spec.Version, error) {
	specID, err := s.tenantSpecID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return s.d.Specs.ListVersions(ctx, tenant, specID)
}

// Diff computes the change set between two specification versions, sorted
// by severity (spec.Diff already returns it that way).
func (s *Service) Diff(ctx context.Context, tenant core.TenantID, fromVersion, toVersion int) ([]spec.Change, error) {
	specID, err := s.tenantSpecID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	from, err := s.d.Specs.GetVersion(ctx, tenant, specID, fromVersion)
	if err != nil {
		return nil, fmt.Errorf("specsvc: loading version %d: %w", fromVersion, err)
	}
	to, err := s.d.Specs.GetVersion(ctx, tenant, specID, toVersion)
	if err != nil {
		return nil, fmt.Errorf("specsvc: loading version %d: %w", toVersion, err)
	}
	return spec.Diff(from.Spec, to.Spec), nil
}

// ProposeRevision starts from the tenant's active specification, applies a
// dotted-path JSON merge patch, and stores the result as a new version
// pending review. It refuses a patch that touches a path the caller lacks
// permission for (see pathPermission) and refuses one that would produce a
// specification failing validation with blocking issues — both checks run
// before anything is persisted, so a rejected proposal never becomes a
// draft a reviewer has to notice is broken.
func (s *Service) ProposeRevision(ctx context.Context, tenant core.TenantID, patch map[string]any, actor string) (spec.Version, error) {
	principal, ok := core.PrincipalFrom(ctx)
	if !ok {
		return spec.Version{}, core.NewError(core.ErrUnauthenticated, "no_principal", "request has no authenticated principal")
	}
	if err := principal.Authorize(core.PermSpecWrite); err != nil {
		return spec.Version{}, err
	}
	for path := range patch {
		if perm := pathPermission(path); perm != "" && !principal.Can(perm) {
			return spec.Version{}, core.Forbidden(
				"patch path %q requires permission %s, which %s does not hold", path, perm, principal.Describe()).
				WithDetail("path", path).WithDetail("required_permission", string(perm))
		}
	}

	active, err := s.d.Specs.GetActive(ctx, tenant)
	if err != nil {
		return spec.Version{}, fmt.Errorf("specsvc: loading active specification: %w", err)
	}

	revised, err := applyDottedPatch(active.Spec, patch)
	if err != nil {
		return spec.Version{}, err
	}

	validation := revised.Validate()
	if validation.HasBlocking() {
		return spec.Version{}, core.Invalid("proposed revision fails validation and cannot be stored").
			WithDetail("issues", validation.Issues)
	}

	// GetLatest (rather than active.Version+1) is what keeps version numbers
	// monotonic even when a prior draft was proposed and then rejected: the
	// rejected draft still consumed a version number, and re-using it would
	// let two stored versions share one number.
	latest, err := s.d.Specs.GetLatest(ctx, tenant, active.SpecID)
	if err != nil {
		return spec.Version{}, fmt.Errorf("specsvc: loading latest specification version: %w", err)
	}

	now := s.d.Clock.Now()
	v := spec.Version{
		ID:             core.NewID("specver"),
		TenantID:       tenant,
		SpecID:         active.SpecID,
		Version:        latest.Version + 1,
		Status:         spec.StatusPendingReview,
		Spec:           revised,
		Checksum:       spec.ComputeChecksum(revised),
		ParentID:       active.ID,
		Diff:           spec.Diff(active.Spec, revised),
		Validation:     validation,
		Completeness:   revised.AssessCompleteness(),
		CreatedBy:      actor,
		CreatedAt:      now,
		ConversationID: active.ConversationID,
	}
	if err := s.d.Specs.SaveDraft(ctx, v); err != nil {
		return spec.Version{}, fmt.Errorf("specsvc: saving proposed revision: %w", err)
	}

	s.writeAudit(ctx, tenant, audit.ActionSpecUpdated, audit.OutcomeSuccess, actor, v.ID,
		fmt.Sprintf("specification revision v%d proposed (%d field(s) changed)", v.Version, len(v.Diff)), nil)
	return v, nil
}

// Approve enforces that only a specification pending review may be
// approved and that its validation carries no blocking issues, then
// atomically freezes it and supersedes the tenant's previously active
// version — both writes happen inside one ports.UnitOfWork, together with
// advancing the tenant's ActiveSpecVersion, because a reader must never be
// able to observe the new version active while the tenant record still
// points at the old one, or vice versa.
func (s *Service) Approve(ctx context.Context, tenant core.TenantID, versionID core.ID, actor string) (spec.Version, error) {
	var result spec.Version
	err := s.d.UoW.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		v, err := repos.Specs.Get(ctx, tenant, versionID)
		if err != nil {
			return err
		}
		if v.Status != spec.StatusPendingReview {
			return core.Conflict(
				"specification v%d is %s and cannot be approved; only a specification pending review may be approved",
				v.Version, v.Status)
		}
		if v.Validation.HasBlocking() {
			return core.Invalid("specification v%d has blocking validation issues and cannot be approved", v.Version).
				WithDetail("issues", v.Validation.Issues)
		}

		now := s.d.Clock.Now()
		approvalID := core.NewID("appr")
		if err := v.Approve(actor, approvalID, now); err != nil {
			return err
		}
		if err := repos.Specs.Approve(ctx, tenant, v); err != nil {
			return err
		}

		t, err := repos.Tenants.Get(ctx, tenant)
		if err != nil {
			return err
		}
		t.SpecID = v.SpecID
		t.ActiveSpecVersion = v.Version
		t.UpdatedAt = now
		if err := repos.Tenants.Update(ctx, t); err != nil {
			return err
		}

		if _, err := repos.Audit.Append(ctx, audit.Record{
			TenantID: tenant, Action: audit.ActionSpecApproved, Outcome: audit.OutcomeSuccess,
			Actor: actorLabel(actor), ActorMachine: actor == "",
			SubjectKind: "spec_version", SubjectID: v.ID, SpecVersionID: v.ID, ApprovalID: approvalID,
			Message: fmt.Sprintf("specification v%d approved", v.Version), At: now,
		}); err != nil {
			return err
		}

		result = v
		return nil
	})
	if err != nil {
		return spec.Version{}, err
	}

	// The approval itself already succeeded; a change to automation or
	// governance posture is something the approver needs to be told about,
	// not something that can still block the approval they already made —
	// so this is a log warning, not an error return.
	if changes := automationOrGovernanceChanges(result.Diff); len(changes) > 0 {
		s.d.Logger.Warn("specsvc: approved specification changes automation or governance posture",
			"tenant", tenant, "version", result.Version, "changed_paths", changePaths(changes))
	}

	s.publish(ctx, ports.Event{
		Type: ports.EventSpecApproved, TenantID: tenant, SubjectID: result.ID, Actor: actor,
	})
	return result, nil
}

// automationOrGovernanceChanges filters a diff down to the material changes
// (severity medium or higher, matching spec.HasMaterialChanges) that alter
// automation or governance posture — the ones an approver is agreeing to
// even if they only skimmed the rest of the diff.
func automationOrGovernanceChanges(changes []spec.Change) []spec.Change {
	var out []spec.Change
	for _, c := range changes {
		if c.Severity.Order() < core.SeverityMedium.Order() {
			continue
		}
		if strings.HasPrefix(c.Path, "automation") || strings.HasPrefix(c.Path, "governance") {
			out = append(out, c)
		}
	}
	return out
}

func changePaths(changes []spec.Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.Path)
	}
	return out
}

// Reject declines a specification version pending review, recording why.
func (s *Service) Reject(ctx context.Context, tenant core.TenantID, versionID core.ID, reason, actor string) error {
	v, err := s.d.Specs.Get(ctx, tenant, versionID)
	if err != nil {
		return err
	}
	if v.Status != spec.StatusPendingReview && v.Status != spec.StatusDraft {
		return core.Conflict("specification v%d is %s and cannot be rejected", v.Version, v.Status)
	}
	if err := s.d.Specs.Reject(ctx, tenant, versionID, reason, actor); err != nil {
		return err
	}
	s.writeAudit(ctx, tenant, audit.ActionSpecRejected, audit.OutcomeSuccess, actor, versionID,
		fmt.Sprintf("specification v%d rejected: %s", v.Version, reason), nil)
	return nil
}

// Validate runs the domain package's deterministic checks. It is exposed
// standalone so a patch or an imported document can be checked before any
// attempt to store it.
func (s *Service) Validate(ctx context.Context, sp spec.Spec) core.ValidationResult {
	return sp.Validate()
}

// ExportYAML renders one specification version as the cloudoptix.yaml
// document a customer commits to their repository, using exactly the yaml
// tags on spec.Spec.
func (s *Service) ExportYAML(ctx context.Context, tenant core.TenantID, versionID core.ID) ([]byte, error) {
	v, err := s.d.Specs.Get(ctx, tenant, versionID)
	if err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(v.Spec)
	if err != nil {
		return nil, fmt.Errorf("specsvc: marshalling specification v%d: %w", v.Version, err)
	}
	return out, nil
}

// ImportYAML parses a cloudoptix.yaml document, rejects one whose
// apiVersion this build does not support, validates it, and — only if
// validation carries no blocking issues — stores it as a new version
// pending review.
func (s *Service) ImportYAML(ctx context.Context, tenant core.TenantID, data []byte, actor string) (spec.Version, error) {
	var sp spec.Spec
	if err := yaml.Unmarshal(data, &sp); err != nil {
		return spec.Version{}, core.Invalid("specification document is not valid YAML: %v", err)
	}
	if sp.APIVersion != spec.CurrentAPIVersion {
		return spec.Version{}, core.Invalid(
			"apiVersion %q is not supported by this build; expected %s", sp.APIVersion, spec.CurrentAPIVersion)
	}

	validation := sp.Validate()
	if validation.HasBlocking() {
		return spec.Version{}, core.Invalid("imported specification fails validation and cannot be stored").
			WithDetail("issues", validation.Issues)
	}

	t, err := s.d.Tenants.Get(ctx, tenant)
	if err != nil {
		return spec.Version{}, err
	}

	specID := t.SpecID
	var parentID core.ID
	nextVersion := 1
	if !specID.IsZero() {
		if active, err := s.d.Specs.GetActive(ctx, tenant); err == nil {
			parentID = active.ID
		}
		if latest, err := s.d.Specs.GetLatest(ctx, tenant, specID); err == nil {
			nextVersion = latest.Version + 1
		}
	} else {
		specID = core.NewID("spec")
	}

	var diff []spec.Change
	if !parentID.IsZero() {
		if parent, err := s.d.Specs.Get(ctx, tenant, parentID); err == nil {
			diff = spec.Diff(parent.Spec, sp)
		}
	}

	now := s.d.Clock.Now()
	v := spec.Version{
		ID: core.NewID("specver"), TenantID: tenant, SpecID: specID, Version: nextVersion,
		Status: spec.StatusPendingReview, Spec: sp, Checksum: spec.ComputeChecksum(sp),
		ParentID: parentID, Diff: diff, Validation: validation, Completeness: sp.AssessCompleteness(),
		CreatedBy: actor, CreatedAt: now,
	}
	if err := s.d.Specs.SaveDraft(ctx, v); err != nil {
		return spec.Version{}, fmt.Errorf("specsvc: saving imported specification: %w", err)
	}

	s.writeAudit(ctx, tenant, audit.ActionSpecUpdated, audit.OutcomeSuccess, actor, v.ID,
		fmt.Sprintf("specification v%d imported from YAML", v.Version), nil)
	return v, nil
}

// tenantSpecID resolves the tenant's current specification id, the key
// several SpecRepository methods need but ports.SpecService's own signatures
// (deliberately) do not carry.
func (s *Service) tenantSpecID(ctx context.Context, tenant core.TenantID) (core.ID, error) {
	t, err := s.d.Tenants.Get(ctx, tenant)
	if err != nil {
		return "", err
	}
	if t.SpecID.IsZero() {
		return "", core.NotFound("spec", tenant)
	}
	return t.SpecID, nil
}

const systemActor = "cloudoptix/specsvc"

func actorLabel(actor string) string {
	if actor == "" {
		return systemActor
	}
	return actor
}

// writeAudit is best-effort: the operation it documents already succeeded
// (or, for a proposal, already persisted) by the time this runs, so a
// logging failure here must not turn into an error returned to the caller —
// the same convention internal/application/governance's Service uses.
func (s *Service) writeAudit(ctx context.Context, tenant core.TenantID, action audit.Action, outcome audit.Outcome, actor string, subjectID core.ID, message string, metadata map[string]any) {
	rec := audit.Record{
		TenantID: tenant, Action: action, Outcome: outcome,
		Actor: actorLabel(actor), ActorMachine: actor == "",
		SubjectKind: "spec_version", SubjectID: subjectID, SpecVersionID: subjectID,
		Message: message, Metadata: metadata, At: s.d.Clock.Now(),
	}
	if _, err := s.d.Audit.Append(ctx, rec); err != nil {
		s.d.Logger.Warn("specsvc: writing audit record failed", "action", action, "error", err)
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
		s.d.Logger.Warn("specsvc: publishing event failed", "type", e.Type, "error", err)
	}
}
