// Package tenancy models the platform's multi-tenant hierarchy and the
// identities inside it.
//
// Traceability: REQ-TEN-001..008, SPEC-SEC-003.
package tenancy

import (
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Plan is the commercial tier, which sets quotas rather than feature flags.
// CloudOptix does not withhold safety features by tier: every tenant gets
// policy, approvals, rollback and audit.
type Plan string

const (
	PlanTrial      Plan = "trial"
	PlanStandard   Plan = "standard"
	PlanEnterprise Plan = "enterprise"
	PlanInternal   Plan = "internal"
)

// Quotas bound a tenant's resource consumption on the platform.
type Quotas struct {
	MaxAWSAccounts         int   `json:"max_aws_accounts"`
	MaxResources           int   `json:"max_resources"`
	MaxConcurrentDiscovery int   `json:"max_concurrent_discovery"`
	MaxSimulationsPerDay   int   `json:"max_simulations_per_day"`
	MaxCopilotTokensPerDay int64 `json:"max_copilot_tokens_per_day"`
	MaxAutomationsPerDay   int   `json:"max_automations_per_day"`
	RetentionDays          int   `json:"retention_days"`
}

// QuotasFor returns the default quotas for a plan.
func QuotasFor(p Plan) Quotas {
	switch p {
	case PlanEnterprise, PlanInternal:
		return Quotas{
			MaxAWSAccounts: 500, MaxResources: 500_000, MaxConcurrentDiscovery: 16,
			MaxSimulationsPerDay: 500, MaxCopilotTokensPerDay: 20_000_000,
			MaxAutomationsPerDay: 500, RetentionDays: 1095,
		}
	case PlanStandard:
		return Quotas{
			MaxAWSAccounts: 50, MaxResources: 50_000, MaxConcurrentDiscovery: 6,
			MaxSimulationsPerDay: 100, MaxCopilotTokensPerDay: 4_000_000,
			MaxAutomationsPerDay: 100, RetentionDays: 400,
		}
	default:
		return Quotas{
			MaxAWSAccounts: 3, MaxResources: 5_000, MaxConcurrentDiscovery: 2,
			MaxSimulationsPerDay: 20, MaxCopilotTokensPerDay: 500_000,
			MaxAutomationsPerDay: 5, RetentionDays: 90,
		}
	}
}

// State is the tenant lifecycle.
type State string

const (
	StateOnboarding State = "onboarding" // conversation in progress, no spec approved
	StateActive     State = "active"
	StateSuspended  State = "suspended"
	StateArchived   State = "archived"
)

// Tenant is the isolation boundary. Every row in every table, every cache key,
// every event and every AI context is scoped to one.
type Tenant struct {
	ID   core.TenantID `json:"id"`
	Slug string        `json:"slug"`
	Name string        `json:"name"`

	Plan   Plan   `json:"plan"`
	Quotas Quotas `json:"quotas"`
	State  State  `json:"state"`

	// SpecID and ActiveSpecVersion point at the approved specification that
	// configures this tenant. A tenant with no approved specification cannot
	// connect an AWS account, which is how the spec-driven flow is enforced
	// structurally rather than by convention.
	SpecID            core.ID `json:"spec_id,omitempty"`
	ActiveSpecVersion int     `json:"active_spec_version,omitempty"`
	ActivePolicyID    core.ID `json:"active_policy_id,omitempty"`

	// Demo marks the built-in demonstration tenant, the only kind permitted to
	// use the simulated AWS access mode.
	Demo bool `json:"demo"`

	// DataRegion is where this tenant's data is stored, for residency
	// commitments made during onboarding.
	DataRegion string `json:"data_region"`
	// EncryptionKeyARN is the tenant's own KMS key when they bring one.
	EncryptionKeyARN core.ARN `json:"encryption_key_arn,omitempty"`

	PrimaryContact string     `json:"primary_contact,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	ActivatedAt    *time.Time `json:"activated_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// CanConnectAWS reports whether the tenant has cleared the gate that lets it
// attach a real AWS account.
func (t Tenant) CanConnectAWS() (bool, string) {
	if t.State == StateSuspended || t.State == StateArchived {
		return false, "tenant is " + string(t.State)
	}
	if t.SpecID.IsZero() || t.ActiveSpecVersion == 0 {
		return false, "no approved specification: onboarding must be completed and approved first"
	}
	return true, ""
}

// Validate enforces the tenant invariants.
func (t Tenant) Validate() error {
	var v core.ValidationResult
	if err := t.ID.Validate(); err != nil {
		v.Add("id", "invalid", core.SeverityCritical, "%v", err)
	}
	if strings.TrimSpace(t.Name) == "" {
		v.Add("name", "required", core.SeverityHigh, "tenant name is required")
	}
	if t.Slug == "" {
		v.Add("slug", "required", core.SeverityHigh, "tenant slug is required")
	}
	return v.Err()
}

// User is a person with access to one or more tenants.
type User struct {
	ID      core.ID `json:"id"`
	Subject string  `json:"subject"` // OIDC sub, the stable external identity
	Email   string  `json:"email"`
	Name    string  `json:"name,omitempty"`

	// Memberships are per-tenant role grants. A consultant working across
	// three customers has three memberships and one identity, and cannot see
	// two tenants in one session.
	Memberships []Membership `json:"memberships"`

	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Membership grants roles within one tenant.
type Membership struct {
	TenantID  core.TenantID `json:"tenant_id"`
	Roles     []core.Role   `json:"roles"`
	Team      string        `json:"team,omitempty"`
	GrantedBy string        `json:"granted_by,omitempty"`
	GrantedAt time.Time     `json:"granted_at"`
	// ExpiresAt supports time-boxed access, which is how break-glass and
	// contractor access are granted without becoming permanent.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Active reports whether the membership is currently in force.
func (m Membership) Active(now time.Time) bool {
	return m.ExpiresAt == nil || now.Before(*m.ExpiresAt)
}

// RolesIn returns the user's active roles in a tenant.
func (u User) RolesIn(tenant core.TenantID, now time.Time) []core.Role {
	if u.Disabled {
		return nil
	}
	for _, m := range u.Memberships {
		if m.TenantID == tenant && m.Active(now) {
			return m.Roles
		}
	}
	return nil
}

// Principal builds the authenticated principal for a tenant session.
func (u User) Principal(tenant core.TenantID, now time.Time) (core.Principal, error) {
	roles := u.RolesIn(tenant, now)
	if len(roles) == 0 {
		return core.Principal{}, core.Forbidden("user %s has no active membership in tenant %s", u.Email, tenant)
	}
	return core.Principal{
		Subject:  u.Subject,
		TenantID: tenant,
		Email:    u.Email,
		Name:     u.Name,
		Roles:    roles,
		IssuedAt: now,
	}, nil
}

// Organization is the customer-company record inside a tenant. A tenant
// normally has one, but a managed service provider tenant can hold several.
type Organization struct {
	ID              core.ID       `json:"id"`
	TenantID        core.TenantID `json:"tenant_id"`
	Name            string        `json:"name"`
	Industry        string        `json:"industry,omitempty"`
	Size            string        `json:"size,omitempty"`
	BusinessRegions []string      `json:"business_regions,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}
