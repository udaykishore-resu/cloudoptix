package http

import (
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestNewRouter_BuildsWithoutPanicking is the structural smoke test every
// other routing test depends on: chi panics at registration time (not at
// request time) if two routes conflict — an inconsistent wildcard name at
// the same tree position, a duplicate method+pattern — so simply
// constructing the router with a zero-value ports.Services (every handler
// method here is reached only via chi.Mux.Match, which never invokes it) is
// itself a meaningful assertion about the whole route table in routes.go.
func TestNewRouter_BuildsWithoutPanicking(t *testing.T) {
	require.NotPanics(t, func() {
		_ = NewRouter(Deps{Services: ports.Services{}}, nil)
	})
}

// TestRouteTable_MatchesRegisteredRoutes walks the constructed router and
// confirms every entry in BuildRoutes and BuildPublicRoutes actually
// resolves via chi's structural matcher (Mux.Match) — this never invokes a
// handler, so it needs no fake services, only a route to exist.
func TestRouteTable_MatchesRegisteredRoutes(t *testing.T) {
	router := NewRouter(Deps{Services: ports.Services{}}, nil)
	mux, ok := router.(*chi.Mux)
	require.True(t, ok, "NewRouter must return a *chi.Mux for Match to work")

	for _, rt := range BuildRoutes(ports.Services{}) {
		rctx := chi.NewRouteContext()
		matched := mux.Match(rctx, rt.Method, "/api/v1"+testPatternForMatch(rt.Pattern))
		require.Truef(t, matched, "%s %s %s did not match any registered route", rt.Name, rt.Method, rt.Pattern)
	}
	for _, rt := range BuildPublicRoutes(ports.Services{}) {
		rctx := chi.NewRouteContext()
		matched := mux.Match(rctx, rt.Method, "/api/v1"+testPatternForMatch(rt.Pattern))
		require.Truef(t, matched, "%s %s %s did not match any registered route", rt.Name, rt.Method, rt.Pattern)
	}
}

// testPatternForMatch substitutes every {param} placeholder with a literal
// value so Match is exercised against a realistic path, not the pattern
// syntax itself.
func testPatternForMatch(pattern string) string {
	out := []byte(pattern)
	result := ""
	depth := 0
	start := 0
	for i, b := range out {
		switch b {
		case '{':
			depth++
			if depth == 1 {
				result += string(out[start:i])
				start = i
			}
		case '}':
			depth--
			if depth == 0 {
				result += "sample-value"
				start = i + 1
			}
		}
	}
	result += string(out[start:])
	return result
}

// TestRouteTable_NoDuplicateMethodPattern guards against copy-paste errors
// in routes.go: two entries registering the same method and pattern would
// silently let the second shadow the first at the handler level (chi itself
// permits it), which is exactly the kind of mistake a data-driven table
// makes easy to introduce and easy to check for.
func TestRouteTable_NoDuplicateMethodPattern(t *testing.T) {
	seen := map[string]string{}
	for _, rt := range BuildRoutes(ports.Services{}) {
		key := rt.Method + " " + rt.Pattern
		require.NotContainsf(t, seen, key, "duplicate route %s (first registered as %q, again as %q)", key, seen[key], rt.Name)
		seen[key] = rt.Name
	}
}

// TestRouteTable_EveryMutatingRouteHasPermission is the RBAC-exhaustiveness
// check: a route that mutates state and declares no permission would be
// reachable by any authenticated principal regardless of role, which
// RequirePermission("") treats as "authentication alone is enough" — correct
// for a small, deliberate allowlist (there is none among the mutating
// routes today) and a bug everywhere else.
func TestRouteTable_EveryMutatingRouteHasPermission(t *testing.T) {
	for _, rt := range BuildRoutes(ports.Services{}) {
		if !isMutating(rt.Method) {
			continue
		}
		require.NotEmptyf(t, rt.Permission, "%s %s (%s) mutates but declares no permission", rt.Method, rt.Pattern, rt.Name)
	}
}

func TestRouteTable_RouteCount(t *testing.T) {
	t.Logf("authenticated routes: %d, public routes: %d", len(BuildRoutes(ports.Services{})), len(BuildPublicRoutes(ports.Services{})))
}
