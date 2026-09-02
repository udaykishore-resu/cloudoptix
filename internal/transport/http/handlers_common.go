package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// authorize is the defence-in-depth permission check every handler in this
// package performs as its first action, independent of RequirePermission
// (rbac.go), which routes.go already wrapped the route with. The RBAC
// wrapper is what makes the authorization model readable and exhaustively
// testable in one table; this call is what keeps a handler safe even if it
// is ever reached by a path the table does not know about — a second
// registration, a future refactor of routes.go that drops the wrap by
// mistake. perm == "" means "authenticated is sufficient" (a handful of
// endpoints — whoami, the copilot suggestion list — have no narrower
// requirement). On failure it writes the problem+json response itself and
// returns ok=false; callers return immediately.
func authorize(w http.ResponseWriter, r *http.Request, perm core.Permission) (core.Principal, bool) {
	p := MustPrincipal(r.Context())
	if perm == "" {
		return p, true
	}
	if err := p.Authorize(perm); err != nil {
		WriteProblem(w, r, err)
		return core.Principal{}, false
	}
	return p, true
}

// respond writes v as JSON on success or renders err as problem+json — the
// one-line tail every handler in this package ends with.
func respond(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		WriteProblem(w, r, err)
		return
	}
	WriteJSON(w, status, v)
}

// decodeBody decodes and validates a JSON request body, writing a
// problem+json 400 and returning ok=false on failure.
func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	v, err := DecodeJSON[T](r)
	if err != nil {
		WriteProblem(w, r, err)
		var zero T
		return zero, false
	}
	return v, true
}
