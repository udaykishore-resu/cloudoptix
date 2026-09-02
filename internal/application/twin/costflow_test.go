package twin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func fixedNowForFlow() time.Time { return time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) }

func totalOfLevels(g ports.CostFlowGraph) core.Money {
	total := core.ZeroUSD()
	for _, level := range g.Levels {
		for _, n := range level.Nodes {
			total = total.MustAdd(n.Amount)
		}
	}
	return total
}

func TestCostFlow_ConservesTotal(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant, _ := smallEstate(t, repos)
	svc := NewService(repos, store.Cache())

	flow, err := svc.CostFlow(ctxFor(tenant), tenant, ports.TwinQuery{})
	require.NoError(t, err)

	sumNodes := totalOfLevels(flow)
	// Every node's displayed amount must sum to exactly the graph's own
	// total (conservation by construction), and that total plus the honest
	// unattributed remainder is the complete accounting of the period.
	assert.Equal(t, flow.Total.Micros(), sumNodes.Micros(), "every node's displayed amount must sum to the graph total")
	assert.False(t, flow.Unattributed.IsNegative())
}

func TestCostFlow_AccumulatesAlongContainsEdges(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_flow2")
	ctx := ctxFor(tenant)

	vpc := mkResource(tenant, cloud.KindVPC, "vpc-1", 0)
	subnetA := mkResource(tenant, cloud.KindSubnet, "subnet-a", 5)
	subnetB := mkResource(tenant, cloud.KindSubnet, "subnet-b", 5)
	instance := mkResource(tenant, cloud.KindEC2Instance, "i-1", 200)

	_, err := repos.Resources.UpsertBatch(ctx, tenant, []cloud.Resource{vpc, subnetA, subnetB, instance})
	require.NoError(t, err)
	inv, err := repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	byName := map[string]cloud.Resource{}
	for _, r := range inv.All() {
		byName[r.Name] = r
	}

	edges := []cloud.Relationship{
		{FromID: byName["vpc-1"].ID, ToID: byName["subnet-a"].ID, Kind: cloud.RelContains, Weight: 1, Confidence: 1},
		{FromID: byName["vpc-1"].ID, ToID: byName["subnet-b"].ID, Kind: cloud.RelContains, Weight: 1, Confidence: 1},
		{FromID: byName["subnet-a"].ID, ToID: byName["i-1"].ID, Kind: cloud.RelContains, Weight: 1, Confidence: 1},
	}
	require.NoError(t, repos.Resources.ReplaceRelationships(ctx, tenant, "111111111111", "us-east-1", edges))

	svc := NewService(repos, store.Cache())
	flow, err := svc.CostFlow(ctxFor(tenant), tenant, ports.TwinQuery{})
	require.NoError(t, err)

	sumNodes := totalOfLevels(flow)
	assert.Equal(t, flow.Total.Micros(), sumNodes.Micros())

	// vpc-1's own cost (0) splits evenly across its two contains-children
	// (default weight 1 each, normalized to 1/2 apiece), so subnet-a
	// receives 0 from the VPC and retains its own $5 plus whatever it does
	// not pass on to the instance; the instance is a sink and must display
	// its full accumulated amount (its own $200, since subnet-a's own money
	// does not flow onward — only what accumulates AT a node flows via ITS
	// OWN outbound edges, and subnet-a's outbound edge carries default
	// weight 1, i.e. subnet-a's entire accumulated total flows to i-1).
	var subnetAAmount, instanceAmount core.Money
	for _, level := range flow.Levels {
		for _, n := range level.Nodes {
			if n.ID == byName["subnet-a"].ID {
				subnetAAmount = n.Amount
			}
			if n.ID == byName["i-1"].ID {
				instanceAmount = n.Amount
			}
		}
	}
	assert.True(t, subnetAAmount.IsZero(), "subnet-a passes its entire accumulated cost on to the instance it contains")
	assert.Equal(t, core.USDollars(205), instanceAmount, "the instance's displayed amount is its own $200 plus subnet-a's forwarded $5")
}

func TestCostFlow_UnattributedRemainderIsHonest(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_flow3")
	ctx := ctxFor(tenant)

	res := mkResource(tenant, cloud.KindEC2Instance, "i-1", 50)
	_, err := repos.Resources.UpsertBatch(ctx, tenant, []cloud.Resource{res})
	require.NoError(t, err)

	// Billed cost for the period exceeds what any discovered resource
	// accounts for — e.g. a support charge or a resource CloudOptix has not
	// discovered yet.
	_, err = repos.Costs.UpsertBatch(ctx, tenant, []cost.Record{{
		TenantID: tenant, AccountID: "111111111111", Period: core.PeriodOfDays(fixedNowForFlow(), 30),
		Basis: cost.BasisAmortized, Service: "Support", ChargeType: cost.ChargeUsage, Amount: core.USDollars(75),
	}})
	require.NoError(t, err)

	svc := NewService(repos, store.Cache())
	svc.Clock = core.FixedClock{T: fixedNowForFlow()}
	flow, err := svc.CostFlow(ctxFor(tenant), tenant, ports.TwinQuery{})
	require.NoError(t, err)

	assert.Equal(t, core.USDollars(50), flow.Total)
	assert.Equal(t, core.USDollars(25), flow.Unattributed, "billed $75 minus the $50 the graph can place must show as an honest gap")
}
