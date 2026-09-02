package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
)

type fakeUserLookup struct {
	users map[string]tenancy.User
}

func (f *fakeUserLookup) GetBySubject(ctx context.Context, subject string) (tenancy.User, error) {
	u, ok := f.users[subject]
	if !ok {
		return tenancy.User{}, core.NotFound("user", subject)
	}
	return u, nil
}

func memberUser(subject string, tenant core.TenantID, roles ...core.Role) tenancy.User {
	return tenancy.User{
		Subject: subject, Email: subject + "@example.com",
		Memberships: []tenancy.Membership{{TenantID: tenant, Roles: roles, GrantedAt: time.Now()}},
	}
}

func TestResolveTenant_UsesTokenClaimWhenHeaderAbsent(t *testing.T) {
	users := &fakeUserLookup{users: map[string]tenancy.User{
		"user-1": memberUser("user-1", "tenant-a", core.RoleViewer),
	}}
	claims := Claims{TenantID: "tenant-a"}
	claims.Subject = "user-1"

	p, err := ResolveTenant(context.Background(), claims, "", users, nil)
	require.NoError(t, err)
	assert.Equal(t, core.TenantID("tenant-a"), p.TenantID)
}

func TestResolveTenant_UsesHeaderWhenClaimAbsent(t *testing.T) {
	users := &fakeUserLookup{users: map[string]tenancy.User{
		"user-1": memberUser("user-1", "tenant-b", core.RoleViewer),
	}}
	claims := Claims{}
	claims.Subject = "user-1"

	p, err := ResolveTenant(context.Background(), claims, "tenant-b", users, nil)
	require.NoError(t, err)
	assert.Equal(t, core.TenantID("tenant-b"), p.TenantID)
}

func TestResolveTenant_MismatchBetweenClaimAndHeaderFails(t *testing.T) {
	users := &fakeUserLookup{users: map[string]tenancy.User{
		"user-1": memberUser("user-1", "tenant-a", core.RoleViewer),
	}}
	claims := Claims{TenantID: "tenant-a"}
	claims.Subject = "user-1"

	_, err := ResolveTenant(context.Background(), claims, "tenant-b", users, nil)
	require.ErrorIs(t, err, ErrTenantMismatch)
}

func TestResolveTenant_NoTenantSpecifiedAnywhereFails(t *testing.T) {
	users := &fakeUserLookup{}
	claims := Claims{}
	claims.Subject = "user-1"

	_, err := ResolveTenant(context.Background(), claims, "", users, nil)
	require.ErrorIs(t, err, ErrTenantHeaderMissing)
}

// This is the critical cross-tenant-isolation case: a token that is
// perfectly valid (right issuer, right audience, right signature) but whose
// claimed tenant the subject does not actually belong to must still be
// refused, even though nothing about the token's cryptographic validity
// failed.
func TestResolveTenant_ValidTokenButNoMembershipInClaimedTenantFails(t *testing.T) {
	users := &fakeUserLookup{users: map[string]tenancy.User{
		"user-1": memberUser("user-1", "tenant-a", core.RoleViewer), // only a member of tenant-a
	}}
	claims := Claims{TenantID: "tenant-b"} // claims membership in tenant-b
	claims.Subject = "user-1"

	_, err := ResolveTenant(context.Background(), claims, "", users, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
}

func TestResolveTenant_ExpiredMembershipFails(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	users := &fakeUserLookup{users: map[string]tenancy.User{
		"user-1": {
			Subject: "user-1",
			Memberships: []tenancy.Membership{
				{TenantID: "tenant-a", Roles: []core.Role{core.RoleViewer}, ExpiresAt: &past},
			},
		},
	}}
	claims := Claims{TenantID: "tenant-a"}
	claims.Subject = "user-1"

	_, err := ResolveTenant(context.Background(), claims, "", users, nil)
	require.Error(t, err)
}

func TestResolveTenant_DisabledUserFails(t *testing.T) {
	u := memberUser("user-1", "tenant-a", core.RoleViewer)
	u.Disabled = true
	users := &fakeUserLookup{users: map[string]tenancy.User{"user-1": u}}
	claims := Claims{TenantID: "tenant-a"}
	claims.Subject = "user-1"

	_, err := ResolveTenant(context.Background(), claims, "", users, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
}

func TestResolveTenant_UnknownSubjectFails(t *testing.T) {
	users := &fakeUserLookup{users: map[string]tenancy.User{}}
	claims := Claims{TenantID: "tenant-a"}
	claims.Subject = "ghost"

	_, err := ResolveTenant(context.Background(), claims, "", users, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrUnauthenticated)
}
