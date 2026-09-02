package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// RequirePermission wraps handler with a check that the request's principal
// holds perm, returning 403 problem+json otherwise. It is what routes.go
// applies to every entry in the route table at registration time — see that
// file's Route.Permission field, which is the single source every mutating
// route's requirement is read from (and which rbac_test.go asserts is never
// empty for a mutating method).
//
// perm == "" means "authenticated is enough" — a small number of routes
// (whoami, the copilot suggestion list) have no narrower permission than
// simply being a logged-in member of the tenant.
func RequirePermission(perm core.Permission) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		if perm == "" {
			return next
		}
		return func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				WriteProblem(w, r, core.NewError(core.ErrUnauthenticated, "no_principal", "no authenticated principal"))
				return
			}
			if err := p.Authorize(perm); err != nil {
				WriteProblem(w, r, err)
				return
			}
			next(w, r)
		}
	}
}
