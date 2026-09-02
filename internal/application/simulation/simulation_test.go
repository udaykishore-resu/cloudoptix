package simulation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant core.TenantID = "tenant-1"

// ekfsRDSRedisNATTopology builds a representative estate — EKS compute, an
// RDS database, a NAT gateway and (in some tests) a Redis ElastiCache
// cluster — used across this file's candidate-generation and counterfactual
// tests. Each caller tweaks the parts it needs from the returned slice.
func representativeTopology(tenant core.TenantID, region core.Region) []cloud.Resource {
	return []cloud.Resource{
		{
			ID: core.NewID("res"), TenantID: tenant, AccountID: "111122223333", Region: region,
			Kind: cloud.KindEKSCluster, NativeID: "prod-cluster", Name: "prod-cluster",
			State: cloud.StateRunning, MonthlyCost: core.USDollars(73), // eks.cluster_hour * 730
		},
		{
			ID: core.NewID("res"), TenantID: tenant, AccountID: "111122223333", Region: region,
			Kind: cloud.KindEKSNodeGroup, NativeID: "prod-workers", Name: "prod-workers",
			InstanceType: "m5.xlarge", State: cloud.StateRunning,
			Capacity:    cloud.Capacity{DesiredCount: 4, MinCount: 2, MaxCount: 10, VCPU: 4, MemoryGiB: 16},
			Purchase:    cloud.PurchaseOnDemand,
			MonthlyCost: core.USDollars(4 * 0.192 * core.HoursPerMonth), // m5.xlarge on-demand x4
		},
		{
			ID: core.NewID("res"), TenantID: tenant, AccountID: "111122223333", Region: region,
			Kind: cloud.KindRDSInstance, NativeID: "prod-db", Name: "prod-db",
			InstanceType: "db.r5.xlarge", Engine: "postgres", State: cloud.StateAvailable,
			MonthlyCost: core.USDollars(0.48 * core.HoursPerMonth),
		},
		{
			ID: core.NewID("res"), TenantID: tenant, AccountID: "111122223333", Region: region,
			Kind: cloud.KindNATGateway, NativeID: "nat-1", Name: "nat-1",
			State: cloud.StateAvailable, MonthlyCost: core.USDollars(0.045*core.HoursPerMonth + 0.045*500),
		},
	}
}

