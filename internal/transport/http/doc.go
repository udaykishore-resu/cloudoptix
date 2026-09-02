// Package http is CloudOptix's HTTP API: a chi router mounting every
// operation the platform exposes under /api/v1, plus the unauthenticated
// operational endpoints (/healthz, /readyz, /metrics, /openapi.yaml).
//
// The design decision that shapes this package: authorization is a
// declarative table (routes.go), not scattered `if !principal.Can(...)`
// checks buried in handler bodies. Every route is registered from one slice
// of Route values that names its HTTP method, its pattern, and the single
// core.Permission required to reach it; RequirePermission wraps each
// handler with that check at registration time. This makes the entire
// authorization model readable in one file and mechanically testable — see
// rbac_test.go, which asserts every mutating route in the table carries a
// non-empty permission, a property that would otherwise only be
// discoverable by reading every handler. Handlers still re-check the same
// permission via core.Principal.Authorize before touching a service — not
// because the router might get the wrapping wrong, but because a handler
// reachable by a path the router does not know about (a future refactor, a
// mistake in Mount) must fail closed on its own.
//
// A second decision worth naming: no handler holds a repository. Every
// handler is constructed with exactly the ports.Services bundle (or one
// field of it), so the only way to reach persistence is through an
// application service that has already applied the tenant guard, the
// business rules, and the audit trail. internal/domain/audit.go's tamper
// -evident chain and internal/domain/core.GuardTenant are worth nothing if
// a handler can route around them by calling a repository directly, so
// this package's handler constructors are typed to make that impossible
// rather than merely policed by review.
//
// Traceability: REQ-API-001..050, SPEC-API-001..020, SPEC-SEC-004/005.
package http
