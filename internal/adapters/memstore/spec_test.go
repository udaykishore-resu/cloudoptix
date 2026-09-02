package memstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

func TestSpecRepo_ApproveIsAtomicAndSupersedesPrevious(t *testing.T) {
	s := New()
	repo := s.Repositories().Specs
	ctx := ctxFor(tenantA)

	specID := core.NewID("spc")
	v1 := spec.Version{ID: core.NewID("sv"), TenantID: tenantA, SpecID: specID, Version: 1, Status: spec.StatusPendingReview}
	require.NoError(t, repo.SaveDraft(ctx, v1))
	require.NoError(t, repo.Approve(ctx, tenantA, v1))

	got, err := repo.GetActive(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Version)
	assert.Equal(t, spec.StatusApproved, got.Status)

	v2 := spec.Version{ID: core.NewID("sv"), TenantID: tenantA, SpecID: specID, Version: 2, Status: spec.StatusPendingReview}
	require.NoError(t, repo.SaveDraft(ctx, v2))
	require.NoError(t, repo.Approve(ctx, tenantA, v2))

	// Exactly one active version at a time.
	active, err := repo.GetActive(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, 2, active.Version)

	v1After, err := repo.GetVersion(ctx, tenantA, specID, 1)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusSuperseded, v1After.Status, "the previously active version must be superseded, not left active")

	v2After, err := repo.GetVersion(ctx, tenantA, specID, 2)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusApproved, v2After.Status)

	versions, err := repo.ListVersions(ctx, tenantA, specID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, 1, versions[0].Version, "ListVersions is ascending by version")
	assert.Equal(t, 2, versions[1].Version)
}

func TestSpecRepo_RejectMarksVersionRejected(t *testing.T) {
	s := New()
	repo := s.Repositories().Specs
	ctx := ctxFor(tenantA)

	specID := core.NewID("spc")
	v1 := spec.Version{ID: core.NewID("sv"), TenantID: tenantA, SpecID: specID, Version: 1, Status: spec.StatusPendingReview}
	require.NoError(t, repo.SaveDraft(ctx, v1))
	require.NoError(t, repo.Reject(ctx, tenantA, v1.ID, "missing external id", "reviewer@example.com"))

	got, err := repo.Get(ctx, tenantA, v1.ID)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusRejected, got.Status)
	assert.Equal(t, "missing external id", got.RejectedReason)

	_, err = repo.GetActive(ctx, tenantA)
	assert.Error(t, err, "a rejected version must never be reported active")
}

func TestSpecRepo_SaveDraftAutoIncrementsVersion(t *testing.T) {
	s := New()
	repo := s.Repositories().Specs
	ctx := ctxFor(tenantA)

	specID := core.NewID("spc")
	require.NoError(t, repo.SaveDraft(ctx, spec.Version{ID: core.NewID("sv"), TenantID: tenantA, SpecID: specID, Status: spec.StatusDraft}))
	require.NoError(t, repo.SaveDraft(ctx, spec.Version{ID: core.NewID("sv"), TenantID: tenantA, SpecID: specID, Status: spec.StatusDraft}))

	latest, err := repo.GetLatest(ctx, tenantA, specID)
	require.NoError(t, err)
	assert.Equal(t, 2, latest.Version)
}

func TestSpecRepo_TenantIsolation(t *testing.T) {
	s := New()
	repo := s.Repositories().Specs

	specID := core.NewID("spc")
	v1 := spec.Version{ID: core.NewID("sv"), TenantID: tenantA, SpecID: specID, Version: 1, Status: spec.StatusPendingReview}
	require.NoError(t, repo.SaveDraft(ctxFor(tenantA), v1))

	_, err := repo.GetVersion(ctxFor(tenantB), tenantA, specID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTenantMismatch)
}
