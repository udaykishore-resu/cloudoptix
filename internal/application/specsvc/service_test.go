package specsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

func TestProposeRevision_ValidPatchProducesPendingReviewDraft(t *testing.T) {
	svc, _, repos := newTestService(t)
	active := seedApprovedTenant(t, repos, testTenant, baseSpec())

	rev, err := svc.ProposeRevision(ctxFor(testTenant), testTenant,
		map[string]any{"objectives.availabilityTarget": 0.999}, "reviewer@example.com")
	require.NoError(t, err)

	assert.Equal(t, spec.StatusPendingReview, rev.Status)
	assert.Equal(t, active.ID, rev.ParentID)
	assert.Equal(t, active.Version+1, rev.Version)
	assert.Equal(t, 0.999, rev.Spec.Objectives.AvailabilityTarget)
	assert.NotEmpty(t, rev.Diff, "a real change must produce a non-empty diff")
	assert.False(t, rev.Validation.HasBlocking())
}

func TestProposeRevision_PatchProducingInvalidSpecIsRejected(t *testing.T) {
	svc, _, repos := newTestService(t)
	seedApprovedTenant(t, repos, testTenant, baseSpec())

	_, err := svc.ProposeRevision(ctxFor(testTenant), testTenant,
		map[string]any{"aws.accounts[0].id": "not-an-account-id"}, "reviewer@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

func TestProposeRevision_RejectsPatchToPermissionGatedPath(t *testing.T) {
	svc, _, repos := newTestService(t)
	seedApprovedTenant(t, repos, testTenant, baseSpec())

	// RoleArchitect can write the specification in general (PermSpecWrite)
	// but does not hold PermAutomationWrite.
	ctx := ctxFor(testTenant, core.RoleArchitect)
	_, err := svc.ProposeRevision(ctx, testTenant, map[string]any{"automation.enabled": true}, "architect@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
}

func TestApprove_AtomicallyFreezesAndSupersedes(t *testing.T) {
	svc, _, repos := newTestService(t)
	v1 := seedApprovedTenant(t, repos, testTenant, baseSpec())

	rev, err := svc.ProposeRevision(ctxFor(testTenant), testTenant,
		map[string]any{"objectives.availabilityTarget": 0.999}, "reviewer@example.com")
	require.NoError(t, err)

	approved, err := svc.Approve(ctxFor(testTenant), testTenant, rev.ID, "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, spec.StatusApproved, approved.Status)
	assert.Equal(t, "admin@example.com", approved.ApprovedBy)

	active, err := repos.Specs.GetActive(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, rev.ID, active.ID, "exactly the newly approved version must be active")

	superseded, err := repos.Specs.GetVersion(ctxFor(testTenant), testTenant, v1.SpecID, v1.Version)
	require.NoError(t, err)
	assert.Equal(t, spec.StatusSuperseded, superseded.Status)

	tn, err := repos.Tenants.Get(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, rev.Version, tn.ActiveSpecVersion, "the tenant record must advance to the newly approved version")
}

func TestApprove_AlreadyApprovedVersionIsRejected(t *testing.T) {
	svc, _, repos := newTestService(t)
	seedApprovedTenant(t, repos, testTenant, baseSpec())

	active, err := repos.Specs.GetActive(ctxFor(testTenant), testTenant)
	require.NoError(t, err)

	_, err = svc.Approve(ctxFor(testTenant), testTenant, active.ID, "admin@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrConflict)
}

func TestExportImportYAML_RoundTrips(t *testing.T) {
	svc, _, repos := newTestService(t)
	active := seedApprovedTenant(t, repos, testTenant, baseSpec())

	out, err := svc.ExportYAML(ctxFor(testTenant), testTenant, active.ID)
	require.NoError(t, err)

	var roundTripped spec.Spec
	require.NoError(t, yaml.Unmarshal(out, &roundTripped))
	assert.Equal(t, active.Spec.Organization.Name, roundTripped.Organization.Name)
	assert.Equal(t, active.Spec.AWS.Accounts[0].ID, roundTripped.AWS.Accounts[0].ID)

	imported, err := svc.ImportYAML(ctxFor(testTenant), testTenant, out, "importer@example.com")
	require.NoError(t, err)
	assert.Equal(t, spec.StatusPendingReview, imported.Status)
	assert.Equal(t, active.SpecID, imported.SpecID)
	assert.Equal(t, active.Version+1, imported.Version)
	assert.Equal(t, active.Spec.Organization.Name, imported.Spec.Organization.Name)
}

func TestImportYAML_RejectsUnsupportedAPIVersion(t *testing.T) {
	svc, _, repos := newTestService(t)
	seedApprovedTenant(t, repos, testTenant, baseSpec())

	doc := []byte("apiVersion: cloudoptix.io/v99\nkind: CloudOptixSpec\n")
	_, err := svc.ImportYAML(ctxFor(testTenant), testTenant, doc, "importer@example.com")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

func TestDiff_ReturnsSeverityOrderedChanges(t *testing.T) {
	svc, _, repos := newTestService(t)
	v1 := seedApprovedTenant(t, repos, testTenant, baseSpec())

	rev, err := svc.ProposeRevision(ctxFor(testTenant), testTenant,
		map[string]any{"objectives.availabilityTarget": 0.999, "notifications.channels[0].name": "ops"},
		"reviewer@example.com")
	require.NoError(t, err)

	changes, err := svc.Diff(ctxFor(testTenant), testTenant, v1.Version, rev.Version)
	require.NoError(t, err)
	require.NotEmpty(t, changes)
	for i := 1; i < len(changes); i++ {
		assert.GreaterOrEqual(t, changes[i-1].Severity.Order(), changes[i].Severity.Order(),
			"changes must be sorted most-severe first")
	}
}
