package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestNewDevIssuer_RefusedInProduction(t *testing.T) {
	_, err := NewDevIssuer("production", "sometoken", "tenant-a", []core.Role{core.RoleViewer})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "production")
}

func TestNewDevIssuer_AllowedInDevelopment(t *testing.T) {
	issuer, err := NewDevIssuer("development", "sometoken", "tenant-a", []core.Role{core.RoleViewer})
	require.NoError(t, err)
	require.NotNil(t, issuer)
}

func TestNewDevIssuer_RequiresToken(t *testing.T) {
	_, err := NewDevIssuer("development", "", "tenant-a", []core.Role{core.RoleViewer})
	require.Error(t, err)
}

func TestNewDevIssuer_RequiresTenant(t *testing.T) {
	_, err := NewDevIssuer("development", "sometoken", "", []core.Role{core.RoleViewer})
	require.Error(t, err)
}

func TestDevIssuer_ValidatesExactToken(t *testing.T) {
	issuer, err := NewDevIssuer("development", "correct-token", "tenant-a", []core.Role{core.RoleTenantAdmin})
	require.NoError(t, err)

	p, err := issuer.Validate("correct-token")
	require.NoError(t, err)
	assert.Equal(t, core.TenantID("tenant-a"), p.TenantID)
	assert.True(t, p.HasRole(core.RoleTenantAdmin))

	_, err = issuer.Validate("wrong-token")
	require.Error(t, err)
}

func TestNewDevIssuer_TestEnvironmentIsAllowed(t *testing.T) {
	_, err := NewDevIssuer("test", "sometoken", "tenant-a", []core.Role{core.RoleViewer})
	require.NoError(t, err)
}

func TestNewDevIssuer_StagingIsAllowed(t *testing.T) {
	// Only "production" is refused; staging environments legitimately run
	// without a full IdP for smoke testing.
	_, err := NewDevIssuer("staging", "sometoken", "tenant-a", []core.Role{core.RoleViewer})
	require.NoError(t, err)
}
