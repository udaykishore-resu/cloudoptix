package tenants

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Deps is every dependency Service needs.
type Deps struct {
	Tenants ports.TenantRepository
	Users   ports.UserRepository
	Audit   ports.AuditRepository
	Events  ports.EventPublisher // optional
	Clock   core.Clock
	Logger  *slog.Logger
}

// Service implements ports.TenantService.
type Service struct {
	d Deps
}

var _ ports.TenantService = (*Service)(nil)

// NewService validates the required dependencies and fills in defaults for
// the optional ones.
func NewService(d Deps) (*Service, error) {
	var missing []string
	if d.Tenants == nil {
		missing = append(missing, "Tenants")
	}
	if d.Users == nil {
		missing = append(missing, "Users")
	}
	if d.Audit == nil {
		missing = append(missing, "Audit")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("tenants: NewService missing required dependencies: %v", missing)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d}, nil
}

// Get returns the tenant record.
func (s *Service) Get(ctx context.Context, id core.TenantID) (tenancy.Tenant, error) {
	return s.d.Tenants.Get(ctx, id)
}

// Update applies a tenant edit, refusing to change the identifiers a tenant
// admin must never be able to move: id, slug and the demo flag (see the
// package doc comment for why each matters). Quotas are always recomputed
// from the tenant's plan rather than trusted from the caller, so Update can
// never become a path to granting a tenant more headroom than its plan
// allows.
func (s *Service) Update(ctx context.Context, t tenancy.Tenant, actor string) (tenancy.Tenant, error) {
	existing, err := s.d.Tenants.Get(ctx, t.ID)
	if err != nil {
		return tenancy.Tenant{}, err
	}

	if t.ID != existing.ID {
		return tenancy.Tenant{}, core.Invalid(
			"tenant id cannot be changed: it is the isolation key for every row, cache entry and event this tenant owns")
	}
	if t.Slug != existing.Slug {
		return tenancy.Tenant{}, core.Invalid(
			"tenant slug %q cannot be changed to %q: the slug is baked into every generated IAM role name and cache key that already references this tenant, so changing it would orphan them", existing.Slug, t.Slug)
	}
	if t.Demo != existing.Demo {
		return tenancy.Tenant{}, core.Invalid(
			"the demo flag cannot be changed after creation: it is the sole gate that permits simulated AWS access, and toggling it on an existing tenant would silently change what access modes are permitted for every account already registered")
	}

	updated := existing
	updated.Name = t.Name
	updated.Plan = t.Plan
	updated.Quotas = tenancy.QuotasFor(t.Plan)
	updated.State = t.State
	updated.PrimaryContact = t.PrimaryContact
	updated.DataRegion = t.DataRegion
	updated.EncryptionKeyARN = t.EncryptionKeyARN
	updated.UpdatedAt = s.d.Clock.Now()
	// SpecID, ActiveSpecVersion and ActivePolicyID are advanced only by
	// specsvc.Service.Approve and governance.Service.ActivatePolicy — never
	// by a direct tenant edit — so they are deliberately not copied from t.

	if err := updated.Validate(); err != nil {
		return tenancy.Tenant{}, err
	}
	if err := s.d.Tenants.Update(ctx, updated); err != nil {
		return tenancy.Tenant{}, err
	}

	s.writeAudit(ctx, t.ID, audit.ActionTenantUpdated, actor, "tenant", core.ID(t.ID),
		fmt.Sprintf("tenant %q updated", updated.Name), nil)
	return updated, nil
}

// ListUsers returns the tenant's members.
func (s *Service) ListUsers(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[tenancy.User], error) {
	return s.d.Users.ListByTenant(ctx, tenant, opts.Normalize())
}

// InviteUser finds or creates the user by email, grants the requested
// roles as a new membership in tenant, and refuses platform_admin — see the
// package doc comment for why that refusal belongs here rather than being
// left to core.Role.Valid, which only checks that a role is a role
// CloudOptix knows about at all, not that it is safe to grant from inside
// tenant-scoped user management.
func (s *Service) InviteUser(ctx context.Context, tenant core.TenantID, email string, roles []core.Role, actor string) (tenancy.User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return tenancy.User{}, core.Invalid("email is required to invite a user")
	}
	if err := validateGrantableRoles(roles); err != nil {
		return tenancy.User{}, err
	}

	now := s.d.Clock.Now()
	u, err := s.d.Users.GetByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			return tenancy.User{}, err
		}
		// The user's OIDC subject is not known until they first sign in;
		// "pending:<email>" is a placeholder subject that GetBySubject will
		// never match against a real login, so a genuine sign-in upserts
		// this record with its real subject rather than colliding with it.
		u = tenancy.User{ID: core.NewID("usr"), Subject: "pending:" + email, Email: email, CreatedAt: now, UpdatedAt: now}
		if err := s.d.Users.Upsert(ctx, u); err != nil {
			return tenancy.User{}, err
		}
	}

	membership := tenancy.Membership{TenantID: tenant, Roles: roles, GrantedBy: actor, GrantedAt: now}
	if err := s.d.Users.AddMembership(ctx, u.ID, membership); err != nil {
		return tenancy.User{}, err
	}
	u.Memberships = replaceMembership(u.Memberships, membership)

	s.writeAudit(ctx, tenant, audit.ActionUserInvited, actor, "user", u.ID,
		fmt.Sprintf("user %s invited with roles %v", email, roles), nil)
	return u, nil
}

