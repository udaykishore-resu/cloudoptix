package auth

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// DevIssuer maps one fixed bearer token to one fixed core.Principal, for
// running the API against no identity provider at all during local
// development. It is not a scaled-down OIDC implementation — it does no
// parsing, no signature check, nothing token-shaped at all — because the
// point is to be obviously, structurally not the production path, not to be
// a "simple" version of it that could be mistaken for one.
type DevIssuer struct {
	token     string
	principal core.Principal
}

// NewDevIssuer builds a DevIssuer, or refuses to when environment is
// "production". This is the enforcement point for the hard requirement that
// the static-token path can never reach a production deployment: it is not
// enough for config.Validate to reject the combination (it does — see
// config.go) — the auth package itself refuses to construct the issuer, so
// a caller that skipped config validation, or wired things up by hand in a
// test, still cannot stand one up under environment=="production".
func NewDevIssuer(environment, token string, tenant core.TenantID, roles []core.Role) (*DevIssuer, error) {
	if environment == "production" {
		return nil, fmt.Errorf("auth: refusing to construct a development static-token issuer when environment is \"production\"")
	}
	if token == "" {
		return nil, fmt.Errorf("auth: development static token must not be empty")
	}
	if tenant.IsZero() {
		return nil, fmt.Errorf("auth: development static token requires a tenant")
	}
	now := time.Now().UTC()
	return &DevIssuer{
		token: token,
		principal: core.Principal{
			Subject: "dev-user", TenantID: tenant, Email: "dev@localhost", Name: "Local Developer",
			Roles: roles, IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		},
	}, nil
}

// Validate returns the fixed principal when tokenString matches the
// configured token, using a constant-time comparison so a timing side
// channel cannot narrow down the token even in a mode that is, admittedly,
// not meant to face real attackers — habits that only apply "when it
// matters" are the ones that get skipped when it turns out to matter.
func (d *DevIssuer) Validate(tokenString string) (core.Principal, error) {
	if !constantTimeEqual(tokenString, d.token) {
		return core.Principal{}, core.NewError(core.ErrUnauthenticated, "invalid_dev_token", "development token does not match")
	}
	p := d.principal
	p.IssuedAt = time.Now().UTC()
	p.ExpiresAt = p.IssuedAt.Add(24 * time.Hour)
	return p, nil
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
