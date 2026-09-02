package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Claims is the JWT claim set CloudOptix expects from an OIDC identity
// provider. Unrecognised claims are ignored, not rejected — an IdP is free
// to add claims CloudOptix does not read.
type Claims struct {
	jwt.RegisteredClaims

	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	// TenantID, when present, is the tenant the IdP itself scopes the token
	// to (common when the IdP has a CloudOptix-specific application/client
	// per tenant). It is one of two inputs to tenant resolution — see
	// tenant.go — the other being the request's tenant header; a request
	// must agree with whichever of these are present, and membership is
	// always re-checked against the user record regardless.
	TenantID string `json:"tenant_id"`
}

// Validator validates OIDC-issued JWTs against a JWKS and a closed algorithm
// allowlist.
type Validator struct {
	issuer            string
	audience          string
	allowedAlgorithms []string
	clockSkew         time.Duration
	jwks              *JWKSCache
	now               func() time.Time
}

// ValidatorConfig configures a Validator.
type ValidatorConfig struct {
	Issuer   string
	Audience string
	// AllowedAlgorithms is a closed allowlist of JWS algorithm names (e.g.
	// "RS256", "ES256"). It must never contain "none", and Validator's
	// constructor refuses to build one that does — see NewValidator.
	AllowedAlgorithms []string
	ClockSkew         time.Duration
	JWKS              *JWKSCache
	// Now is injectable for deterministic expiry tests.
	Now func() time.Time
}

// NewValidator builds a Validator. It refuses to construct one whose
// allowlist includes "none" or is empty — those are not conservative
// defaults to fall back on, they are configurations equivalent to "accept
// any token", so failing at construction time is safer than validating
// tokens against a config that cannot reject anything.
func NewValidator(cfg ValidatorConfig) (*Validator, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("auth: validator requires an issuer")
	}
	if cfg.JWKS == nil {
		return nil, fmt.Errorf("auth: validator requires a JWKS cache")
	}
	if len(cfg.AllowedAlgorithms) == 0 {
		return nil, fmt.Errorf("auth: validator requires at least one allowed algorithm")
	}
	for _, alg := range cfg.AllowedAlgorithms {
		if strings.EqualFold(alg, "none") {
			return nil, fmt.Errorf("auth: %q must never be an allowed algorithm", alg)
		}
	}
	if cfg.ClockSkew <= 0 {
		cfg.ClockSkew = 2 * time.Minute
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Validator{
		issuer: cfg.Issuer, audience: cfg.Audience,
		allowedAlgorithms: cfg.AllowedAlgorithms, clockSkew: cfg.ClockSkew,
		jwks: cfg.JWKS, now: now,
	}, nil
}

// Validate parses and fully validates tokenString: signature (against the
// JWKS, by kid), algorithm (against the closed allowlist — this is also
// where "alg":"none" and algorithm confusion are rejected, see keyFunc),
// issuer, audience, and expiry (with clock skew).
func (v *Validator) Validate(ctx context.Context, tokenString string) (Claims, error) {
	var claims Claims
	parser := jwt.NewParser(
		jwt.WithValidMethods(v.allowedAlgorithms),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithLeeway(v.clockSkew),
		jwt.WithTimeFunc(v.now),
		// WithExpirationRequired: an IdP that forgets to set exp is not a
		// token CloudOptix should treat as long-lived by accident.
		jwt.WithExpirationRequired(),
	)
	token, err := parser.ParseWithClaims(tokenString, &claims, v.keyFunc(ctx))
	if err != nil {
		return Claims{}, core.NewError(core.ErrUnauthenticated, "invalid_token", "token validation failed").Wrap(err)
	}
	if !token.Valid {
		return Claims{}, core.NewError(core.ErrUnauthenticated, "invalid_token", "token is not valid")
	}
	return claims, nil
}

// keyFunc resolves the signing key for one token. The algorithm-confusion
// defence is here, not only in WithValidMethods: this function only ever
// returns an asymmetric public key, and it asserts the token's parsed
// method is one of the RSA/ECDSA families before doing so. WithValidMethods
// alone protects an allowlist that is purely asymmetric (CloudOptix's is —
// see NewValidator's refusal of "none", and the config-level ban on mixing
// this validator's algorithms with the service-token HMAC secret), but
// asserting the concrete Go type here means this function is safe even if a
// future change ever widened the allowlist to include an HMAC algorithm by
// mistake: it would still never hand an RSA/EC public key to an HMAC
// verifier, which is the actual mechanism of the classic attack.
func (v *Validator) keyFunc(ctx context.Context) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		switch token.Method.(type) {
		case *jwt.SigningMethodRSA, *jwt.SigningMethodECDSA:
			// fine — the only families this Keyfunc will ever serve a key for.
		default:
			return nil, fmt.Errorf("auth: refusing to validate token using algorithm %q (not RSA/ECDSA)", token.Method.Alg())
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("auth: token header carries no kid")
		}
		key, err := v.jwks.Key(ctx, kid)
		if err != nil {
			return nil, err
		}
		switch token.Method.(type) {
		case *jwt.SigningMethodRSA:
			if _, ok := key.(*rsa.PublicKey); !ok {
				return nil, fmt.Errorf("auth: kid %q is not an RSA key but token claims an RSA algorithm", kid)
			}
		case *jwt.SigningMethodECDSA:
			if _, ok := key.(*ecdsa.PublicKey); !ok {
				return nil, fmt.Errorf("auth: kid %q is not an EC key but token claims an ECDSA algorithm", kid)
			}
		}
		return key, nil
	}
}

// ToPrincipal maps validated claims onto a core.Principal scoped to the
// given tenant. It does not itself check tenant membership — call
// ResolveTenant (tenant.go) for the full, membership-checked resolution;
// this is the pure claim-to-struct mapping step it builds on.
func (c Claims) ToPrincipal(tenant core.TenantID) core.Principal {
	roles := make([]core.Role, 0, len(c.Roles))
	for _, r := range c.Roles {
		role := core.Role(r)
		// core.RoleSystem is documented as "never issued to humans": it is
		// only ever constructed by core.SystemPrincipal for the service-token
		// path (service_token.go). Honouring it from an OIDC roles claim
		// would let a misconfigured or compromised identity provider grant a
		// human session the same authority as an internal worker; refuse it
		// here regardless of what upstream claims.
		if role.Valid() && role != core.RoleSystem {
			roles = append(roles, role)
		}
	}
	var issuedAt, expiresAt time.Time
	if c.IssuedAt != nil {
		issuedAt = c.IssuedAt.Time
	}
	if c.ExpiresAt != nil {
		expiresAt = c.ExpiresAt.Time
	}
	return core.Principal{
		Subject:   c.Subject,
		TenantID:  tenant,
		Email:     c.Email,
		Name:      c.Name,
		Roles:     roles,
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
	}
}
