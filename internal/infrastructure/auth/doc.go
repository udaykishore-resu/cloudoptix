// Package auth turns an inbound bearer token into a core.Principal scoped to
// one tenant.
//
// Three token shapes are accepted, and the design keeps them structurally
// distinct rather than funnelling everything through one lenient parser:
//
//   - An OIDC-issued JWT, validated against a JWKS fetched (and cached, and
//     rotated) from the tenant's identity provider. This is the path for
//     every human user.
//   - A service token: an HS256 JWT signed with a platform-held HMAC secret,
//     for the discovery/execution/notification workers acting as
//     core.SystemPrincipal. It is deliberately a different signing method
//     from the OIDC path so a worker credential can never be replayed as a
//     user credential or vice versa — see jwt.go's algorithm allowlist
//     handling.
//   - A development-mode static token, which maps one fixed bearer string to
//     one fixed principal for local iteration with no identity provider
//     running. NewDevIssuer refuses to construct when told the environment is
//     production — see devtoken.go — so this path cannot reach a real
//     deployment by a config mistake alone.
//
// The other property enforced throughout this package: the JWT parser's
// algorithm allowlist is explicit and closed (jwt.WithValidMethods), "none"
// is never in it, and the asymmetric (OIDC) and symmetric (service token)
// paths never share a Keyfunc — which is what actually prevents algorithm
// confusion (an attacker crafting an HS256 token and "signing" it with the
// server's public RSA key, which the server would otherwise accept as a
// valid HMAC secret). A single Keyfunc that returns either key type
// depending on the token's own claimed algorithm is the classic way this
// vulnerability gets reintroduced; this package never does that.
//
// Traceability: REQ-SEC-002 (authentication), REQ-SEC-003 (tenant
// isolation), SPEC-SEC-004.
package auth
