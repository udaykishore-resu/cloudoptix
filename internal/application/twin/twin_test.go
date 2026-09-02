package twin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
}

func mkResource(tenant core.TenantID, kind cloud.Kind, name string, monthly float64) cloud.Resource {
	return cloud.Resource{
		ID: core.NewID("res"), TenantID: tenant, AccountID: "111111111111", Region: "us-east-1",
		Kind: kind, NativeID: name, Name: name, State: cloud.StateRunning,
		Environment: core.EnvProduction, MonthlyCost: core.USDollars(monthly),
		LastSeenAt: time.Now(), FirstSeenAt: time.Now(),
	}
}

// smallEstate seeds an ALB -> target group -> two EC2 instances architecture
// plus an RDS instance depended on by the instances, wired with topology
// edges. It returns the tenant and the resource ids by role.
func smallEstate(t *testing.T, repos ports.Repositories) (core.TenantID, map[string]cloud.Resource) {
	t.Helper()
	tenant := core.TenantID("tnt_twin1")
	ctx := ctxFor(tenant)

	alb := mkResource(tenant, cloud.KindALB, "web-alb", 20)
	tg := mkResource(tenant, cloud.KindTargetGroup, "web-tg", 0)
	i1 := mkResource(tenant, cloud.KindEC2Instance, "web-1", 100)
	i2 := mkResource(tenant, cloud.KindEC2Instance, "web-2", 100)
	db := mkResource(tenant, cloud.KindRDSInstance, "app-db", 300)

	resources := []cloud.Resource{alb, tg, i1, i2, db}
	_, err := repos.Resources.UpsertBatch(ctx, tenant, resources)
	require.NoError(t, err)

	// Re-read to pick up assigned IDs (UpsertBatch is idempotent on Key(),
	// but for brand-new resources the ID we set is kept as-is).
	byName := map[string]cloud.Resource{}
	inv, err := repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	for _, r := range inv.All() {
		byName[r.Name] = r
	}

	edges := []cloud.Relationship{
		{FromID: byName["web-alb"].ID, ToID: byName["web-tg"].ID, Kind: cloud.RelRoutesTo, Weight: 1, Confidence: 0.9},
		{FromID: byName["web-tg"].ID, ToID: byName["web-1"].ID, Kind: cloud.RelRoutesTo, Weight: 0.5, Confidence: 0.9},
		{FromID: byName["web-tg"].ID, ToID: byName["web-2"].ID, Kind: cloud.RelRoutesTo, Weight: 0.5, Confidence: 0.9},
		{FromID: byName["web-1"].ID, ToID: byName["app-db"].ID, Kind: cloud.RelDependsOn, Weight: 1, Confidence: 0.8},
		{FromID: byName["web-2"].ID, ToID: byName["app-db"].ID, Kind: cloud.RelDependsOn, Weight: 1, Confidence: 0.8},
	}
	require.NoError(t, repos.Resources.ReplaceRelationships(ctx, tenant, "111111111111", "us-east-1", edges))

	return tenant, byName
}

func TestGraph_ArchitectureViewBuildsNodesAndEdges(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, byName := smallEstate(t, repos)
	svc := NewService(repos, store.Cache())

	graph, err := svc.Graph(ctxFor(tenant), tenant, ports.TwinQuery{View: "architecture"})
	require.NoError(t, err)
	assert.Equal(t, 5, graph.Stats.NodeCount)
	assert.Equal(t, 5, len(graph.Edges))
	assert.Equal(t, "category", graph.Legend["color"])
	assert.False(t, graph.Truncated)

	// Every resource is represented.
	byID := map[core.ID]ports.TwinNode{}
	for _, n := range graph.Nodes {
		byID[n.ID] = n
	}
	require.Contains(t, byID, byName["app-db"].ID)
	assert.Equal(t, core.USDollars(300), byID[byName["app-db"].ID].MonthlyCost)
}

func TestGraph_CostViewComputesCostShare(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, byName := smallEstate(t, repos)
	svc := NewService(repos, store.Cache())

	graph, err := svc.Graph(ctxFor(tenant), tenant, ports.TwinQuery{View: "cost"})
	require.NoError(t, err)
	var dbShare float64
	for _, n := range graph.Nodes {
		if n.ID == byName["app-db"].ID {
			dbShare = n.CostShare
		}
	}
	// total cost = 20 + 0 + 100 + 100 + 300 = 520; db share = 300/520
	assert.InDelta(t, 300.0/520.0, dbShare, 0.001)
}

func TestGraph_RootedSubgraphRespectsDepth(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, byName := smallEstate(t, repos)
	svc := NewService(repos, store.Cache())

	// Depth 1 from the ALB reaches only the target group.
	graph, err := svc.Graph(ctxFor(tenant), tenant, ports.TwinQuery{RootID: byName["web-alb"].ID, MaxDepth: 1})
	require.NoError(t, err)
	ids := nodeIDs(graph.Nodes)
	assert.Contains(t, ids, byName["web-alb"].ID)
	assert.Contains(t, ids, byName["web-tg"].ID)
	assert.NotContains(t, ids, byName["app-db"].ID)

	// Depth 3 reaches the database.
	graph, err = svc.Graph(ctxFor(tenant), tenant, ports.TwinQuery{RootID: byName["web-alb"].ID, MaxDepth: 3})
	require.NoError(t, err)
	assert.Contains(t, nodeIDs(graph.Nodes), byName["app-db"].ID)
}

