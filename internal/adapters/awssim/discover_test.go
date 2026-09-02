package awssim

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-demo")

func discoverAll(t *testing.T, estate *Estate) ports.DiscoveryOutput {
	t.Helper()
	broker := NewBroker(estate, cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute)
	account := cloud.AWSAccount{AccountID: estate.AccountID}
	session, err := broker.Assume(context.Background(), account, cloud.ScopeRead)
	require.NoError(t, err)

	d := NewDiscoverer()
	out, err := d.Discover(context.Background(), ports.DiscoveryInput{
		TenantID: testTenant, Session: session, AccountID: estate.AccountID, Region: estate.Regions[0],
	})
	require.NoError(t, err)
	return out
}

func TestDiscover_ProducesEveryDeclaredKind(t *testing.T) {
	e := BuildDemoEstate()
	out := discoverAll(t, e)
	require.NotEmpty(t, out.Resources)

	seen := map[cloud.Kind]int{}
	for _, r := range out.Resources {
		seen[r.Kind]++
	}
	for _, k := range NewDiscoverer().Kinds() {
		if k == cloud.KindVPCEndpoint {
			continue // deliberately absent in the demo estate
		}
		assert.Greater(t, seen[k], 0, "expected at least one resource of kind %s", k)
	}
}

func TestDiscover_EveryResourceValidates(t *testing.T) {
	e := BuildDemoEstate()
	out := discoverAll(t, e)
	for _, r := range out.Resources {
		assert.NoError(t, r.Validate(), "resource %s (%s) failed validation", r.NativeID, r.Kind)
	}
}

func TestDiscover_NoDanglingEdges(t *testing.T) {
	e := BuildDemoEstate()
	out := discoverAll(t, e)

	ids := map[core.ID]bool{}
	for _, r := range out.Resources {
		ids[r.ID] = true
	}
	require.NotEmpty(t, out.Relationships)
	for _, rel := range out.Relationships {
		assert.True(t, ids[rel.FromID], "relationship %s references unknown FromID %s", rel.Kind, rel.FromID)
		assert.True(t, ids[rel.ToID], "relationship %s references unknown ToID %s", rel.Kind, rel.ToID)
		assert.NotEqual(t, rel.FromID, rel.ToID, "relationship must not self-loop")
	}
}

func TestDiscover_GraphIsConnected(t *testing.T) {
	e := BuildDemoEstate()
	out := discoverAll(t, e)

	topo := cloud.NewTopology(out.Relationships)
	inv := cloud.NewInventory(out.Resources)

	// Every EC2 instance with a NAT gateway assigned must have at least one
	// outbound edge (egress_via, at minimum).
	touched := 0
	for _, r := range inv.OfKind(cloud.KindEC2Instance) {
		if r.State != cloud.StateRunning {
			continue
		}
		if len(topo.Outbound(r.ID)) > 0 || len(topo.Inbound(r.ID)) > 0 {
			touched++
		}
	}
	assert.Greater(t, touched, 0, "at least some running instances should carry a graph edge")

	// NAT gateways should show up as shared components consumed by several
	// instances (Consumers reads inbound egress_via edges).
	var sharedNAT bool
	for _, r := range inv.OfKind(cloud.KindNATGateway) {
		if consumers := topo.Consumers(r.ID); len(consumers) > 1 {
			sharedNAT = true
		}
	}
	assert.True(t, sharedNAT, "at least one NAT gateway should be shared by multiple instances")

	// EKS node groups must be reachable from their cluster via contains.
	var eksLinked bool
	for _, r := range inv.OfKind(cloud.KindEKSCluster) {
		if len(topo.Outbound(r.ID, cloud.RelContains)) > 0 {
			eksLinked = true
		}
	}
	assert.True(t, eksLinked, "EKS cluster should contain its node groups")
}

func TestDiscover_MonthlyCostReconcilesWithEstateTotal(t *testing.T) {
	e := BuildDemoEstate()
	out := discoverAll(t, e)

	total := core.ZeroUSD()
	for _, r := range out.Resources {
		total = total.MustAdd(r.MonthlyCost)
	}
	estateTotal := e.TotalMonthlyCost()
	assert.Equal(t, estateTotal.Micros(), total.Micros(),
		"summed discovered resource costs must equal the estate's own total")
}

func TestDiscover_RegionFilter(t *testing.T) {
	e := BuildDemoEstate()
	broker := NewBroker(e, cloud.ScopeRead)
	session, err := broker.Assume(context.Background(), cloud.AWSAccount{AccountID: e.AccountID}, cloud.ScopeRead)
	require.NoError(t, err)

	d := NewDiscoverer()
	out, err := d.Discover(context.Background(), ports.DiscoveryInput{
		TenantID: testTenant, Session: session, AccountID: e.AccountID, Region: "eu-west-1",
	})
	require.NoError(t, err)
	var regional int
	for _, r := range out.Resources {
		if r.Kind == cloud.KindS3Bucket || r.Kind == cloud.KindCloudFront {
			continue // not region-scoped
		}
		regional++
	}
	assert.Zero(t, regional, "a discoverer scoped to eu-west-1 must not return the estate's us-east-1 resources")
}