// UpdateRoles replaces a user's role set within tenant. It refuses to
// demote the tenant's last remaining tenant_admin and refuses to grant
// platform_admin, for the reasons given in the package doc comment.
func (s *Service) UpdateRoles(ctx context.Context, tenant core.TenantID, userID core.ID, roles []core.Role, actor string) error {
	if err := validateGrantableRoles(roles); err != nil {
		return err
	}

	if !roleSetHas(roles, core.RoleTenantAdmin) {
		isLast, err := s.isLastTenantAdmin(ctx, tenant, userID)
		if err != nil {
			return err
		}
		if isLast {
			return core.Conflict(
				"cannot remove tenant_admin from the last administrator of this tenant: a tenant with no administrator has no one able to invite, approve or recover access")
		}
	}

	membership := tenancy.Membership{TenantID: tenant, Roles: roles, GrantedBy: actor, GrantedAt: s.d.Clock.Now()}
	if err := s.d.Users.AddMembership(ctx, userID, membership); err != nil {
		return err
	}

	s.writeAudit(ctx, tenant, audit.ActionUserRoleChanged, actor, "user", userID,
		fmt.Sprintf("roles changed to %v", roles), nil)
	return nil
}

// RemoveUser removes a user's membership in tenant. It refuses to remove
// the tenant's last remaining tenant_admin for the same reason UpdateRoles
// refuses to demote one.
func (s *Service) RemoveUser(ctx context.Context, tenant core.TenantID, userID core.ID, actor string) error {
	isLast, err := s.isLastTenantAdmin(ctx, tenant, userID)
	if err != nil {
		return err
	}
	if isLast {
		return core.Conflict(
			"cannot remove the last tenant_admin from this tenant: a tenant with no administrator has no one able to invite, approve or recover access")
	}

	if err := s.d.Users.RemoveMembership(ctx, userID, tenant); err != nil {
		return err
	}

	s.writeAudit(ctx, tenant, audit.ActionUserRoleChanged, actor, "user", userID, "user removed from tenant", nil)
	return nil
}

// validateGrantableRoles checks every role is a role CloudOptix recognises
// and that none of them is platform_admin — see the package doc comment.
func validateGrantableRoles(roles []core.Role) error {
	if len(roles) == 0 {
		return core.Invalid("at least one role is required")
	}
	for _, r := range roles {
		if !r.Valid() {
			return core.Invalid("%q is not a recognised CloudOptix role", r)
		}
		if r == core.RolePlatformAdmin {
			return core.Forbidden(
				"platform_admin cannot be granted through tenant user management: it is a cross-tenant operator role, and issuing it from inside a tenant would let a tenant admin escalate their own access to every other customer's data")
		}
	}
	return nil
}

// isLastTenantAdmin reports whether userID is currently a tenant_admin of
// tenant and no other active member holds that role.
func (s *Service) isLastTenantAdmin(ctx context.Context, tenant core.TenantID, userID core.ID) (bool, error) {
	now := s.d.Clock.Now()
	page, err := s.d.Users.ListByTenant(ctx, tenant, ports.ListOptions{Limit: 500})
	if err != nil {
		return false, err
	}
	isTarget := false
	otherAdmins := 0
	for _, u := range page.Items {
		for _, m := range u.Memberships {
			if m.TenantID != tenant || !m.Active(now) || !roleSetHas(m.Roles, core.RoleTenantAdmin) {
				continue
			}
			if u.ID == userID {
				isTarget = true
			} else {
				otherAdmins++
			}
		}
	}
	return isTarget && otherAdmins == 0, nil
}

func roleSetHas(roles []core.Role, want core.Role) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

// replaceMembership mirrors what UserRepository.AddMembership does server
// side (replace-by-tenant), so InviteUser's returned value reflects the
// membership it just granted without a second round trip to the store.
func replaceMembership(memberships []tenancy.Membership, m tenancy.Membership) []tenancy.Membership {
	for i, existing := range memberships {
		if existing.TenantID == m.TenantID {
			memberships[i] = m
			return memberships
		}
	}
	return append(memberships, m)
}

const systemActor = "cloudoptix/tenants"

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
func (s *Service) writeAudit(ctx context.Context, tenant core.TenantID, action audit.Action, actor string, subjectKind string, subjectID core.ID, message string, metadata map[string]any) {
	rec := audit.Record{
		TenantID: tenant, Action: action, Outcome: audit.OutcomeSuccess,
		Actor: actorLabel(actor), ActorMachine: actor == "",
		SubjectKind: subjectKind, SubjectID: subjectID,
		Message: message, Metadata: metadata, At: s.d.Clock.Now(),
	}
	if _, err := s.d.Audit.Append(ctx, rec); err != nil {
		s.d.Logger.Warn("tenants: writing audit record failed", "action", action, "error", err)
	}
}
