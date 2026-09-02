package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// serviceClaims is the claim set a CloudOptix worker's own service token
// carries. It is a distinct Go type from Claims (the OIDC user claim set)
// even though the two overlap, because keeping them separate means a
// service token can never be fed to Validator.Validate and vice versa — the
// type system, not a runtime check, is what keeps the two trust domains
// apart.
type serviceClaims struct {
	jwt.RegisteredClaims
	TenantID  string `json:"tenant_id"`
	Component string `json:"component"`
}

// ServiceTokenIssuer mints and validates HS256 tokens for CloudOptix's own
// background workers (discovery, execution, notification) acting on a
// tenant's behalf. It is deliberately symmetric (HMAC) rather than asymmetric
// like the OIDC path: workers and the API server share one process boundary
// (the same deployment, the same secret store), so there is no need for the
// asymmetric split that lets an IdP sign without the relying party being
// able to forge tokens — and using a visibly different algorithm family is
// itself part of keeping this path structurally unable to be confused with
// the user-token path (see jwt.go's keyFunc, which only ever accepts
// RSA/ECDSA and would reject an HS256 token outright).
type ServiceTokenIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewServiceTokenIssuer builds an issuer/validator using secret as the HMAC
// key. secret must come from config.Secret.Value() (environment or a secret
// reference), never a literal in code — the caller enforces that by
// construction since config.Secret has no way to yield a value that did not
// go through Resolve.
func NewServiceTokenIssuer(secret string, ttl time.Duration) (*ServiceTokenIssuer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("auth: service token secret must be at least 32 bytes, got %d", len(secret))
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &ServiceTokenIssuer{secret: []byte(secret), ttl: ttl, now: time.Now}, nil
}

// Issue mints a token for one worker component acting on tenant's behalf.
func (s *ServiceTokenIssuer) Issue(tenant core.TenantID, component string) (string, error) {
	now := s.now()
	claims := serviceClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "cloudoptix/" + component,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
			Issuer:    "cloudoptix-internal",
		},
		TenantID:  tenant.String(),
		Component: component,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secret)
}

// Validate parses and validates a service token, returning the
// core.SystemPrincipal it authorizes. The allowlist here is exactly one
// algorithm (HS256) — there is no scenario where a service token needs to be
// validated against a family of algorithms the way a multi-provider OIDC
// deployment does, so the parser is built with WithValidMethods([]string{"HS256"})
// rather than accepting whatever NewServiceTokenIssuer happened to sign
// with, closing the same "accept what the token claims" gap the OIDC
// validator closes for RS256/ES256.
func (s *ServiceTokenIssuer) Validate(tokenString string) (core.Principal, error) {
	var claims serviceClaims
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer("cloudoptix-internal"),
		jwt.WithTimeFunc(s.now),
		jwt.WithExpirationRequired(),
	)
	token, err := parser.ParseWithClaims(tokenString, &claims, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	})
	if err != nil || !token.Valid {
		return core.Principal{}, core.NewError(core.ErrUnauthenticated, "invalid_service_token", "service token validation failed").Wrap(err)
	}
	if claims.TenantID == "" {
		return core.Principal{}, core.NewError(core.ErrUnauthenticated, "invalid_service_token", "service token carries no tenant_id")
	}
	p := core.SystemPrincipal(core.TenantID(claims.TenantID), claims.Component)
	if claims.IssuedAt != nil {
		p.IssuedAt = claims.IssuedAt.Time
	}
	if claims.ExpiresAt != nil {
		p.ExpiresAt = claims.ExpiresAt.Time
	}
	return p, nil
}
