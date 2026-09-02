package memstore

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const (
	tenantA = core.TenantID("tenant-aaa")
	tenantB = core.TenantID("tenant-bbb")
)

func mkResource(tenant core.TenantID, kind cloud.Kind, env core.Environment, cost float64, tags core.Tags) cloud.Resource {
	return cloud.Resource{
		ID:          core.NewID("res"),
		TenantID:    tenant,
		AccountID:   core.AccountID("111122223333"),
		Region:      core.Region("us-east-1"),
		Kind:        kind,
		NativeID:    string(core.NewID("i")),
		Name:        "test-resource",
		State:       cloud.StateRunning,
		Environment: env,
		Tags:        tags,
		MonthlyCost: core.USDollars(cost),
		LastSeenAt:  time.Now().UTC(),
		FirstSeenAt: time.Now().UTC(),
	}
}

func TestResourceRepo_TenantIsolation(t *testing.T) {
	s := New()
	repo := s.Repositories().Resources

	res := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 100, nil)
	_, err := repo.UpsertBatch(ctxFor(tenantA), tenantA, []cloud.Resource{res})
	require.NoError(t, err)

	// A caller scoped to tenant B may not read tenant A's data, even when it
	// supplies tenant A's own id as the query scope: GuardTenant compares the
	// caller's scope against the argument, and a request to look inside
	// another tenant is exactly what it exists to refuse.
	_, err = repo.Get(ctxFor(tenantB), tenantA, res.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrTenantMismatch))

	// The correct tenant can still read it.
	got, err := repo.Get(ctxFor(tenantA), tenantA, res.ID)
	require.NoError(t, err)
	assert.Equal(t, res.ID, got.ID)

	// Cross-tenant listing must not leak tenant A's resource into tenant B's
	// result set even via a filter that matches on kind rather than id — the
	// isolation lives in how data is keyed internally, not merely in what the
	// filter happens to exclude.
	page, err := repo.List(ctxFor(tenantB), tenantB, ports.ResourceFilter{}, ports.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)

	// A platform admin may cross tenants for support.
	_, err = repo.Get(platformAdminCtx(), tenantA, res.ID)
	require.NoError(t, err)
}

func TestResourceRepo_DeepCopyOnReadAndWrite(t *testing.T) {
	s := New()
	repo := s.Repositories().Resources

	res := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 100, core.Tags{"Team": "payments"})
	_, err := repo.UpsertBatch(ctxFor(tenantA), tenantA, []cloud.Resource{res})
	require.NoError(t, err)

	got, err := repo.Get(ctxFor(tenantA), tenantA, res.ID)
	require.NoError(t, err)

	// Mutate the caller's copy...
	got.Tags["Team"] = "mutated"
	got.Name = "mutated-name"

	// ...and confirm the stored value is untouched.
	again, err := repo.Get(ctxFor(tenantA), tenantA, res.ID)
	require.NoError(t, err)
	assert.Equal(t, "payments", again.Tags["Team"])
	assert.Equal(t, "test-resource", again.Name)
}

