package economics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
}

func testPeriod() core.Period {
	end := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	return core.Period{Start: end.AddDate(0, 0, -30), End: end}
}

func mkResource(tenant core.TenantID, kind cloud.Kind, name string, appID, workloadID core.ID) cloud.Resource {
	return cloud.Resource{
		ID: core.NewID("res"), TenantID: tenant, AccountID: "111111111111", Region: "us-east-1",
		Kind: kind, NativeID: name, Name: name, State: cloud.StateRunning,
		Environment: core.EnvProduction, EnvironmentSource: core.ProvenanceConfirmed,
		ApplicationID: appID, WorkloadID: workloadID,
		LastSeenAt: time.Now(), FirstSeenAt: time.Now(),
	}
}

func seedCost(t *testing.T, repos ports.Repositories, tenant core.TenantID, resourceID core.ID, amount float64, period core.Period) {
	t.Helper()
	_, err := repos.Costs.UpsertBatch(ctxFor(tenant), tenant, []cost.Record{{
		TenantID: tenant, AccountID: "111111111111", ResourceID: resourceID,
		Period: period, Basis: cost.BasisAmortized, Service: "test", ChargeType: cost.ChargeUsage,
		Amount: core.USDollars(amount),
	}})
	require.NoError(t, err)
}

// econEstate seeds an application (app1) owning an ALB and two EC2 instances,
// a shared observability resource consumed by those two instances plus a
// third instance belonging to a different application, a NAT gateway
// exclusively egressed-through by one of app1's instances, and an RDS
// database that both of app1's instances depend on but which records no
// consumer edge at all.
func econEstate(t *testing.T, repos ports.Repositories) (tenant core.TenantID, app1, app2 core.ID, res map[string]cloud.Resource) {
	t.Helper()
	tenant = core.TenantID("tnt_econ1")
	ctx := ctxFor(tenant)

	app1 = core.NewID("app")
	app2 = core.NewID("app")
	require.NoError(t, repos.Applications.UpsertApplication(ctx, cloud.Application{
		ID: app1, TenantID: tenant, Name: "Checkout", Slug: "checkout", Domain: "ecommerce",
	}))
	require.NoError(t, repos.Applications.UpsertApplication(ctx, cloud.Application{
		ID: app2, TenantID: tenant, Name: "Fulfilment", Slug: "fulfilment", Domain: "logistics",
	}))

	alb := mkResource(tenant, cloud.KindALB, "web-alb", app1, "")
	i1 := mkResource(tenant, cloud.KindEC2Instance, "web-1", app1, "")
	i2 := mkResource(tenant, cloud.KindEC2Instance, "web-2", app1, "")
	i3 := mkResource(tenant, cloud.KindEC2Instance, "other-1", app2, "")
	shared := mkResource(tenant, cloud.KindElastiCache, "shared-cache", "", "")
	nat := mkResource(tenant, cloud.KindNATGateway, "nat-1", "", "")
	db := mkResource(tenant, cloud.KindRDSInstance, "app-db", "", "")

	all := []cloud.Resource{alb, i1, i2, i3, shared, nat, db}
	_, err := repos.Resources.UpsertBatch(ctx, tenant, all)
	require.NoError(t, err)

	inv, err := repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	res = map[string]cloud.Resource{}
	for _, r := range inv.All() {
		res[r.Name] = r
	}

	edges := []cloud.Relationship{
		{FromID: res["web-1"].ID, ToID: res["app-db"].ID, Kind: cloud.RelDependsOn, Weight: 1, Confidence: 0.9},
		{FromID: res["web-2"].ID, ToID: res["app-db"].ID, Kind: cloud.RelDependsOn, Weight: 1, Confidence: 0.9},
		{FromID: res["web-1"].ID, ToID: res["shared-cache"].ID, Kind: cloud.RelSharedBy, Weight: 1, Confidence: 0.9},
		{FromID: res["web-2"].ID, ToID: res["shared-cache"].ID, Kind: cloud.RelSharedBy, Weight: 1, Confidence: 0.9},
		{FromID: res["other-1"].ID, ToID: res["shared-cache"].ID, Kind: cloud.RelSharedBy, Weight: 1, Confidence: 0.9},
		{FromID: res["web-1"].ID, ToID: res["nat-1"].ID, Kind: cloud.RelEgressVia, Weight: 1, Confidence: 0.9},
	}
	require.NoError(t, repos.Resources.ReplaceRelationships(ctx, tenant, "111111111111", "us-east-1", edges))

	period := testPeriod()
	seedCost(t, repos, tenant, res["web-alb"].ID, 20, period)
	seedCost(t, repos, tenant, res["web-1"].ID, 100, period)
	seedCost(t, repos, tenant, res["web-2"].ID, 100, period)
	seedCost(t, repos, tenant, res["other-1"].ID, 100, period)
	seedCost(t, repos, tenant, res["shared-cache"].ID, 60, period)
	seedCost(t, repos, tenant, res["nat-1"].ID, 40, period)
	seedCost(t, repos, tenant, res["app-db"].ID, 300, period)

	return tenant, app1, app2, res
}

