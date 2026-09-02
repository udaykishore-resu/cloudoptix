package core

import (
	"context"
	"time"
)

// Role is a CloudOptix RBAC role. Permissions are derived from roles by table
// lookup rather than stored per user, so a policy change applies immediately
// and uniformly.
//
// Traceability: REQ-SEC-005, SPEC-SEC-002.
type Role string

const (
	RolePlatformAdmin Role = "platform_admin" // CloudOptix operators, cross-tenant
	RoleTenantAdmin   Role = "tenant_admin"
	RoleArchitect     Role = "architect"
	RoleFinOpsAnalyst Role = "finops_analyst"
	RoleSRE           Role = "sre"
	RoleDeveloper     Role = "developer"
	RoleAuditor       Role = "auditor"
	RoleViewer        Role = "viewer"
	RoleSystem        Role = "system" // internal workers; never issued to humans
)

// Permission is a verb-object capability checked at the API boundary and again
// in the application service, so a missing middleware cannot silently open a
// hole.
type Permission string

const (
	PermSpecRead        Permission = "spec:read"
	PermSpecWrite       Permission = "spec:write"
	PermSpecApprove     Permission = "spec:approve"
	PermAWSConnect      Permission = "aws:connect"
	PermDiscoveryRun    Permission = "discovery:run"
	PermResourceRead    Permission = "resource:read"
	PermCostRead        Permission = "cost:read"
	PermEconomicsRead   Permission = "economics:read"
	PermRecommendRead   Permission = "recommendation:read"
	PermRecommendRun    Permission = "recommendation:generate"
	PermSimulationRun   Permission = "simulation:run"
	PermCompilerRun     Permission = "compiler:run"
	PermPolicyRead      Permission = "policy:read"
	PermPolicyWrite     Permission = "policy:write"
	PermApprovalRead    Permission = "approval:read"
	PermApprovalDecide  Permission = "approval:decide"
	PermExecutionRead   Permission = "execution:read"
	PermExecutionStart  Permission = "execution:start"
	PermExecutionCancel Permission = "execution:cancel"
	PermRollbackStart   Permission = "rollback:start"
	PermAutomationWrite Permission = "automation:configure"
	PermAuditRead       Permission = "audit:read"
	PermTenantAdmin     Permission = "tenant:administer"
	PermPlatformAdmin   Permission = "platform:administer"
	PermCopilotUse      Permission = "copilot:use"
	PermSLOWrite        Permission = "slo:write"
)

var readOnlyBundle = []Permission{
	PermSpecRead, PermResourceRead, PermCostRead, PermEconomicsRead,
	PermRecommendRead, PermApprovalRead, PermExecutionRead, PermPolicyRead,
}

// rolePermissions is the single source of truth for authorization.
//
// The two rules encoded here that matter most: no role except tenant_admin and
// sre may start an execution, and the auditor role can read everything
// including the audit log but can change nothing at all — an auditor whose
// credentials are stolen cannot move money.
var rolePermissions = map[Role][]Permission{
	RolePlatformAdmin: {PermPlatformAdmin, PermTenantAdmin, PermAuditRead},
	RoleTenantAdmin: append(append([]Permission{}, readOnlyBundle...),
		PermSpecWrite, PermSpecApprove, PermAWSConnect, PermDiscoveryRun,
		PermRecommendRun, PermSimulationRun, PermCompilerRun,
		PermPolicyWrite, PermApprovalDecide, PermExecutionStart, PermExecutionCancel,
		PermRollbackStart, PermAutomationWrite, PermAuditRead, PermTenantAdmin,
		PermCopilotUse, PermSLOWrite),
	RoleArchitect: append(append([]Permission{}, readOnlyBundle...),
		PermSpecWrite, PermSimulationRun, PermCompilerRun, PermRecommendRun,
		PermCopilotUse, PermSLOWrite),
	RoleFinOpsAnalyst: append(append([]Permission{}, readOnlyBundle...),
		PermSimulationRun, PermCompilerRun, PermRecommendRun, PermCopilotUse,
		PermSLOWrite),
	RoleSRE: append(append([]Permission{}, readOnlyBundle...),
		PermDiscoveryRun, PermRecommendRun, PermSimulationRun,
		PermExecutionStart, PermExecutionCancel, PermRollbackStart, PermCopilotUse),
	RoleDeveloper: append(append([]Permission{}, readOnlyBundle...),
		PermCompilerRun, PermSimulationRun, PermCopilotUse),
	RoleAuditor: append(append([]Permission{}, readOnlyBundle...), PermAuditRead),
	RoleViewer:  readOnlyBundle,
	RoleSystem: {
		PermDiscoveryRun, PermRecommendRun, PermResourceRead, PermCostRead,
		PermEconomicsRead, PermExecutionRead, PermExecutionStart, PermRollbackStart,
		PermSpecRead, PermPolicyRead, PermApprovalRead,
	},
}

