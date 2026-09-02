package memstore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func mkTenant(id core.TenantID, slug string) tenancy.Tenant {
	return tenancy.Tenant{ID: id, Slug: slug, Name: "Test Co", Plan: tenancy.PlanStandard, State: tenancy.StateActive}
}

func TestUnitOfWork_CommitsOnSuccess(t *testing.T) {
	s := New()
	ctx := ctxFor(tenantA)

	res := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 10, nil)

	err := s.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := repos.Resources.UpsertBatch(ctx, tenantA, []cloud.Resource{res}); err != nil {
			return err
		}
		return repos.Tenants.Create(ctx, mkTenant(tenantA, "acme"))
	})
	require.NoError(t, err)

	got, err := s.Repositories().Resources.Get(ctx, tenantA, res.ID)
	require.NoError(t, err)
	assert.Equal(t, res.ID, got.ID)

	tn, err := s.Repositories().Tenants.Get(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, "acme", tn.Slug)
}

func TestUnitOfWork_RollsBackWholeStoreOnError(t *testing.T) {
	s := New()
	ctx := ctxFor(tenantA)

	// State that exists BEFORE the transaction must survive it untouched.
	pre := mkResource(tenantA, cloud.KindEC2Instance, core.EnvProduction, 5, nil)
	_, err := s.Repositories().Resources.UpsertBatch(ctx, tenantA, []cloud.Resource{pre})
	require.NoError(t, err)

	boom := errors.New("boom: second write failed")
	inTxRes := mkResource(tenantA, cloud.KindEBSVolume, core.EnvProduction, 1, nil)

	err = s.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		// First write succeeds...
		if _, err := repos.Resources.UpsertBatch(ctx, tenantA, []cloud.Resource{inTxRes}); err != nil {
			return err
		}
		if err := repos.Tenants.Create(ctx, mkTenant(tenantA, "acme")); err != nil {
			return err
		}
		// ...but the transaction as a whole fails.
		return boom
	})
	require.ErrorIs(t, err, boom)

	// The resource written inside the failed transaction must be gone.
	_, err = s.Repositories().Resources.Get(ctx, tenantA, inTxRes.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNotFound)

	// The tenant created inside the failed transaction must be gone too —
	// rollback covers every aggregate the callback touched, not just the
	// first one.
	_, err = s.Repositories().Tenants.Get(ctx, tenantA)
	require.Error(t, err)

	// But state from before the transaction must still be exactly there.
	got, err := s.Repositories().Resources.Get(ctx, tenantA, pre.ID)
	require.NoError(t, err)
	assert.Equal(t, pre.ID, got.ID)
}

func TestUnitOfWork_NestedFailureLeavesNoPartialWrite(t *testing.T) {
	s := New()
	ctx := ctxFor(tenantA)

	specID := core.NewID("spc")
	err := s.Do(ctx, func(ctx context.Context, repos ports.Repositories) error {
		v := struct{}{}
		_ = v
		if err := repos.Tenants.Create(ctx, mkTenant(tenantA, "acme")); err != nil {
			return err
		}
		return core.Invalid("simulated mid-transaction failure for spec %s", specID)
	})
	require.Error(t, err)

	_, getErr := s.Repositories().Tenants.Get(ctx, tenantA)
	require.Error(t, getErr, "a tenant created mid-transaction must not survive the rollback")
}

func TestStore_SnapshotRestore(t *testing.T) {
	s := New()
	ctx := ctxFor(tenantA)

	require.NoError(t, s.Repositories().Tenants.Create(ctx, mkTenant(tenantA, "before")))
	snap := s.Snapshot()

	require.NoError(t, s.Repositories().Tenants.Update(ctx, mkTenant(tenantA, "after")))
	tn, err := s.Repositories().Tenants.Get(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, "after", tn.Slug)

	s.Restore(snap)
	tn, err = s.Repositories().Tenants.Get(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, "before", tn.Slug, "Restore must revert to exactly the snapshotted state")
}

func TestStore_Reset(t *testing.T) {
	s := New()
	ctx := ctxFor(tenantA)
	require.NoError(t, s.Repositories().Tenants.Create(ctx, mkTenant(tenantA, "acme")))

	s.Reset()

	_, err := s.Repositories().Tenants.Get(ctx, tenantA)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNotFound)
}

func TestTenantRepo_TenantIsolation(t *testing.T) {
	s := New()
	repo := s.Repositories().Tenants
	require.NoError(t, repo.Create(ctxFor(tenantA), mkTenant(tenantA, "acme")))

	_, err := repo.Get(ctxFor(tenantB), tenantA)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTenantMismatch)

	// A platform admin may still list every tenant.
	page, err := repo.List(platformAdminCtx(), ports.ListOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, page.Items)

	// An ordinary tenant-scoped principal may not.
	_, err = repo.List(ctxFor(tenantB), ports.ListOptions{})
	require.Error(t, err)
}

func TestTenantRepo_SlugUniqueness(t *testing.T) {
	s := New()
	repo := s.Repositories().Tenants
	require.NoError(t, repo.Create(ctxFor(tenantA), mkTenant(tenantA, "acme")))
	err := repo.Create(ctxFor(tenantB), mkTenant(tenantB, "acme"))
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrAlreadyExists)
}
