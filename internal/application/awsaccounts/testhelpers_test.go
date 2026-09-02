package awsaccounts

import (
	"context"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-awsaccounts-test")

var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func ctxFor(tenant core.TenantID, roles ...core.Role) context.Context {
	if len(roles) == 0 {
		roles = []core.Role{core.RoleTenantAdmin}
	}
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject: "test-user", TenantID: tenant, Roles: roles, IssuedAt: testNow,
	})
}

// fakeBroker returns a canned ports.ConnectionCheck (or error) from Verify,
// set per test. Assume is never exercised by this package's tests.
type fakeBroker struct {
	check   ports.ConnectionCheck
	checkAt func(cloud.AWSAccount) ports.ConnectionCheck
	err     error
}

func (b *fakeBroker) Assume(context.Context, cloud.AWSAccount, cloud.RoleScope) (ports.AWSSession, error) {
	return nil, core.NewError(core.ErrNotImplemented, "not_implemented", "fakeBroker.Assume is unused by these tests")
}

func (b *fakeBroker) Verify(_ context.Context, account cloud.AWSAccount) (ports.ConnectionCheck, error) {
	if b.err != nil {
		return ports.ConnectionCheck{}, b.err
	}
	if b.checkAt != nil {
		return b.checkAt(account), nil
	}
	return b.check, nil
}

// newTestService wires a Service against a fresh in-memory Store and a
// broker, returning both so a test can seed fixtures and swap the broker's
// canned answer between calls.
func newTestService(t interface{ Helper() }, broker ports.AWSCredentialBroker) (*Service, ports.Repositories) {
	t.Helper()
	store := memstore.New()
	repos := store.Repositories()
	svc, err := NewService(Deps{
		Accounts: repos.AWSAccounts, Tenants: repos.Tenants, Specs: repos.Specs,
		Executions: repos.Executions, Audit: repos.Audit, Broker: broker,
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})
	if err != nil {
		panic(err) // programmer error in the test fixture, not a real assertion failure
	}
	return svc, repos
}

// seedTenant creates a tenant, optionally with an approved specification (so
// CanConnectAWS passes), and returns it.
func seedTenant(t interface{ Helper() }, repos ports.Repositories, tenant core.TenantID, demo, withApprovedSpec bool) tenancy.Tenant {
	t.Helper()
	ctx := ctxFor(tenant)

	var sp spec.Spec
	sp.APIVersion = spec.CurrentAPIVersion
	sp.Organization.Name = "Acme Corp"
	sp.Application.Name = "Storefront"
	sp.AWS.AccessMode = "assume_role"
	sp.Security.AWSAccessMode = "assume_role"
	sp.Optimization.RiskTolerance = "medium"

	tn := tenancy.Tenant{
		ID: tenant, Slug: "acme", Name: "Acme Corp", Plan: tenancy.PlanStandard,
		Quotas: tenancy.QuotasFor(tenancy.PlanStandard), State: tenancy.StateActive,
		Demo: demo, CreatedAt: testNow, UpdatedAt: testNow,
	}

	if withApprovedSpec {
		specID := core.NewID("spec")
		v := spec.Version{
			ID: core.NewID("specver"), TenantID: tenant, SpecID: specID, Version: 1,
			Status: spec.StatusApproved, Spec: sp, CreatedAt: testNow,
		}
		if err := repos.Specs.SaveDraft(ctx, v); err != nil {
			panic(err)
		}
		if err := repos.Specs.Approve(ctx, tenant, v); err != nil {
			panic(err)
		}
		tn.SpecID = specID
		tn.ActiveSpecVersion = 1
	}

	if err := repos.Tenants.Create(ctx, tn); err != nil {
		panic(err)
	}
	return tn
}
