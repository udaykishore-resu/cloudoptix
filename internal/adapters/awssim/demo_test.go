package awssim

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestBuildDemoEstate_TotalWithinTargetRange(t *testing.T) {
	e := BuildDemoEstate()
	total := e.TotalMonthlyCost().Units()
	t.Logf("demo estate total monthly cost: $%.2f", total)
	assert.GreaterOrEqual(t, total, 180_000.0, "demo estate should cost at least $180K/month")
	assert.LessOrEqual(t, total, 190_000.0, "demo estate should cost at most $190K/month")
}

func TestBuildDemoEstate_IdentifiableWasteWithinTargetRange(t *testing.T) {
	e := BuildDemoEstate()
	waste := e.EstimatedIdentifiableWaste()
	t.Logf("demo estate identifiable waste: $%.2f", waste.Total.Units())
	assert.GreaterOrEqual(t, waste.Total.Units(), 40_000.0, "identifiable waste should be at least $40K/month")
	assert.LessOrEqual(t, waste.Total.Units(), 50_000.0, "identifiable waste should be at most $50K/month")

	// Every category should be contributing something real; a zeroed
	// category would mean that part of the demo story silently isn't
	// costing anything, which would make it invisible to a rule engine.
	assert.True(t, waste.OversizedCompute.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.OldGenerationCompute.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.StoppedInstanceVolumes.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.UnattachedVolumes.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.GP2ShouldBeGP3.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.OldSnapshots.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.UnattachedEIPs.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.OversizedRDS.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.UnusedRDSReplicas.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.S3Lifecycle.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.LambdaOverprovisioning.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.EKSPacking.GreaterThan(core.ZeroUSD()))
	assert.True(t, waste.NATWithoutEndpoint.GreaterThan(core.ZeroUSD()))
}

func TestBuildDemoEstate_Deterministic(t *testing.T) {
	e1 := BuildDemoEstate()
	e2 := BuildDemoEstate()
	assert.Equal(t, e1.TotalMonthlyCost().Micros(), e2.TotalMonthlyCost().Micros(),
		"two builds from the same seed must produce byte-identical totals")
	assert.Equal(t, len(e1.EBSSnapshots), len(e2.EBSSnapshots))
	assert.Equal(t, len(e1.EC2Instances), len(e2.EC2Instances))
}

func TestBuildDemoEstate_ContainsEveryRequiredKind(t *testing.T) {
	e := BuildDemoEstate()
	assert.NotEmpty(t, e.EC2Instances)
	assert.NotEmpty(t, e.EBSVolumes)
	assert.NotEmpty(t, e.EBSSnapshots)
	assert.NotEmpty(t, e.ElasticIPs)
	assert.NotEmpty(t, e.AMIs)
	assert.NotEmpty(t, e.RDSInstances)
	assert.NotEmpty(t, e.RDSClusters)
	assert.NotEmpty(t, e.RDSSnapshots)
	assert.NotEmpty(t, e.DynamoDBTables)
	assert.NotEmpty(t, e.S3Buckets)
	assert.NotEmpty(t, e.LambdaFunctions)
	assert.NotEmpty(t, e.ECSClusters)
	assert.NotEmpty(t, e.ECSServices)
	assert.NotEmpty(t, e.EKSClusters)
	assert.NotEmpty(t, e.EKSNodeGroups)
	assert.NotEmpty(t, e.LoadBalancers)
	assert.NotEmpty(t, e.CloudFront)
	assert.NotEmpty(t, e.APIGateways)
	assert.NotEmpty(t, e.NATGateways)
	assert.NotEmpty(t, e.VPCs)
	assert.NotEmpty(t, e.Subnets)
	assert.NotEmpty(t, e.SecurityGroups)
	assert.NotEmpty(t, e.ElastiCacheClusters)
	assert.NotEmpty(t, e.SQSQueues)
	assert.NotEmpty(t, e.SNSTopics)
	assert.NotEmpty(t, e.LogGroups)
	assert.NotEmpty(t, e.KMSKeys)
	assert.NotEmpty(t, e.Secrets)

	// VPC endpoints are deliberately absent — that absence is the NAT waste
	// story, not an oversight, so this asserts it explicitly.
	assert.Empty(t, e.VPCEndpoints, "the demo estate must not pre-create the VPC endpoint the NAT-waste finding recommends")
}

func TestBuildDemoEstate_UnattachedGP2Count(t *testing.T) {
	e := BuildDemoEstate()
	var unattachedGP2 int
	for _, v := range e.EBSVolumes {
		if v.VolumeType == "gp2" && v.AttachedTo == "" {
			unattachedGP2++
		}
	}
	assert.InDelta(t, 12, unattachedGP2, 1, "roughly a dozen unattached gp2 volumes")
}

func TestBuildDemoEstate_UntaggedFractionApproximatelyRight(t *testing.T) {
	e := BuildDemoEstate()
	total, untagged := 0, 0
	count := func(tags map[string]string) {
		total++
		if _, ok := tags["Application"]; !ok {
			untagged++
		}
	}
	for _, r := range e.EC2Instances {
		count(r.Tags)
	}
	for _, r := range e.EBSVolumes {
		count(r.Tags)
	}
	for _, r := range e.EBSSnapshots {
		count(r.Tags)
	}
	for _, r := range e.S3Buckets {
		count(r.Tags)
	}
	require.Greater(t, total, 0)
	fraction := float64(untagged) / float64(total)
	t.Logf("untagged fraction across sampled kinds: %.3f", fraction)
	assert.InDelta(t, 0.15, fraction, 0.08, "untagged fraction should land near the fixture's declared 15%%")
}
