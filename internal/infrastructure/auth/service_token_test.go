package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestServiceTokenIssuer_RoundTrip(t *testing.T) {
	issuer, err := NewServiceTokenIssuer("this-is-a-32-byte-or-longer-secret!!", time.Hour)
	require.NoError(t, err)

	token, err := issuer.Issue("tenant-a", "discovery-worker")
	require.NoError(t, err)

	p, err := issuer.Validate(token)
	require.NoError(t, err)
	assert.Equal(t, core.TenantID("tenant-a"), p.TenantID)
	assert.True(t, p.Machine)
	assert.True(t, p.HasRole(core.RoleSystem))
}

func TestServiceTokenIssuer_RejectsShortSecret(t *testing.T) {
	_, err := NewServiceTokenIssuer("too-short", time.Hour)
	require.Error(t, err)
}

func TestServiceTokenIssuer_RejectsTokenFromDifferentSecret(t *testing.T) {
	a, err := NewServiceTokenIssuer("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Hour)
	require.NoError(t, err)
	b, err := NewServiceTokenIssuer("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", time.Hour)
	require.NoError(t, err)

	token, err := a.Issue("tenant-a", "worker")
	require.NoError(t, err)

	_, err = b.Validate(token)
	require.Error(t, err)
}

func TestServiceTokenIssuer_RejectsExpiredToken(t *testing.T) {
	fixedNow := time.Now()
	issuer, err := NewServiceTokenIssuer("this-is-a-32-byte-or-longer-secret!!", time.Millisecond)
	require.NoError(t, err)
	issuer.now = func() time.Time { return fixedNow }

	token, err := issuer.Issue("tenant-a", "worker")
	require.NoError(t, err)

	issuer.now = func() time.Time { return fixedNow.Add(time.Hour) }
	_, err = issuer.Validate(token)
	require.Error(t, err)
}

// A service token must never validate through the OIDC Validator's keyFunc —
// they are different signing methods (HS256 vs RSA/ECDSA) by construction, so
// a worker's credential presented to the user-token path is rejected before
// any secret comparison happens at all.
func TestServiceToken_NeverValidatesAsOIDCToken(t *testing.T) {
	issuer, err := NewServiceTokenIssuer("this-is-a-32-byte-or-longer-secret!!", time.Hour)
	require.NoError(t, err)
	token, err := issuer.Issue("tenant-a", "worker")
	require.NoError(t, err)

	cache := NewJWKSCache("http://unused.example.com", time.Minute, nil)
	v, err := NewValidator(ValidatorConfig{
		Issuer: "https://issuer.example.com", AllowedAlgorithms: []string{"RS256"}, JWKS: cache,
	})
	require.NoError(t, err)

	_, err = v.Validate(context.Background(), token)
	require.Error(t, err)
}