func TestResourceRepo_FilterCorrectness(t *testing.T) {
	s := New()
	repo := s.Repositories().Resources
	ctx := ctxFor(tenantA)

	prod := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 500, core.Tags{"team": "checkout"})
	staging := mkResource(tenantA, cloud.KindEC2Instance, core.EnvStaging, 10, core.Tags{"team": "checkout"})
	rds := mkResource(tenantA, cloud.KindRDSInstance, core.EnvProduction, 800, core.Tags{"team": "payments"})
	rds.Name = "orders-primary-db"
	deleted := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 50, nil)
	deleted.Deleted = true

	_, err := repo.UpsertBatch(ctx, tenantA, []cloud.Resource{prod, staging, rds, deleted})
	require.NoError(t, err)

	cases := []struct {
		name string
		f    ports.ResourceFilter
		want []core.ID
	}{
		{"by kind", ports.ResourceFilter{Kinds: []cloud.Kind{cloud.KindRDSInstance}}, []core.ID{rds.ID}},
		{"by environment", ports.ResourceFilter{Environments: []core.Environment{core.EnvStaging}}, []core.ID{staging.ID}},
		{"by category", ports.ResourceFilter{Categories: []cloud.Category{cloud.CategoryDatabase}}, []core.ID{rds.ID}},
		{"by tag key+value", ports.ResourceFilter{TagKey: "team", TagValue: "payments"}, []core.ID{rds.ID}},
		{"by tag key only", ports.ResourceFilter{TagKey: "team"}, []core.ID{prod.ID, staging.ID, rds.ID}},
		{"by search", ports.ResourceFilter{Search: "orders-primary"}, []core.ID{rds.ID}},
		{"by min monthly cost", ports.ResourceFilter{MinMonthlyCost: core.USDollars(400)}, []core.ID{prod.ID, rds.ID}},
		{"excludes deleted by default", ports.ResourceFilter{}, []core.ID{prod.ID, staging.ID, rds.ID}},
		{"includes deleted when asked", ports.ResourceFilter{IncludeDeleted: true}, []core.ID{prod.ID, staging.ID, rds.ID, deleted.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := repo.List(ctx, tenantA, tc.f, ports.ListOptions{Limit: 100})
			require.NoError(t, err)
			got := make([]core.ID, 0, len(page.Items))
			for _, r := range page.Items {
				got = append(got, r.ID)
			}
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestResourceRepo_PaginationStability(t *testing.T) {
	s := New()
	repo := s.Repositories().Resources
	ctx := ctxFor(tenantA)

	const n = 23
	ids := make(map[core.ID]bool, n)
	for i := 0; i < n; i++ {
		res := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, float64(i), nil)
		res.LastSeenAt = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		_, err := repo.UpsertBatch(ctx, tenantA, []cloud.Resource{res})
		require.NoError(t, err)
		ids[res.ID] = true
	}

	seen := map[core.ID]bool{}
	cursor := ""
	pages := 0
	for {
		page, err := repo.List(ctx, tenantA, ports.ResourceFilter{}, ports.ListOptions{Limit: 7, Cursor: cursor})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Items), 7)
		for _, r := range page.Items {
			require.False(t, seen[r.ID], "resource %s returned twice across pages", r.ID)
			seen[r.ID] = true
		}
		pages++
		require.Less(t, pages, 20, "pagination did not terminate")
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	assert.Len(t, seen, n)
	for id := range ids {
		assert.True(t, seen[id], "resource %s missing from paginated result", id)
	}
}

func TestResourceRepo_PaginationStableUnderConcurrentInsert(t *testing.T) {
	s := New()
	repo := s.Repositories().Resources
	ctx := ctxFor(tenantA)

	for i := 0; i < 10; i++ {
		res := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, float64(i), nil)
		res.LastSeenAt = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		_, err := repo.UpsertBatch(ctx, tenantA, []cloud.Resource{res})
		require.NoError(t, err)
	}

	first, err := repo.List(ctx, tenantA, ports.ResourceFilter{}, ports.ListOptions{Limit: 5})
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	// Insert more resources between page requests — a keyset cursor must
	// still return the second page cleanly, with no duplicate and no missing
	// item among the original ten.
	for i := 10; i < 15; i++ {
		res := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, float64(i), nil)
		res.LastSeenAt = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		_, err := repo.UpsertBatch(ctx, tenantA, []cloud.Resource{res})
		require.NoError(t, err)
	}

	second, err := repo.List(ctx, tenantA, ports.ResourceFilter{}, ports.ListOptions{Limit: 100, Cursor: first.NextCursor})
	require.NoError(t, err)
	firstIDs := map[core.ID]bool{}
	for _, r := range first.Items {
		firstIDs[r.ID] = true
	}
	for _, r := range second.Items {
		assert.False(t, firstIDs[r.ID], "resource %s duplicated across pages", r.ID)
	}
}

func TestResourceRepo_MarkAbsentIsPartialScanSafe(t *testing.T) {
	s := New()
	repo := s.Repositories().Resources
	ctx := ctxFor(tenantA)

	acct := core.AccountID("111122223333")
	region := core.Region("us-east-1")
	other := core.Region("us-west-2")

	kept := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 10, nil)
	kept.AccountID, kept.Region = acct, region
	gone := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 10, nil)
	gone.AccountID, gone.Region = acct, region
	untouchedRegion := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 10, nil)
	untouchedRegion.AccountID, untouchedRegion.Region = acct, other

	_, err := repo.UpsertBatch(ctx, tenantA, []cloud.Resource{kept, gone, untouchedRegion})
	require.NoError(t, err)

	marked, err := repo.MarkAbsent(ctx, tenantA, acct, region, []cloud.Kind{cloud.KindEC2Instance}, []string{kept.Key()}, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, 1, marked)

	page, err := repo.List(ctx, tenantA, ports.ResourceFilter{IncludeDeleted: true}, ports.ListOptions{Limit: 100})
	require.NoError(t, err)
	states := map[core.ID]bool{}
	for _, r := range page.Items {
		states[r.ID] = r.Deleted
	}
	assert.False(t, states[kept.ID])
	assert.True(t, states[gone.ID])
	assert.False(t, states[untouchedRegion.ID], "a scan of one region must not mark resources in another region absent")
}