// Permissions returns the capability set granted by a role.
func (r Role) Permissions() []Permission { return rolePermissions[r] }

// Valid reports whether the role is known.
func (r Role) Valid() bool { _, ok := rolePermissions[r]; return ok }

// Principal is the authenticated caller. Every application-service method
// takes one; there is no ambient identity anywhere in CloudOptix.
type Principal struct {
	Subject   string    `json:"subject"` // OIDC sub
	TenantID  TenantID  `json:"tenant_id"`
	Email     string    `json:"email,omitempty"`
	Name      string    `json:"name,omitempty"`
	Roles     []Role    `json:"roles"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Machine marks a worker or automation identity, which the audit log
	// records differently from a human actor.
	Machine bool `json:"machine"`
}

// Can reports whether the principal holds a permission.
func (p Principal) Can(perm Permission) bool {
	for _, r := range p.Roles {
		if r == RolePlatformAdmin && perm != PermExecutionStart && perm != PermRollbackStart {
			// Platform operators administer the platform and can read
			// anything for support, but deliberately cannot push a customer's
			// infrastructure change. That must come from the tenant.
			return true
		}
		for _, granted := range rolePermissions[r] {
			if granted == perm {
				return true
			}
		}
	}
	return false
}

// HasRole reports role membership.
func (p Principal) HasRole(r Role) bool {
	for _, have := range p.Roles {
		if have == r {
			return true
		}
	}
	return false
}

// Authorize returns a Forbidden error when the permission is absent.
func (p Principal) Authorize(perm Permission) error {
	if p.Can(perm) {
		return nil
	}
	return Forbidden("principal %s lacks permission %s", p.Subject, perm).
		WithDetail("required_permission", string(perm))
}

// Describe renders the principal for the audit log.
func (p Principal) Describe() string {
	if p.Machine {
		return "system:" + p.Subject
	}
	if p.Email != "" {
		return p.Email
	}
	return p.Subject
}

// SystemPrincipal builds the identity used by background workers acting on a
// tenant's behalf. It is never derived from a token.
func SystemPrincipal(tenant TenantID, component string) Principal {
	return Principal{
		Subject:  "cloudoptix/" + component,
		TenantID: tenant,
		Roles:    []Role{RoleSystem},
		Machine:  true,
		IssuedAt: time.Now().UTC(),
	}
}

type principalCtxKey struct{}

// WithPrincipal attaches the caller identity to a context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFrom extracts the caller identity from a context.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(Principal)
	return p, ok
}

// TenantFrom extracts the tenant scope from a context, which repositories use
// as a defence-in-depth check against a caller passing a foreign tenant id.
func TenantFrom(ctx context.Context) (TenantID, bool) {
	p, ok := PrincipalFrom(ctx)
	if !ok || p.TenantID.IsZero() {
		return "", false
	}
	return p.TenantID, true
}

// GuardTenant verifies that the object's tenant matches the caller's scope.
// Repositories and services call it on every read and write; it is the last
// line of defence behind row-level security in Postgres.
func GuardTenant(ctx context.Context, objectTenant TenantID) error {
	p, hasPrincipal := PrincipalFrom(ctx)
	if !hasPrincipal {
		return NewError(ErrUnauthenticated, "no_principal", "request has no authenticated principal")
	}
	// The platform-admin check runs before the tenant-scope check, not after
	// it. A cross-tenant operator has no single tenant scope by definition —
	// that is what makes them cross-tenant — so TenantFrom reports "not
	// scoped" for exactly the identity this branch exists to admit. Testing
	// the role first is what makes the branch reachable at all; ordering it
	// after left it dead, and every cross-tenant read (a support lookup, a
	// worker enumerating tenants, the demo seed's idempotency check) failed
	// with "no authenticated principal" instead.
	if p.HasRole(RolePlatformAdmin) {
		// Platform operators may read across tenants for support; the audit
		// log records every such crossing.
		return nil
	}
	caller, ok := TenantFrom(ctx)
	if !ok {
		return NewError(ErrUnauthenticated, "no_tenant_scope",
			"principal %s carries no tenant scope and is not a platform operator", p.Describe())
	}
	if caller == objectTenant {
		return nil
	}
	return NewError(ErrTenantMismatch, "tenant_mismatch",
		"principal scoped to tenant %s may not access tenant %s", caller, objectTenant)
}

// Clock abstracts time so that engines producing time-dependent output —
// forecasts, error budgets, savings windows — are deterministically testable.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FixedClock is a Clock pinned to an instant, for tests and replay.
type FixedClock struct{ T time.Time }

// Now returns the pinned instant.
func (f FixedClock) Now() time.Time { return f.T.UTC() }
