package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/auth"
)

// testDeps builds a Deps whose authentication path is the fixed
// development static token — deterministic, no network, no JWKS — so
// middleware tests exercise the real authenticationMiddleware end to end
// rather than a hand-rolled substitute for it.
func testDeps(t *testing.T, tenant core.TenantID, roles ...core.Role) (Deps, string) {
	t.Helper()
	const token = "test-dev-token"
	issuer, err := auth.NewDevIssuer("development", token, tenant, roles)
	require.NoError(t, err)
	return Deps{Auth: &auth.Authenticator{Dev: issuer}}, token
}

func echoHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := MustPrincipal(r.Context())
		WriteJSON(w, http.StatusOK, map[string]string{"subject": p.Subject, "tenant": p.TenantID.String()})
	}
}

func TestAuthenticationMiddleware_MissingCredentialsIs401(t *testing.T) {
	deps, _ := testDeps(t, core.TenantID("tn_1"), core.RoleViewer)
	handler := Chain(deps, echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, problemContentType, rec.Header().Get("Content-Type"))
}

func TestAuthenticationMiddleware_InvalidTokenIs401(t *testing.T) {
	deps, _ := testDeps(t, core.TenantID("tn_1"), core.RoleViewer)
	handler := Chain(deps, echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthenticationMiddleware_ValidTokenAttachesPrincipal(t *testing.T) {
	deps, token := testDeps(t, core.TenantID("tn_1"), core.RoleViewer)
	handler := Chain(deps, echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "tn_1")
}

// TestAuthenticationMiddleware_NoAuthConfiguredIs503 confirms a deployment
// that forgot to wire an Authenticator fails obviously (503, a clear
// "auth_not_configured" code) rather than admitting every request or
// panicking on a nil pointer.
func TestAuthenticationMiddleware_NoAuthConfiguredIs503(t *testing.T) {
	handler := Chain(Deps{}, echoHandler())
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestPublicChain_NeverRequiresAuthentication confirms PublicChain really
// does skip Authentication/TenantResolved — the whole point of using it for
// onboarding — by running a handler that never even looks at the context
// through it with no credentials at all.
func TestPublicChain_NeverRequiresAuthentication(t *testing.T) {
	called := false
	handler := PublicChain(Deps{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/onboarding", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.True(t, called)
	require.Equal(t, http.StatusOK, rec.Code)
}
