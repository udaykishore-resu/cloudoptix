// Package tenants implements ports.TenantService: administering a tenant's
// own record and the users who belong to it.
//
// KEY DESIGN DECISION: two invariants are enforced here rather than left to
// the caller, because both protect the platform's own security model rather
// than a single tenant's data. First, Update refuses to change a tenant's
// id, slug or demo flag — the slug is baked into every cache key,
// object-storage prefix and generated IAM role name that already reference
// this tenant (see internal/application/iampolicy.RoleName), and the demo
// flag is the sole gate that permits simulated AWS access (see
// tenancy.Tenant.CanConnectAWS and internal/application/awsaccounts), so
// either changing silently under an otherwise-ordinary "update my tenant"
// call would be a structural inconsistency, not a business decision a
// tenant admin gets to make. Second, no tenant may end up with zero
// tenant_admin members: RemoveUser and UpdateRoles both refuse to strip the
// last one, and InviteUser/UpdateRoles both refuse to grant platform_admin
// — a cross-tenant operator role — from inside a tenant's own user
// management, because granting it here would let a tenant admin escalate
// their own access to every other customer's data.
//
// Traceability: REQ-TEN-001..008, SPEC-SEC-002, SPEC-SEC-003.
package tenants