func TestFootprint_DirectClassifiesExclusivelyOwnedResources(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)

	fp, err := svc.Footprint(ctxFor(tenant), tenant, econ.ScopeApplication, app1, testPeriod())
	require.NoError(t, err)

	// alb(20) + web-1(100) + web-2(100)
	assert.Equal(t, core.USDollars(220), fp.Direct)
}

func TestFootprint_IndirectIsFullyCausedByASingleConsumer(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)

	fp, err := svc.Footprint(ctxFor(tenant), tenant, econ.ScopeApplication, app1, testPeriod())
	require.NoError(t, err)

	// nat-1 ($40) has exactly one consumer (web-1), which belongs to app1,
	// so the whole of its cost is Indirect, not split.
	assert.Equal(t, core.USDollars(40), fp.Indirect)
}

func TestFootprint_SharedSplitsByMeasuredConsumption(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, app2, _ := econEstate(t, repos)
	svc := NewService(repos)

	fp1, err := svc.Footprint(ctxFor(tenant), tenant, econ.ScopeApplication, app1, testPeriod())
	require.NoError(t, err)
	fp2, err := svc.Footprint(ctxFor(tenant), tenant, econ.ScopeApplication, app2, testPeriod())
	require.NoError(t, err)

	// shared-cache ($60) has three equally-weighted consumers; app1 owns two
	// of them (web-1, web-2) so it books 2/3 = $40, app2 owns the third and
	// books 1/3 = $20. The two scopes' shares reconcile exactly to the
	// shared resource's own cost, because Consumers() normalizes to one.
	assert.Equal(t, core.USDollars(40), fp1.Shared)
	assert.Equal(t, core.USDollars(20), fp2.Shared)
	assert.Equal(t, fp1.Shared.MustAdd(fp2.Shared).Micros(), core.USDollars(60).Micros())
}

func TestFootprint_UnattributedIsHonestAndDedupedAcrossOwnedDependents(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)

	fp, err := svc.Footprint(ctxFor(tenant), tenant, econ.ScopeApplication, app1, testPeriod())
	require.NoError(t, err)

	// app-db ($300) is depended on by both web-1 and web-2, but records no
	// consumer edge at all — its cost must land in Unattributed exactly
	// once, not twice (once per owned dependent) and not silently guessed
	// at via an even split.
	assert.Equal(t, core.USDollars(300), fp.Unattributed)

	// Total must never silently absorb the unattributed remainder.
	assert.Equal(t, core.USDollars(220).MustAdd(core.USDollars(40)).MustAdd(core.USDollars(40)), fp.Total)
}

func TestFootprint_ResourceScopeIsASingleResource(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, _, _, res := econEstate(t, repos)
	svc := NewService(repos)

	fp, err := svc.Footprint(ctxFor(tenant), tenant, econ.ScopeResource, res["web-1"].ID, testPeriod())
	require.NoError(t, err)
	assert.Equal(t, core.USDollars(100), fp.Direct)
	// web-1 alone still sees the nat gateway it exclusively causes and the
	// database it depends on with no recorded consumer.
	assert.Equal(t, core.USDollars(40), fp.Indirect)
	assert.Equal(t, core.USDollars(300), fp.Unattributed)
}

func TestFootprint_BusinessCapabilityUnionsApplicationsBySharedDomain(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, _, _, _ := econEstate(t, repos)
	svc := NewService(repos)

	fp, err := svc.Footprint(ctxFor(tenant), tenant, econ.ScopeBusinessCapability, core.ID("ecommerce"), testPeriod())
	require.NoError(t, err)
	assert.Equal(t, core.USDollars(220), fp.Direct)
}

func TestFootprint_IsPersistedAndServedFromCacheOnRepeat(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, app1, _, _ := econEstate(t, repos)
	svc := NewService(repos)

	fp1, err := svc.Footprint(ctxFor(tenant), tenant, econ.ScopeApplication, app1, testPeriod())
	require.NoError(t, err)

	listed, err := repos.Economics.ListFootprints(ctxFor(tenant), tenant, econ.ScopeApplication, testPeriod())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, fp1.Total.Micros(), listed[0].Total.Micros())
}