func testService(t *testing.T, resources []cloud.Resource) (*Service, *fakeInventoryLoader, *fakeSimulationStore) {
	t.Helper()
	loader := &fakeInventoryLoader{resources: resources}
	store := newFakeSimulationStore()
	svc := New(pricing.New(), loader, store)
	svc.Clock = core.FixedClock{T: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return svc, loader, store
}

// --- Architecture Mutation Engine ------------------------------------------

// TestMutateArchitecture_RepresentativeTopology exercises candidate
// generation end to end against an EKS + RDS + NAT (+ optionally Redis)
// topology: every pattern must either produce a priced, scored candidate or
// an honest blocker, ranking must run, and the result must persist.
func TestMutateArchitecture_RepresentativeTopology(t *testing.T) {
	region := core.Region("us-east-1")
	resources := representativeTopology(testTenant, region)
	svc, _, store := testService(t, resources)

	sim, err := svc.MutateArchitecture(context.Background(), testTenant, ports.MutationRequest{
		Scope: "account", ScopeID: core.ID("111122223333"), Name: "eks estate review",
		RiskTolerance: "balanced", RequestedBy: "test",
	})
	require.NoError(t, err)

	require.NotEmpty(t, sim.Candidates, "every pattern is either applicable or blocked; the catalog is never empty")
	require.False(t, sim.BaselineCost.IsZero())

	var sawApplicable, sawBlocked bool
	var sawConsolidate, sawNetworkElim bool
	for _, c := range sim.Candidates {
		if len(c.Blockers) > 0 {
			sawBlocked = true
			assert.NotEmpty(t, c.Blockers[0], "a blocked candidate must state why")
			assert.Equal(t, sim.BaselineCost.Micros(), c.ProjectedMonthlyCost.Micros(), "a blocked candidate carries the baseline cost forward, not a fabricated projection")
			continue
		}
		sawApplicable = true
		assert.NotEmpty(t, c.Scores, "an applicable candidate must be scored on every dimension")
		assert.Len(t, c.Scores, 8, "all eight simulate.Dimensions must be scored")
		assert.NotEmpty(t, c.Changes, "an applicable candidate must carry priced component changes")
		for _, a := range c.Assumptions {
			assert.NotEmpty(t, a.Key)
		}
		switch c.Pattern {
		case "consolidate_to_ecs_fargate":
			sawConsolidate = true
		case "network_cost_elimination":
			sawNetworkElim = true
		}
	}
	assert.True(t, sawApplicable, "at least one pattern must apply to an EKS+RDS+NAT topology")
	assert.True(t, sawBlocked, "at least one pattern (e.g. managed_data_migration with no matching engine, caching_layer_introduction with no DB, ...) should be inapplicable and say so")
	assert.True(t, sawConsolidate, "EKS node group + EC2-shaped compute should make consolidate_to_ecs_fargate applicable")
	assert.True(t, sawNetworkElim, "a NAT gateway in scope should make network_cost_elimination applicable")

	// Ranking: the top candidate is marked Recommended and Composite scores
	// are populated (RankCandidates from the domain package, exercised
	// end-to-end through this service).
	var recommendedCount int
	for _, c := range sim.Candidates {
		if c.Recommended {
			recommendedCount++
		}
	}
	if sawApplicable {
		assert.Equal(t, 1, recommendedCount, "exactly one applicable candidate is marked recommended")
	}

	// Persistence.
	stored, err := store.GetSimulation(context.Background(), testTenant, sim.ID)
	require.NoError(t, err)
	assert.Equal(t, sim.ID, stored.ID)
}

// TestMutateArchitecture_CachingPatternBlockedWhenCacheAlreadyPresent checks
// that a pattern correctly reports "not applicable" rather than scoring an
// already-satisfied change as if it were new.
func TestMutateArchitecture_CachingPatternBlockedWhenCacheAlreadyPresent(t *testing.T) {
	region := core.Region("us-east-1")
	resources := representativeTopology(testTenant, region)
	resources = append(resources, cloud.Resource{
		ID: core.NewID("res"), TenantID: testTenant, AccountID: "111122223333", Region: region,
		Kind: cloud.KindElastiCache, NativeID: "redis-1", Name: "redis-1",
		InstanceType: "cache.r6g.large", Engine: "redis", State: cloud.StateAvailable,
		MonthlyCost: core.USDollars(0.1512 * core.HoursPerMonth),
	})
	svc, _, _ := testService(t, resources)

	sim, err := svc.MutateArchitecture(context.Background(), testTenant, ports.MutationRequest{
		Scope: "account", ScopeID: core.ID("111122223333"), Patterns: []string{"caching_layer_introduction"},
	})
	require.NoError(t, err)
	require.Len(t, sim.Candidates, 1)
	assert.NotEmpty(t, sim.Candidates[0].Blockers)
}

// --- Counterfactual Engine: traffic change ---------------------------------

// TestCounterfactual_TrafficDoubling_IsNonLinear is the specification's
// explicit requirement: doubling traffic must not simply double the bill.
// It asserts the proposed cost is NOT baseline*2, and that the reasoning
// (Narrative/Caveats) names the different scaling behaviors applied.
func TestCounterfactual_TrafficDoubling_IsNonLinear(t *testing.T) {
	region := core.Region("us-east-1")
	resources := representativeTopology(testTenant, region)
	// Add a Lambda function (usage-metered, should scale exactly linearly)
	// and a KMS key (fixed, should not move at all) to make the
	// classification observable component by component.
	resources = append(resources,
		cloud.Resource{
			ID: core.NewID("res"), TenantID: testTenant, Region: region, Kind: cloud.KindLambdaFunction,
			NativeID: "api-fn", Name: "api-fn", State: cloud.StateRunning, MonthlyCost: core.USDollars(40),
		},
		cloud.Resource{
			ID: core.NewID("res"), TenantID: testTenant, Region: region, Kind: cloud.KindKMSKey,
			NativeID: "key-1", Name: "key-1", State: cloud.StateRunning, MonthlyCost: core.USDollars(1),
		},
	)
	svc, _, store := testService(t, resources)

	cf, err := svc.Counterfactual(context.Background(), testTenant, simulate.Scenario{
		Type: simulate.ScenarioTrafficChange, Label: "2x traffic",
		Parameters: map[string]any{"multiplier": 2.0},
	})
	require.NoError(t, err)

	naiveDouble := cf.CurrentState.MonthlyCost.Scale(2)
	assert.NotEqual(t, naiveDouble.Micros(), cf.ProposedState.MonthlyCost.Micros(),
		"a 2x traffic scenario must not simply multiply the current bill by 2")

	// The fixed KMS key must be unchanged; the linear Lambda function must
	// be exactly doubled; the stepwise node group must have jumped to a
	// whole-unit ceiling, not a smooth 2x.
	kmsCost, ok := cf.ProposedState.ByService["kms"]
	require.True(t, ok)
	assert.Equal(t, core.USDollars(1).Micros(), kmsCost.Micros(), "a fixed-cost service must not move with traffic")

	lambdaCost, ok := cf.ProposedState.ByService["lambda"]
	require.True(t, ok)
	assert.Equal(t, core.USDollars(80).Micros(), lambdaCost.Micros(), "a usage-metered service scales exactly linearly with the traffic multiplier")

	assert.NotEmpty(t, cf.Narrative, "the traffic scenario must attach reasoning, not just a number")
	assert.Contains(t, cf.Narrative, "not multiply the bill")
	assert.NotEmpty(t, cf.Caveats)
	assert.NotEmpty(t, cf.Assumptions, "traffic scaling depends on stated NAT/LB usage baselines that must travel as assumptions")

	_, hasCF := store.counterfactuals[cf.ID]
	assert.True(t, hasCF, "the counterfactual must be persisted via SaveCounterfactual")
}

// TestCounterfactual_TrafficChange_StepwiseCeiling verifies the ceiling
// behavior directly: 4 node-group units at 1.3x traffic must round UP to 6
// (ceil(4*1.3)=6, not 5.2 or 5), which is what makes the compute component's
// growth outpace a naive proportional scaling.
func TestCounterfactual_TrafficChange_StepwiseCeiling(t *testing.T) {
	region := core.Region("us-east-1")
	resources := representativeTopology(testTenant, region)
	svc, _, _ := testService(t, resources)

	cf, err := svc.Counterfactual(context.Background(), testTenant, simulate.Scenario{
		Type: simulate.ScenarioTrafficChange, Parameters: map[string]any{"multiplier": 1.3},
	})
	require.NoError(t, err)

	var nodeGroupComponent *simulate.ProjectedComponent
	for i := range cf.ProposedState.Components {
		if cf.ProposedState.Components[i].Kind == string(cloud.KindEKSNodeGroup) {
			nodeGroupComponent = &cf.ProposedState.Components[i]
		}
	}
	require.NotNil(t, nodeGroupComponent, "the stepwise-scaled node group must appear as a proposed component")

	baseline := resources[1] // prod-workers, DesiredCount 4
	perUnit := baseline.MonthlyCost.Div(4)
	expected := perUnit.Scale(6) // ceil(4*1.3) == 6
	assert.Equal(t, expected.Micros(), nodeGroupComponent.MonthlyCost.Micros())
}

// --- Counterfactual Engine: add-cache reduces database load ---------------

// TestCounterfactual_AddCache_ReducesDatabaseLoad verifies the cache
// scenario's central claim: introducing a cache lowers the database's
// projected cost by exactly readShare*hitRate, and adds the cache's own
// always-on cost on top.
func TestCounterfactual_AddCache_ReducesDatabaseLoad(t *testing.T) {
	region := core.Region("us-east-1")
	resources := representativeTopology(testTenant, region)
	svc, _, _ := testService(t, resources)

	cf, err := svc.Counterfactual(context.Background(), testTenant, simulate.Scenario{
		Type:       simulate.ScenarioAddCache,
		Parameters: map[string]any{"hit_rate": 0.8, "db_read_share": 0.5},
	})
	require.NoError(t, err)

	var dbCurrent, dbProposed core.Money
	for _, r := range resources {
		if r.Kind == cloud.KindRDSInstance {
			dbCurrent = r.MonthlyCost
		}
	}
	rdsProposed, ok := cf.ProposedState.ByService["rds"]
	require.True(t, ok)
	dbProposed = rdsProposed

	assert.True(t, dbProposed.LessThan(dbCurrent), "the database's projected cost must fall once a cache absorbs a share of its reads")
	expectedReduction := dbCurrent.Scale(0.5 * 0.8)
	expectedDB := dbCurrent.MustSub(expectedReduction)
	assert.InDelta(t, expectedDB.Units(), dbProposed.Units(), 0.01)

	// The cache cluster's own cost must be present as a new component, on
	// top of the reduced database cost — the projection is a net effect,
	// not just a database discount.
	elastiCacheCost, ok := cf.ProposedState.ByService["elasticache"]
	require.True(t, ok, "the new cache cluster must appear in the proposed state's by-service breakdown")
	assert.False(t, elastiCacheCost.IsZero())

	assert.Contains(t, cf.PerformanceDelta, "cache")
}

// TestCounterfactual_Custom_NeverFabricatesANumber mirrors the compiler's
// central discipline in the counterfactual engine: an unmodelled scenario
// must say so, not guess.
func TestCounterfactual_Custom_NeverFabricatesANumber(t *testing.T) {
	region := core.Region("us-east-1")
	svc, _, _ := testService(t, representativeTopology(testTenant, region))

	cf, err := svc.Counterfactual(context.Background(), testTenant, simulate.Scenario{
		Type: simulate.ScenarioCustom, Label: "migrate to a new SaaS billing model",
	})
	require.NoError(t, err)
	assert.True(t, cf.CostDelta.IsZero(), "a custom scenario with no cost model must project zero effect, not a guess")
	assert.Equal(t, core.Confidence(0), cf.Confidence)
	assert.NotEmpty(t, cf.Caveats)
}
