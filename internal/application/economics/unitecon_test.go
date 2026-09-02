package economics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func seedTransaction(t *testing.T, repos ports.Repositories, tenant core.TenantID, workloadIDs []core.ID, vs econ.VolumeSource) econ.BusinessTransaction {
	t.Helper()
	tx := econ.BusinessTransaction{
		ID: core.NewID("tx"), TenantID: tenant, Name: "checkout", WorkloadIDs: workloadIDs, VolumeSource: vs,
	}
	require.NoError(t, repos.Economics.UpsertTransaction(ctxFor(tenant), tx))
	return tx
}

func tagResourceWorkload(t *testing.T, repos ports.Repositories, tenant core.TenantID, resourceID, workloadID core.ID) {
	t.Helper()
	ctx := ctxFor(tenant)
	res, err := repos.Resources.Get(ctx, tenant, resourceID)
	require.NoError(t, err)
	res.WorkloadID = workloadID
	_, err = repos.Resources.UpsertBatch(ctx, tenant, []cloud.Resource{res})
	require.NoError(t, err)
}

// singleResourceTransactionFixture builds the minimal graph a unit-economics
// test needs: one application, one workload, one resource with no topology
// edges at all — so a transaction's footprint total is exactly that
// resource's direct cost, with no indirect/shared/unattributed noise to
// account for when asserting on cost-per-unit.
func singleResourceTransactionFixture(t *testing.T, repos ports.Repositories) (tenant core.TenantID, txID, resourceID core.ID) {
	t.Helper()
	tenant = core.TenantID("tnt_econ_ue")
	ctx := ctxFor(tenant)

	appID := core.NewID("app")
	require.NoError(t, repos.Applications.UpsertApplication(ctx, cloud.Application{ID: appID, TenantID: tenant, Name: "Checkout", Slug: "checkout"}))
	wlID := core.NewID("wl")
	require.NoError(t, repos.Applications.UpsertWorkload(ctx, cloud.Workload{
		ID: wlID, TenantID: tenant, ApplicationID: appID, Name: "checkout-api", Type: cloud.WorkloadAPI, Platform: cloud.PlatformEC2,
	}))

	res := mkResource(tenant, cloud.KindEC2Instance, "checkout-1", appID, wlID)
	_, err := repos.Resources.UpsertBatch(ctx, tenant, []cloud.Resource{res})
	require.NoError(t, err)
	inv, err := repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	resourceID = inv.All()[0].ID

	tx := seedTransaction(t, repos, tenant, []core.ID{wlID}, econ.VolumeSource{Kind: "declared", DeclaredMonthly: 100})
	return tenant, tx.ID, resourceID
}

func TestUnitEconomics_DeclaredVolumeProducesRequiresConfirmationProvenance(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, txID, resourceID := singleResourceTransactionFixture(t, repos)
	svc := NewService(repos)

	period := testPeriod()
	seedCost(t, repos, tenant, resourceID, 200, period)

	ue, err := svc.UnitEconomics(ctxFor(tenant), tenant, txID, period)
	require.NoError(t, err)

	// A declared monthly figure is prorated to the query period's actual
	// length, so a 30-day window (fractionally short of the 30.4375-day
	// average month the figure is declared against) yields a hair under 100.
	expectedVolume := 100.0 * (period.Days() / core.AverageDaysPerMonth)
	assert.Equal(t, core.ProvenanceRequiresConfirmation, ue.VolumeProvenance)
	assert.InDelta(t, expectedVolume, ue.Volume, 0.01)
	assert.Equal(t, core.USDollars(200), ue.TotalCost)
}

func TestUnitEconomics_MeasuredVolumeFromNamedResourceIsConfirmed(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, txID, resourceID := singleResourceTransactionFixture(t, repos)
	svc := NewService(repos)

	period := testPeriod()
	seedCost(t, repos, tenant, resourceID, 200, period)
	require.NoError(t, repos.Metrics.SaveSeries(ctxFor(tenant), tenant, []ports.MetricSeries{{
		ResourceID: resourceID, MetricName: "RequestCount", Unit: "Count", Source: "cloudwatch",
		Points: []ports.MetricPoint{
			{At: period.Start.Add(1), Value: 500},
			{At: period.Start.Add(2), Value: 500},
		},
	}}))

	tx, err := repos.Economics.GetTransaction(ctxFor(tenant), tenant, txID)
	require.NoError(t, err)
	tx.VolumeSource = econ.VolumeSource{
		Kind: "cloudwatch", MetricName: "RequestCount",
		Dimensions: map[string]string{"resource_id": string(resourceID)},
	}
	require.NoError(t, repos.Economics.UpsertTransaction(ctxFor(tenant), tx))

	ue, err := svc.UnitEconomics(ctxFor(tenant), tenant, txID, period)
	require.NoError(t, err)

	assert.Equal(t, core.ProvenanceConfirmed, ue.VolumeProvenance)
	assert.Equal(t, 1000.0, ue.Volume)
}

