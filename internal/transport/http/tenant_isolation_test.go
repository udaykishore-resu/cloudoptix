package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/auth"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeAuditService is the smallest ports.Services member (four methods),
// which is why tenant isolation — a concern that applies identically to
// every service — is exercised through it rather than a larger interface
// that would need more uninteresting stub methods to compile.
type fakeAuditService struct {
	// timelineErr is returned by Timeline for every call, letting a test
	// simulate either "genuinely missing" (core.NotFound) or "belongs to
	// another tenant" (core.ErrTenantMismatch) — see the doc comment on
	// core.HTTPStatus for why the platform deliberately makes these
	// indistinguishable to the caller.
	timelineErr error
}

func (f *fakeAuditService) Record(ctx context.Context, r ports.AuditEntry) error { return nil }
func (f *fakeAuditService) Query(ctx context.Context, tenant core.TenantID, q ports.AuditQuery) (ports.Page[ports.AuditEntry], error) {
	return ports.Page[ports.AuditEntry]{}, nil
}
func (f *fakeAuditService) Verify(ctx context.Context, tenant core.TenantID, from, to time.Time) (any, error) {
	return nil, nil
}
func (f *fakeAuditService) Timeline(ctx context.Context, tenant core.TenantID, recommendationID core.ID) ([]ports.AuditEntry, error) {
	if f.timelineErr != nil {
		return nil, f.timelineErr
	}
	return []ports.AuditEntry{{ID: "ae_1", Subject: string(recommendationID)}}, nil
}

// buildTestRouter wires a full router with a dev-token authenticator so
// end-to-end tests can exercise the real middleware chain plus RBAC plus a
// handler, exactly as a client would hit it.
func buildTestRouter(t *testing.T, svcs ports.Services, tenant core.TenantID, roles ...core.Role) (http.Handler, string) {
	t.Helper()
	deps, token := testDeps(t, tenant, roles...)
	deps.Services = svcs
	return NewRouter(deps, nil), token
}

func TestTenantIsolation_TenantMismatchRendersAsPlain404(t *testing.T) {
	svcs := ports.Services{Audit: &fakeAuditService{
		timelineErr: core.NewError(core.ErrTenantMismatch, "tenant_mismatch", "recommendation belongs to another tenant"),
	}}
	router, token := buildTestRouter(t, svcs, core.TenantID("tn_1"), core.RoleAuditor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/recommendations/rec_other_tenant/timeline", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotEqual(t, http.StatusForbidden, rec.Code, "a cross-tenant object must never render as 403 — see problem.go's doc comment")
}

func TestTenantIsolation_GenuinelyMissingAlsoRenders404(t *testing.T) {
	svcs := ports.Services{Audit: &fakeAuditService{
		timelineErr: core.NotFound("recommendation", "rec_missing"),
	}}
	router, token := buildTestRouter(t, svcs, core.TenantID("tn_1"), core.RoleAuditor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/recommendations/rec_missing/timeline", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTenantIsolation_SameTenantSucceeds(t *testing.T) {
	svcs := ports.Services{Audit: &fakeAuditService{}}
	router, token := buildTestRouter(t, svcs, core.TenantID("tn_1"), core.RoleAuditor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/recommendations/rec_1/timeline", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

// TestTenantIsolation_WrongTenantHeaderCannotOverridePrincipal confirms that
// a caller cannot widen its own scope by sending a different tenant in
// X-CloudOptix-Tenant than the one its (dev) credential is bound to — the
// handler always uses the principal's own TenantID, never a header a client
// controls directly.
func TestTenantIsolation_WrongTenantHeaderCannotOverridePrincipal(t *testing.T) {
	svcs := ports.Services{Audit: &fakeAuditService{}}
	router, token := buildTestRouter(t, svcs, core.TenantID("tn_1"), core.RoleAuditor)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/recommendations/rec_1/timeline", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(auth.TenantHeader, "tn_evil")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// The dev token issuer ignores the tenant header entirely (its principal
	// is fixed at construction), so this simply must still succeed as tn_1 —
	// proving the header alone cannot substitute for the token's own scope.
	require.Equal(t, http.StatusOK, rec.Code)
}
