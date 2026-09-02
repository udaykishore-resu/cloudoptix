package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func withPrincipal(p core.Principal) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	return req.WithContext(core.WithPrincipal(req.Context(), p))
}

func TestRequirePermission_EmptyPermissionOnlyNeedsAuthentication(t *testing.T) {
	called := false
	handler := RequirePermission("")(func(w http.ResponseWriter, r *http.Request) { called = true })
	rec := httptest.NewRecorder()
	handler(rec, withPrincipal(core.Principal{Subject: "u1", TenantID: "tn_1", Roles: []core.Role{core.RoleViewer}}))
	require.True(t, called)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestRequirePermission_NoPrincipalIs401(t *testing.T) {
	handler := RequirePermission(core.PermCostRead)(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be reached without a principal")
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequirePermission_DeniesEveryRoleLackingThePermission(t *testing.T) {
	// PermExecutionStart is granted only to tenant_admin and sre (see
	// core.rolePermissions) — every other role must be denied with 403, and
	// the handler must never run for them.
	allRoles := []core.Role{
		core.RolePlatformAdmin, core.RoleTenantAdmin, core.RoleArchitect, core.RoleFinOpsAnalyst,
		core.RoleSRE, core.RoleDeveloper, core.RoleAuditor, core.RoleViewer,
	}
	for _, role := range allRoles {
		t.Run(string(role), func(t *testing.T) {
			called := false
			handler := RequirePermission(core.PermExecutionStart)(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			p := core.Principal{Subject: "u1", TenantID: "tn_1", Roles: []core.Role{role}}
			rec := httptest.NewRecorder()
			handler(rec, withPrincipal(p))

			if p.Can(core.PermExecutionStart) {
				require.True(t, called, "role %s should be granted PermExecutionStart", role)
				require.Equal(t, http.StatusOK, rec.Code)
			} else {
				require.False(t, called, "role %s must not reach the handler for PermExecutionStart", role)
				require.Equal(t, http.StatusForbidden, rec.Code)
			}
		})
	}
}

// TestRequirePermission_AuditorCanReadButNeverMutate is the platform's
// stated invariant (core.rolePermissions' doc comment): an auditor with
// stolen credentials can read everything, including the audit log, but
// cannot change anything at all.
func TestRequirePermission_AuditorCanReadButNeverMutate(t *testing.T) {
	auditor := core.Principal{Subject: "u1", TenantID: "tn_1", Roles: []core.Role{core.RoleAuditor}}
	readOnly := []core.Permission{core.PermCostRead, core.PermAuditRead, core.PermResourceRead, core.PermRecommendRead}
	for _, perm := range readOnly {
		require.True(t, auditor.Can(perm), "auditor should be able to %s", perm)
	}
	mutating := []core.Permission{
		core.PermExecutionStart, core.PermExecutionCancel, core.PermRollbackStart,
		core.PermPolicyWrite, core.PermApprovalDecide, core.PermSpecWrite, core.PermAutomationWrite,
	}
	for _, perm := range mutating {
		require.False(t, auditor.Can(perm), "auditor must not be able to %s", perm)
	}
}

// TestRoutes_PermissionMatchesHandlerAuthorizeCall spot-checks that a
// handful of routes.go's declared Permission agrees with what the handler
// itself checks via authorize() — the two are independent (see
// handlers_common.go's doc comment on defence in depth) and drifting apart
// would mean RBAC and the handler disagree about who may call a route, which
// is exactly the kind of gap the defence-in-depth design exists to catch,
// not create.
func TestRoutes_PermissionMatchesHandlerAuthorizeCall(t *testing.T) {
	routes := BuildRoutes(ports.Services{})
	byName := map[string]Route{}
	for _, r := range routes {
		byName[r.Name] = r
	}
	cases := map[string]core.Permission{
		"executions.execute":      core.PermExecutionStart,
		"executions.rollback":     core.PermRollbackStart,
		"approvals.decide":        core.PermApprovalDecide,
		"policies.save":           core.PermPolicyWrite,
		"recommendations.dismiss": core.PermRecommendRun,
		"aws_accounts.register":   core.PermAWSConnect,
		"discovery.run":           core.PermDiscoveryRun,
		"tenants.update":          core.PermTenantAdmin,
		"cost_slos.upsert":        core.PermSLOWrite,
	}
	for name, want := range cases {
		rt, ok := byName[name]
		require.Truef(t, ok, "route %q not found in BuildRoutes", name)
		require.Equalf(t, want, rt.Permission, "route %q declared permission mismatch", name)
	}
}