func TestUnitEconomics_MeasuredSourceWithNoDataFallsBackToDeclaredWithWeakerProvenance(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, txID, resourceID := singleResourceTransactionFixture(t, repos)
	svc := NewService(repos)

	period := testPeriod()
	seedCost(t, repos, tenant, resourceID, 200, period)

	tx, err := repos.Economics.GetTransaction(ctxFor(tenant), tenant, txID)
	require.NoError(t, err)
	// A measured source is named but no series was ever saved for it, and a
	// declared fallback figure is present.
	tx.VolumeSource = econ.VolumeSource{
		Kind: "cloudwatch", MetricName: "RequestCount",
		Dimensions: map[string]string{"resource_id": string(resourceID)}, DeclaredMonthly: 50,
	}
	require.NoError(t, repos.Economics.UpsertTransaction(ctxFor(tenant), tx))

	ue, err := svc.UnitEconomics(ctxFor(tenant), tenant, txID, period)
	require.NoError(t, err)

	expectedVolume := 50.0 * (period.Days() / core.AverageDaysPerMonth)
	assert.Equal(t, core.ProvenanceUnknown, ue.VolumeProvenance)
	assert.InDelta(t, expectedVolume, ue.Volume, 0.01)
}

func TestDecomposeChange_VolumeAndUnitCostArithmeticReconcilesToTotalDelta(t *testing.T) {
	// A pure-domain-function test of the price/volume variance identity
	// econ.DecomposeChange implements: volume held at the prior unit cost,
	// unit-cost effect applied to the current volume, summing back to the
	// exact total-cost delta between the two observations.
	prior := econ.UnitEconomics{Volume: 100, TotalCost: core.USDollars(100), CostPerUnit: core.USDollars(1)}
	current := econ.UnitEconomics{Volume: 300, TotalCost: core.USDollars(150), CostPerUnit: core.MoneyFromMicros(500_000, core.USD)}

	drivers := econ.DecomposeChange(prior, current)
	require.Len(t, drivers, 2)

	var volume, unit econ.Driver
	for _, d := range drivers {
		switch d.Kind {
		case "volume":
			volume = d
		case "unit_cost":
			unit = d
		}
	}
	require.NotEmpty(t, volume.Kind)
	require.NotEmpty(t, unit.Kind)

	// volumeEffect = prior.CostPerUnit * (300-100) = $1 * 200 = $200: volume
	// growth alone would have added $200 at the old unit cost.
	assert.Equal(t, core.USDollars(200).Micros(), volume.Impact.Micros())
	// unitEffect = (0.5-1.0) * 300 = -$150: the unit-cost improvement saved
	// $150 at the new volume.
	assert.Equal(t, core.USDollars(-150).Micros(), unit.Impact.Micros())
	// The two effects reconcile exactly to the observed total-cost delta.
	assert.Equal(t, core.USDollars(50).Micros(), volume.Impact.MustAdd(unit.Impact).Micros())
	// Impact shares are normalized against the combined magnitude of both
	// effects (200+150=350) and must sum to 1.
	assert.InDelta(t, 1.0, volume.ImpactShare+unit.ImpactShare, 1e-9)
	// The larger-magnitude driver (volume, $200) is reported first.
	assert.Equal(t, "volume", drivers[0].Kind)
}

func TestUnitEconomics_ThroughServicePopulatesDriversWhenCostAndVolumeBothMove(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, txID, resourceID := singleResourceTransactionFixture(t, repos)
	svc := NewService(repos)

	period := testPeriod()
	priorPeriod := core.Period{Start: period.Start.Add(-period.Duration()), End: period.Start}
	seedCost(t, repos, tenant, resourceID, 100, priorPeriod)
	seedCost(t, repos, tenant, resourceID, 150, period)

	_, err := svc.UnitEconomics(ctxFor(tenant), tenant, txID, priorPeriod)
	require.NoError(t, err)

	tx, err := repos.Economics.GetTransaction(ctxFor(tenant), tenant, txID)
	require.NoError(t, err)
	tx.VolumeSource.DeclaredMonthly = 300
	require.NoError(t, repos.Economics.UpsertTransaction(ctxFor(tenant), tx))

	ue, err := svc.UnitEconomics(ctxFor(tenant), tenant, txID, period)
	require.NoError(t, err)

	assert.NotEmpty(t, ue.Drivers, "cost and volume both moved between periods, so a movement must be decomposed")
	assert.False(t, ue.PriorCostPerUnit.IsZero())
	assert.NotZero(t, ue.ChangePct)

	history, err := svc.UnitEconomicsHistory(ctxFor(tenant), tenant, txID, priorPeriod.Start, period.End)
	require.NoError(t, err)
	assert.Len(t, history, 2, "both computed observations must be retained for the trend view")
}
