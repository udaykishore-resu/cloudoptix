package memstore

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// ctxFor builds a context carrying an authenticated principal scoped to
// tenant, the shape every repository method expects from core.GuardTenant.
func ctxFor(tenant core.TenantID, roles ...core.Role) context.Context {
	if len(roles) == 0 {
		roles = []core.Role{core.RoleTenantAdmin}
	}
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject:  "test-user",
		TenantID: tenant,
		Roles:    roles,
		IssuedAt: time.Now().UTC(),
	})
}

// platformAdminCtx builds a context for a cross-tenant platform operator.
func platformAdminCtx() context.Context {
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject:  "operator",
		TenantID: core.TenantID("platform"),
		Roles:    []core.Role{core.RolePlatformAdmin},
		IssuedAt: time.Now().UTC(),
	})
}
