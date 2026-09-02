package tenants

import (
	"context"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-tenants-test")

var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject: "test-admin", TenantID: tenant, Roles: []core.Role{core.RoleTenantAdmin}, IssuedAt: testNow,
	})
}

func newTestService(t interface{ Helper() }) (*Service, ports.Repositories) {
	t.Helper()
	repos := memstore.New().Repositories()
	svc, err := NewService(Deps{
		Tenants: repos.Tenants, Users: repos.Users, Audit: repos.Audit,
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})
	if err != nil {
		panic(err) // programmer error in the test fixture, not a real assertion failure
	}
	return svc, repos
}

func seedTenant(t interface{ Helper() }, repos ports.Repositories, tenant core.TenantID) tenancy.Tenant {
	t.Helper()
	tn := tenancy.Tenant{
		ID: tenant, Slug: "acme", Name: "Acme Corp", Plan: tenancy.PlanStandard,
		Quotas: tenancy.QuotasFor(tenancy.PlanStandard), State: tenancy.StateActive,
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	if err := repos.Tenants.Create(ctxFor(tenant), tn); err != nil {
		panic(err)
	}
	return tn
}

// seedUser creates a user with an active membership in tenant holding roles.
func seedUser(t interface{ Helper() }, repos ports.Repositories, tenant core.TenantID, email string, roles []core.Role) tenancy.User {
	t.Helper()
	u := tenancy.User{ID: core.NewID("usr"), Subject: "subj-" + email, Email: email, CreatedAt: testNow, UpdatedAt: testNow}
	if err := repos.Users.Upsert(ctxFor(tenant), u); err != nil {
		panic(err)
	}
	m := tenancy.Membership{TenantID: tenant, Roles: roles, GrantedBy: "seed", GrantedAt: testNow}
	if err := repos.Users.AddMembership(ctxFor(tenant), u.ID, m); err != nil {
		panic(err)
	}
	u.Memberships = []tenancy.Membership{m}
	return u
}
