package tenants

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestUpdate_RefusesImmutableFieldChanges(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)

	cases := map[string]tenancy.Tenant{
		"slug changed": {ID: testTenant, Slug: "not-acme", Name: "Acme Corp"},
		"demo changed": {ID: testTenant, Slug: "acme", Name: "Acme Corp", Demo: true},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Update(ctxFor(testTenant), edit, "admin@example.com")
			require.Error(t, err)
			assert.ErrorIs(t, err, core.ErrInvalidInput)
		})
	}
}

func TestUpdate_AppliesMutableFieldsAndRecomputesQuotas(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)

	edit := tenancy.Tenant{
		ID: testTenant, Slug: "acme", Name: "Acme Corporation", Plan: tenancy.PlanEnterprise,
		Quotas: tenancy.Quotas{MaxAWSAccounts: 999999}, // must be ignored, not trusted from the caller
	}
	updated, err := svc.Update(ctxFor(testTenant), edit, "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, "Acme Corporation", updated.Name)
	assert.Equal(t, tenancy.PlanEnterprise, updated.Plan)
	assert.Equal(t, tenancy.QuotasFor(tenancy.PlanEnterprise), updated.Quotas)
}

func TestInviteUser_CreatesUserAndGrantsRoles(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)

	u, err := svc.InviteUser(ctxFor(testTenant), testTenant, "dev@example.com", []core.Role{core.RoleDeveloper}, "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, "dev@example.com", u.Email)
	require.Len(t, u.Memberships, 1)
	assert.Equal(t, []core.Role{core.RoleDeveloper}, u.Memberships[0].Roles)

	page, err := svc.ListUsers(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestInviteUser_RefusesPlatformAdmin(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)

	_, err := svc.InviteUser(ctxFor(testTenant), testTenant, "sneaky@example.com", []core.Role{core.RolePlatformAdmin}, "admin@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
}

func TestUpdateRoles_RefusesPlatformAdmin(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)
	u := seedUser(t, repos, testTenant, "dev@example.com", []core.Role{core.RoleDeveloper})

	err := svc.UpdateRoles(ctxFor(testTenant), testTenant, u.ID, []core.Role{core.RolePlatformAdmin}, "admin@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
}

func TestUpdateRoles_RefusesDemotingLastTenantAdmin(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)
	admin := seedUser(t, repos, testTenant, "admin@example.com", []core.Role{core.RoleTenantAdmin})

	err := svc.UpdateRoles(ctxFor(testTenant), testTenant, admin.ID, []core.Role{core.RoleViewer}, "admin@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrConflict)
}

func TestUpdateRoles_AllowsDemotingWhenAnotherAdminExists(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)
	admin1 := seedUser(t, repos, testTenant, "admin1@example.com", []core.Role{core.RoleTenantAdmin})
	seedUser(t, repos, testTenant, "admin2@example.com", []core.Role{core.RoleTenantAdmin})

	err := svc.UpdateRoles(ctxFor(testTenant), testTenant, admin1.ID, []core.Role{core.RoleViewer}, "admin2@example.com")
	require.NoError(t, err)
}

func TestRemoveUser_RefusesRemovingLastTenantAdmin(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)
	admin := seedUser(t, repos, testTenant, "admin@example.com", []core.Role{core.RoleTenantAdmin})

	err := svc.RemoveUser(ctxFor(testTenant), testTenant, admin.ID, "admin@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrConflict)
}

func TestRemoveUser_AllowsRemovingWhenAnotherAdminExists(t *testing.T) {
	svc, repos := newTestService(t)
	seedTenant(t, repos, testTenant)
	admin1 := seedUser(t, repos, testTenant, "admin1@example.com", []core.Role{core.RoleTenantAdmin})
	seedUser(t, repos, testTenant, "admin2@example.com", []core.Role{core.RoleTenantAdmin})

	require.NoError(t, svc.RemoveUser(ctxFor(testTenant), testTenant, admin1.ID, "admin2@example.com"))

	page, err := svc.ListUsers(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1, "the remaining admin's membership must be untouched")
}