func TestGraph_SearchFiltersNodes(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, byName := smallEstate(t, repos)
	svc := NewService(repos, store.Cache())

	graph, err := svc.Graph(ctxFor(tenant), tenant, ports.TwinQuery{Search: "db"})
	require.NoError(t, err)
	ids := nodeIDs(graph.Nodes)
	assert.Contains(t, ids, byName["app-db"].ID)
	assert.NotContains(t, ids, byName["web-1"].ID)
}

func TestGraph_CollapsesLowValueLeaves(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_twin_collapse")
	ctx := ctxFor(tenant)

	var resources []cloud.Resource
	for i := 0; i < 30; i++ {
		resources = append(resources, mkResource(tenant, cloud.KindEBSSnapshot, fmt.Sprintf("snap-%d", i), 1))
	}
	_, err := repos.Resources.UpsertBatch(ctx, tenant, resources)
	require.NoError(t, err)

	svc := NewService(repos, store.Cache())
	graph, err := svc.Graph(ctx, tenant, ports.TwinQuery{Collapse: true})
	require.NoError(t, err)

	require.Len(t, graph.Nodes, 1, "30 identical unconnected snapshots should collapse into one group")
	assert.True(t, graph.Nodes[0].Group)
	assert.Equal(t, 30, graph.Nodes[0].GroupCount)
	assert.Equal(t, core.USDollars(30), graph.Nodes[0].MonthlyCost)
	assert.True(t, graph.Truncated)
}

func TestGraph_ProtectsHighCostNodesFromCollapse(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_twin_protect")
	ctx := ctxFor(tenant)

	var resources []cloud.Resource
	expensive := mkResource(tenant, cloud.KindEBSSnapshot, "big-snap", 5000)
	resources = append(resources, expensive)
	for i := 0; i < 10; i++ {
		resources = append(resources, mkResource(tenant, cloud.KindEBSSnapshot, fmt.Sprintf("snap-%d", i), 1))
	}
	_, err := repos.Resources.UpsertBatch(ctx, tenant, resources)
	require.NoError(t, err)

	svc := NewService(repos, store.Cache())
	svc.MaxNodesBeforeCollapse = 0 // force collapsing regardless of size
	graph, err := svc.Graph(ctx, tenant, ports.TwinQuery{})
	require.NoError(t, err)

	var found bool
	for _, n := range graph.Nodes {
		if !n.Group && n.MonthlyCost.Units() == 5000 {
			found = true
		}
	}
	assert.True(t, found, "the single high-cost resource must survive collapsing intact")
}

func TestDependents_WalksRequestPath(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, byName := smallEstate(t, repos)
	svc := NewService(repos, store.Cache())

	deps, err := svc.Dependents(ctxFor(tenant), tenant, byName["app-db"].ID, 5)
	require.NoError(t, err)
	ids := nodeIDs(deps)
	assert.Contains(t, ids, byName["web-1"].ID)
	assert.Contains(t, ids, byName["web-2"].ID)
	assert.Contains(t, ids, byName["web-tg"].ID)
	assert.Contains(t, ids, byName["web-alb"].ID)
}

func TestNode_ReturnsFindingsAndSavings(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, byName := smallEstate(t, repos)
	ctx := ctxFor(tenant)

	rec := optimize.Recommendation{
		ID: core.NewID("rec"), TenantID: tenant,
		Finding:                optimize.Finding{ResourceID: byName["app-db"].ID, RuleID: "rds-oversized", ResourceKind: cloud.KindRDSInstance},
		EstimatedMonthlySaving: core.USDollars(120), Status: optimize.StatusOpen,
	}
	require.NoError(t, repos.Recommendations.SaveBatch(ctx, tenant, []optimize.Recommendation{rec}))

	svc := NewService(repos, store.Cache())
	node, err := svc.Node(ctx, tenant, byName["app-db"].ID)
	require.NoError(t, err)
	assert.Equal(t, 1, node.FindingCount)
	assert.Equal(t, core.USDollars(120), node.PotentialSaving)
}

func TestGraph_IsCached(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, _ := smallEstate(t, repos)
	svc := NewService(repos, store.Cache())
	ctx := ctxFor(tenant)

	g1, err := svc.Graph(ctx, tenant, ports.TwinQuery{View: "architecture"})
	require.NoError(t, err)

	// Add a resource directly without going through Rebuild; the cached
	// graph must still be served until invalidated.
	_, err = repos.Resources.UpsertBatch(ctx, tenant, []cloud.Resource{mkResource(tenant, cloud.KindS3Bucket, "extra", 5)})
	require.NoError(t, err)

	g2, err := svc.Graph(ctx, tenant, ports.TwinQuery{View: "architecture"})
	require.NoError(t, err)
	assert.Equal(t, g1.Stats.NodeCount, g2.Stats.NodeCount, "an uninvalidated cache must be served as-is")

	_, err = svc.Rebuild(ctx, tenant)
	require.NoError(t, err)
	g3, err := svc.Graph(ctx, tenant, ports.TwinQuery{View: "architecture"})
	require.NoError(t, err)
	assert.Equal(t, g1.Stats.NodeCount+1, g3.Stats.NodeCount, "after Rebuild invalidates the cache, the new resource must appear")
}

func nodeIDs(nodes []ports.TwinNode) []core.ID {
	out := make([]core.ID, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}
