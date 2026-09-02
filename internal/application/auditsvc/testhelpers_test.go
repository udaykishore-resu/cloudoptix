package auditsvc

import (
	"context"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-auditsvc-test")

var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject: "test-user", TenantID: tenant, Roles: []core.Role{core.RoleTenantAdmin}, IssuedAt: testNow,
	})
}

func newTestService(t interface{ Helper() }) (*Service, ports.Repositories) {
	t.Helper()
	repos := memstore.New().Repositories()
	svc, err := NewService(Deps{Audit: repos.Audit, Clock: core.FixedClock{T: testNow}, Logger: discardLogger()})
	if err != nil {
		panic(err) // programmer error in the test fixture, not a real assertion failure
	}
	return svc, repos
}
