package specsvc

import (
	"context"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-specsvc-test")

var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// ctxFor builds a context carrying an authenticated principal scoped to
// tenant, defaulting to tenant_admin (which holds every permission
// ProposeRevision checks) so tests that are not specifically about
// authorization do not have to think about it.
func ctxFor(tenant core.TenantID, roles ...core.Role) context.Context {
	if len(roles) == 0 {
		roles = []core.Role{core.RoleTenantAdmin}
	}
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject: "test-user", TenantID: tenant, Roles: roles, IssuedAt: testNow,
	})
}

// newTestService wires a Service against a fresh in-memory Store, returning
// both so a test can seed fixtures directly through the repositories.
func newTestService(t interface{ Helper() }) (*Service, *memstore.Store, ports.Repositories) {
	t.Helper()
	store := memstore.New()
	repos := store.Repositories()
	svc, err := NewService(Deps{
		Specs: repos.Specs, Tenants: repos.Tenants, Audit: repos.Audit, UoW: store,
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})
	if err != nil {
		panic(err) // programmer error in the test fixture, not a real assertion failure
	}
	return svc, store, repos
}

// baseSpec returns a specification that passes spec.Validate with no
// blocking issues, the starting point every test mutates from.
func baseSpec() spec.Spec {
	var sp spec.Spec
	sp.APIVersion = spec.CurrentAPIVersion
	sp.Kind = spec.KindSpec
	sp.Organization.Name = "Acme Corp"
	sp.Application.Name = "Storefront"
	sp.AWS.Accounts = []spec.Account{
		{
			ID: "111122223333", Environment: "production", Regions: []string{"us-east-1"}, Production: true,
			RoleARN: "arn:aws:iam::111122223333:role/CloudOptix-acme-Read", ExternalID: "cloudoptix-test0000000000000000",
		},
	}
	sp.AWS.AccessMode = "assume_role"
	sp.Security.AWSAccessMode = "assume_role"
	sp.Optimization.RiskTolerance = "medium"
	sp.Objectives.AvailabilityTarget = 0.995
	sp.Governance.ProductionChangesRequireApproval = true
	return sp
}

// seedApprovedTenant creates a tenant and an approved v1 specification for
// it, returning the stored version.
func seedApprovedTenant(t interface{ Helper() }, repos ports.Repositories, tenant core.TenantID, sp spec.Spec) spec.Version {
	t.Helper()
	ctx := ctxFor(tenant)
	specID := core.NewID("spec")
	v1 := spec.Version{
		ID: core.NewID("specver"), TenantID: tenant, SpecID: specID, Version: 1,
		Status: spec.StatusPendingReview, Spec: sp, Validation: sp.Validate(),
		Completeness: sp.AssessCompleteness(), Checksum: spec.ComputeChecksum(sp),
		CreatedBy: "seed", CreatedAt: testNow,
	}
	if err := repos.Specs.SaveDraft(ctx, v1); err != nil {
		panic(err)
	}
	if err := repos.Specs.Approve(ctx, tenant, v1); err != nil {
		panic(err)
	}
	tn := tenancy.Tenant{
		ID: tenant, Slug: "acme", Name: "Acme Corp", Plan: tenancy.PlanStandard,
		Quotas: tenancy.QuotasFor(tenancy.PlanStandard), State: tenancy.StateActive,
		SpecID: specID, ActiveSpecVersion: 1, CreatedAt: testNow, UpdatedAt: testNow,
	}
	if err := repos.Tenants.Create(ctx, tn); err != nil {
		panic(err)
	}
	approved, err := repos.Specs.GetActive(ctx, tenant)
	if err != nil {
		panic(err)
	}
	return approved
}
