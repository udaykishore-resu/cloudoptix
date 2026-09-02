package optimization

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// TestComputeBlastRadius_IncompleteGraphNeverReadsAsSmall is the invariant
// this file exists to guarantee: an empty or thin topology must never
// produce a lower blast-radius score than what an honestly "small" but
// fully-observed graph would produce. Missing information pushes the score
// up, not down.
func TestComputeBlastRadius_IncompleteGraphNeverReadsAsSmall(t *testing.T) {
	r := mkResource(cloud.KindEC2Instance, "m5.large")

	t.Run("empty topology entirely: lowest completeness, non-zero score", func(t *testing.T) {
		inv := cloud.NewInventory([]cloud.Resource{r})
		ctx := testEvalContext(inv, cloud.NewTopology(nil), nil, testSpec())
		b := ComputeBlastRadius(ctx, r)
		assert.InDelta(t, 0.1, b.Completeness, 1e-9)
		assert.Greater(t, b.Score, 0.0, "zero known dependents on an empty graph must still carry a non-zero risk score")
		assert.Equal(t, 0, b.ResourcesAffected)
	})

	t.Run("compute resource with zero dependents on a non-empty graph reads as more uncertain than a leaf kind", func(t *testing.T) {
		other := mkResource(cloud.KindS3Bucket, "")
		s3Leaf := mkResource(cloud.KindS3Bucket, "")
		// A populated topology that simply has no edges touching r or s3Leaf.
		unrelatedA, unrelatedB := mkResource(cloud.KindEC2Instance, "m5.large"), mkResource(cloud.KindRDSInstance, "db.m5.large")
		edges := []cloud.Relationship{
			{ID: core.NewID("rel"), FromID: unrelatedA.ID, ToID: unrelatedB.ID, Kind: cloud.RelDependsOn, Confidence: 0.9},
		}
		topo := cloud.NewTopology(edges)
		inv := cloud.NewInventory([]cloud.Resource{r, other, s3Leaf, unrelatedA, unrelatedB})

		ctxCompute := testEvalContext(inv, topo, nil, testSpec())
		computeBlast := ComputeBlastRadius(ctxCompute, r) // EC2Instance: CategoryCompute

		ctxLeaf := testEvalContext(inv, topo, nil, testSpec())
		leafBlast := ComputeBlastRadius(ctxLeaf, s3Leaf) // S3Bucket: not compute/database

		assert.InDelta(t, 0.35, computeBlast.Completeness, 1e-9)
		assert.InDelta(t, 0.9, leafBlast.Completeness, 1e-9)
		assert.Greater(t, computeBlast.Score, leafBlast.Score,
			"an unresolved compute resource must read as riskier than a resource whose isolation is more plausibly real")
	})
}

// TestComputeBlastRadius_RealDependents checks that a resource with actual
// discovered dependents produces a completeness driven by edge confidence
// and a score that responds to fan-out and criticality, not just presence.
func TestComputeBlastRadius_RealDependents(t *testing.T) {
	db := mkResource(cloud.KindRDSInstance, "db.m5.large")
	svc1 := mkResource(cloud.KindEC2Instance, "m5.large")
	svc1.Criticality = core.CriticalityTier0
	svc1.WorkloadID = core.NewID("wl")
	svc2 := mkResource(cloud.KindEC2Instance, "m5.large")
	svc2.WorkloadID = core.NewID("wl")

	edges := []cloud.Relationship{
		{ID: core.NewID("rel"), FromID: svc1.ID, ToID: db.ID, Kind: cloud.RelDependsOn, Confidence: 0.9},
		{ID: core.NewID("rel"), FromID: svc2.ID, ToID: db.ID, Kind: cloud.RelDependsOn, Confidence: 0.6},
	}
	topo := cloud.NewTopology(edges)
	inv := cloud.NewInventory([]cloud.Resource{db, svc1, svc2})
	ctx := testEvalContext(inv, topo, nil, testSpec())

	b := ComputeBlastRadius(ctx, db)
	require.Equal(t, 2, b.ResourcesAffected)
	assert.InDelta(t, (0.9+0.6)/2, b.Completeness, 1e-9)
	assert.Equal(t, 1, b.CriticalServices, "svc1's Tier0 criticality must be reflected")
	assert.Greater(t, b.Score, 0.0)
}
