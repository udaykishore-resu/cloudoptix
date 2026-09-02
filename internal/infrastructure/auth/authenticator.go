package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Authenticator is the single entry point the transport layer calls: given
// an inbound request, it returns the authenticated, tenant-scoped principal
// or an error. It composes every credential path this package supports, so
// the authentication middleware itself stays a thin adapter over one method
// rather than re-implementing the bearer/API-key branching per deployment.
type Authenticator struct {
	// OIDC validates user bearer tokens. Nil in a deployment that has not
	// configured an identity provider (which Config.Validate refuses to
	// accept in production — see config.go).
	OIDC *Validator
	// Service validates the internal worker bearer tokens.
	Service *ServiceTokenIssuer
	// Dev validates the fixed local-development token. Nil everywhere except
	// an explicitly opted-in local deployment.
	Dev *DevIssuer
	// APIKeys validates the X-CloudOptix-Api-Key header. Nil disables the
	// API-key path entirely.
	APIKeys APIKeyStore
	// APIKeyTTL bounds how long the principal resolved from an API key is
	// considered valid before the caller must present the key again (which,
	// unlike a JWT, has no expiry claim of its own). Zero means no expiry is
	// stamped — the key's validity is governed entirely by APIKeys.Lookup /
	// revocation.
	APIKeyTTL time.Duration

	Users UserLookup
	Clock core.Clock

	// APIKeyHeader names the header APIKeys validation reads from.
	APIKeyHeader string
}

// AuthResult is what a successful authentication produces: the principal,
// and which path produced it, which the audit middleware records (actor
// machine/human split) and which the RBAC layer's system-principal fast path
// checks.
type AuthResult struct {
	Principal core.Principal
	Method    string // "oidc" | "service_token" | "dev_token" | "api_key"
}

// Authenticate inspects the request's Authorization header and API-key
// header (in that order — a request presenting both is unusual enough that
// picking a fixed, documented order beats silently preferring whichever this
// implementation happens to check first) and returns the resolved principal.
func (a *Authenticator) Authenticate(r *http.Request) (AuthResult, error) {
	ctx := r.Context()
	tenantHeader := r.Header.Get(TenantHeader)

	if bearer, ok := bearerToken(r); ok {
		return a.authenticateBearer(ctx, bearer, tenantHeader)
	}
	if key := r.Header.Get(a.headerName()); key != "" {
		return a.authenticateAPIKey(ctx, key)
	}
	return AuthResult{}, core.NewError(core.ErrUnauthenticated, "no_credentials",
		"request carries no Authorization bearer token and no %s header", a.headerName())
}

func (a *Authenticator) headerName() string {
	if a.APIKeyHeader != "" {
		return a.APIKeyHeader
	}
	return "X-CloudOptix-Api-Key"
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	return tok, tok != ""
}

// authenticateBearer tries, in order: the internal service-token validator
// (workers), the dev static-token issuer (if configured), then the OIDC
// validator. Order matters here for a subtle reason: a service token and a
// dev token are both short, fixed-shape secrets that are cheap to check and
// whose failure carries no network cost, so trying them first means a
// misrouted or forged token fails fast without an unnecessary JWKS round
// trip; a well-formed OIDC JWT will simply fail the service-token and dev
// checks immediately (wrong signing method / wrong value) and fall through.
func (a *Authenticator) authenticateBearer(ctx context.Context, token, tenantHeader string) (AuthResult, error) {
	if a.Service != nil {
		if p, err := a.Service.Validate(token); err == nil {
			return AuthResult{Principal: p, Method: "service_token"}, nil
		}
	}
	if a.Dev != nil {
		if p, err := a.Dev.Validate(token); err == nil {
			return AuthResult{Principal: p, Method: "dev_token"}, nil
		}
	}
	if a.OIDC == nil {
		return AuthResult{}, core.NewError(core.ErrUnauthenticated, "oidc_not_configured", "no OIDC validator configured for this deployment")
	}
	claims, err := a.OIDC.Validate(ctx, token)
	if err != nil {
		return AuthResult{}, err
	}
	if a.Users == nil {
		return AuthResult{}, fmt.Errorf("auth: authenticator has no UserLookup configured")
	}
	principal, err := ResolveTenant(ctx, claims, tenantHeader, a.Users, a.Clock)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Principal: principal, Method: "oidc"}, nil
}

func (a *Authenticator) authenticateAPIKey(ctx context.Context, key string) (AuthResult, error) {
	if a.APIKeys == nil {
		return AuthResult{}, core.NewError(core.ErrUnauthenticated, "api_key_not_configured", "no API key store configured for this deployment")
	}
	p, err := ValidateAPIKey(ctx, a.APIKeys, key, a.APIKeyTTL)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Principal: p, Method: "api_key"}, nil
}
