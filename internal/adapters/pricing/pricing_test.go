package pricing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestInstancePrice(t *testing.T) {
	c := New()
	tests := []struct {
		name         string
		region       core.Region
		instanceType string
		platform     string
		wantOK       bool
	}{
		{"known linux us-east-1", "us-east-1", "m5.large", "linux", true},
		{"case insensitive type and region", "US-EAST-1", "M5.LARGE", "Linux", true},
		{"known windows premium", "us-east-1", "m5.large", "windows", true},
		{"eu-west-1 costs more", "eu-west-1", "m5.large", "linux", true},
		{"unknown instance type", "us-east-1", "z9.giant", "linux", false},
		{"unknown region", "mars-central-1", "m5.large", "linux", false},
		{"unknown platform", "us-east-1", "m5.large", "os2warp", false},
		{"empty platform defaults linux", "us-east-1", "m5.large", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, ok := c.InstancePrice(tt.region, tt.instanceType, tt.platform)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.True(t, price.GreaterThan(core.ZeroUSD()), "price must be positive")
			} else {
				assert.True(t, price.IsZero())
			}
		})
	}
}

func TestInstancePrice_WindowsCostsMoreThanLinux(t *testing.T) {
	c := New()
	linux, ok := c.InstancePrice("us-east-1", "m5.large", "linux")
	require.True(t, ok)
	windows, ok := c.InstancePrice("us-east-1", "m5.large", "windows")
	require.True(t, ok)
	assert.True(t, windows.GreaterThan(linux))
}

func TestInstancePrice_RegionMultiplierApplied(t *testing.T) {
	c := New()
	east, ok := c.InstancePrice("us-east-1", "m5.large", "linux")
	require.True(t, ok)
	west, ok := c.InstancePrice("us-west-2", "m5.large", "linux")
	require.True(t, ok)
	euWest, ok := c.InstancePrice("eu-west-1", "m5.large", "linux")
	require.True(t, ok)

	assert.Equal(t, east.Micros(), west.Micros(), "us-east-1 and us-west-2 must match")
	assert.True(t, euWest.GreaterThan(east), "eu-west-1 must carry its documented premium")
}

func TestSpotPrice_CheaperThanOnDemand(t *testing.T) {
	c := New()
	od, ok := c.InstancePrice("us-east-1", "c5.xlarge", "linux")
	require.True(t, ok)
	spot, ok := c.SpotPrice("us-east-1", "c5.xlarge")
	require.True(t, ok)
	assert.True(t, spot.LessThan(od))
	// Spot should land in the documented 55-72% off range.
	ratio := spot.Ratio(od)
	assert.True(t, ratio > 0.20 && ratio < 0.60, "spot ratio %v out of expected range", ratio)
}

func TestSpotPrice_UnknownInstance(t *testing.T) {
	c := New()
	_, ok := c.SpotPrice("us-east-1", "nonexistent.type")
	assert.False(t, ok)
}

func TestStoragePrice_EBSAndS3(t *testing.T) {
	c := New()
	gp3, ok := c.StoragePrice("us-east-1", "gp3")
	require.True(t, ok)
	gp2, ok := c.StoragePrice("us-east-1", "gp2")
	require.True(t, ok)
	assert.True(t, gp3.LessThan(gp2), "gp3 should be cheaper per GiB than gp2")

	standard, ok := c.StoragePrice("us-east-1", "standard")
	require.True(t, ok)
	glacier, ok := c.StoragePrice("us-east-1", "deep_archive")
	require.True(t, ok)
	assert.True(t, glacier.LessThan(standard), "deep archive must be far cheaper than standard")

	_, ok = c.StoragePrice("us-east-1", "made_up_class")
	assert.False(t, ok)
}

func TestIOPSAndThroughputPrice(t *testing.T) {
	c := New()
	iops, ok := c.IOPSPrice("us-east-1", "gp3")
	require.True(t, ok)
	assert.True(t, iops.GreaterThan(core.ZeroUSD()))

	// gp2 does not bill provisioned IOPS separately.
	_, ok = c.IOPSPrice("us-east-1", "gp2")
	assert.False(t, ok)

	tp, ok := c.ThroughputPrice("us-east-1", "gp3")
	require.True(t, ok)
	assert.True(t, tp.GreaterThan(core.ZeroUSD()))

	_, ok = c.ThroughputPrice("us-east-1", "io1")
	assert.False(t, ok)
}

func TestDatabasePrice(t *testing.T) {
	c := New()
	single, ok := c.DatabasePrice("us-east-1", "db.m5.large", "postgres", false)
	require.True(t, ok)
	multi, ok := c.DatabasePrice("us-east-1", "db.m5.large", "postgres", true)
	require.True(t, ok)
	assert.True(t, multi.GreaterThan(single), "multi-AZ must cost more than single-AZ")
	assert.InDelta(t, 2.0, multi.Ratio(single), 0.001)

	aurora, ok := c.DatabasePrice("us-east-1", "db.m5.large", "aurora-postgresql", false)
	require.True(t, ok)
	assert.True(t, aurora.GreaterThan(single), "aurora premium must apply")

	_, ok = c.DatabasePrice("us-east-1", "db.m5.large", "oracle-ee", false)
	assert.False(t, ok, "unmodelled engine must not fabricate a price")

	_, ok = c.DatabasePrice("us-east-1", "db.fake.class", "postgres", false)
	assert.False(t, ok)
}

func TestCachePrice(t *testing.T) {
	c := New()
	redis, ok := c.CachePrice("us-east-1", "cache.m6g.large", "redis")
	require.True(t, ok)
	memcached, ok := c.CachePrice("us-east-1", "cache.m6g.large", "memcached")
	require.True(t, ok)
	assert.True(t, memcached.LessThan(redis))

	_, ok = c.CachePrice("us-east-1", "cache.does.not.exist", "redis")
	assert.False(t, ok)
}

func TestServicePrice(t *testing.T) {
	c := New()
	tests := []struct {
		service, dimension string
	}{
		{"nat_gateway", "hours"}, {"nat_gateway", "gb_processed"},
		{"alb", "hours"}, {"alb", "lcu_hour"},
		{"nlb", "hours"}, {"nlb", "lcu_hour"},
		{"cloudfront", "gb_out"}, {"cloudfront", "requests"},
		{"api_gateway", "rest_request"}, {"api_gateway", "http_request"},
		{"lambda", "gb_second"}, {"lambda", "request"},
		{"lambda", "provisioned_concurrency_gb_second"}, {"lambda", "arm_gb_second"},
		{"dynamodb", "rcu_hour"}, {"dynamodb", "wcu_hour"},
		{"dynamodb", "on_demand_read"}, {"dynamodb", "on_demand_write"},
		{"dynamodb", "storage_gb_month"},
		{"sqs", "requests"}, {"sns", "requests"},
		{"cloudwatch", "log_ingest_gb"}, {"cloudwatch", "log_storage_gb"},
		{"cloudwatch", "metric_month"}, {"cloudwatch", "dashboard_month"},
		{"eks", "cluster_hour"},
		{"fargate", "vcpu_hour"}, {"fargate", "gb_hour"},
		{"transit_gateway", "attachment_hour"}, {"transit_gateway", "gb_processed"},
		{"vpc_endpoint", "hour"}, {"vpc_endpoint", "gb_processed"},
		{"elastic_ip", "idle_hour"},
		{"kms", "key_month"},
		{"secretsmanager", "secret_month"},
		{"kinesis", "shard_hour"}, {"kinesis", "put_units"},
		{"s3", "put_request_per_1k"}, {"s3", "get_request_per_1k"},
		{"s3", "monitoring_per_million_objects"},
	}
	for _, tt := range tests {
		t.Run(tt.service+"/"+tt.dimension, func(t *testing.T) {
			price, ok := c.ServicePrice("us-east-1", tt.service, tt.dimension)
			require.True(t, ok, "expected a price for %s/%s", tt.service, tt.dimension)
			assert.True(t, price.GreaterThan(core.ZeroUSD()))
		})
	}

	_, ok := c.ServicePrice("us-east-1", "nat_gateway", "made_up_dimension")
	assert.False(t, ok)
	_, ok = c.ServicePrice("us-east-1", "made_up_service", "hours")
	assert.False(t, ok)
}

func TestDataTransferPrice(t *testing.T) {
	c := New()
	for _, dir := range []string{"internet_out", "cross_az", "cross_region", "nat_processed", "cloudfront_origin"} {
		_, ok := c.DataTransferPrice("us-east-1", dir)
		assert.True(t, ok, "expected a price for direction %s", dir)
	}
	internetOut, _ := c.DataTransferPrice("us-east-1", "internet_out")
	crossAZ, _ := c.DataTransferPrice("us-east-1", "cross_az")
	assert.True(t, internetOut.GreaterThan(crossAZ), "internet egress should be pricier than cross-AZ")

	_, ok := c.DataTransferPrice("us-east-1", "teleport")
	assert.False(t, ok)
}

func TestInstanceSpec(t *testing.T) {
	c := New()
	spec, ok := c.InstanceSpec("m5.2xlarge")
	require.True(t, ok)
	assert.Equal(t, "m5", spec.Family)
	assert.Equal(t, "2xlarge", spec.Size)
	assert.Equal(t, 5, spec.Generation)
	assert.Equal(t, 8.0, spec.VCPU)
	assert.Equal(t, 32.0, spec.MemoryGiB)
	assert.Equal(t, "x86_64", spec.Architecture)
	assert.False(t, spec.Burstable)
	assert.Equal(t, "m6i.2xlarge", spec.SuccessorType)

	tspec, ok := c.InstanceSpec("t3.micro")
	require.True(t, ok)
	assert.True(t, tspec.Burstable)

	_, ok = c.InstanceSpec("does.not.exist")
	assert.False(t, ok)
}

func TestInstanceFamily_OrderedSmallToLarge(t *testing.T) {
	c := New()
	family := c.InstanceFamily("m5.4xlarge")
	require.NotEmpty(t, family)
	assert.Equal(t, "m5.large", family[0])

	var lastVCPU float64
	for _, t2 := range family {
		spec, ok := c.InstanceSpec(t2)
		require.True(t, ok)
		assert.GreaterOrEqual(t, spec.VCPU, lastVCPU, "family must be ordered small to large")
		lastVCPU = spec.VCPU
	}

	assert.Nil(t, c.InstanceFamily("nonexistent.type"))
}

func TestSuccessorMapping(t *testing.T) {
	c := New()
	tests := []struct{ from, want string }{
		{"m4.large", "m5.large"},
		{"m5.large", "m6i.large"},
		{"c4.large", "c5.large"},
		{"r4.large", "r5.large"},
	}
	for _, tt := range tests {
		spec, ok := c.InstanceSpec(tt.from)
		require.True(t, ok)
		assert.Equal(t, tt.want, spec.SuccessorType, "successor of %s", tt.from)
	}

	// Current-generation types have no successor.
	spec, ok := c.InstanceSpec("m7i.large")
	require.True(t, ok)
	assert.Empty(t, spec.SuccessorType)
}

func TestCommitmentPrice(t *testing.T) {
	c := New()
	od, ok := c.InstancePrice("us-east-1", "m5.large", "linux")
	require.True(t, ok)

	cases := []struct {
		term, payment string
	}{
		{"1yr", "reserved_no_upfront"},
		{"1yr", "reserved_all_upfront"},
		{"3yr", "reserved_no_upfront"},
		{"3yr", "reserved_all_upfront"},
		{"1yr", "savings_plan_no_upfront"},
		{"3yr", "savings_plan_all_upfront"},
	}
	var prev core.Money
	for i, tt := range cases {
		price, ok := c.CommitmentPrice("us-east-1", "m5.large", tt.term, tt.payment)
		require.True(t, ok, "%s/%s", tt.term, tt.payment)
		assert.True(t, price.LessThan(od), "commitment must be cheaper than on-demand")
		_ = i
		_ = prev
	}

	// 3yr all-upfront must be the deepest discount of the set tested.
	threeYrAll, _ := c.CommitmentPrice("us-east-1", "m5.large", "3yr", "reserved_all_upfront")
	oneYrNo, _ := c.CommitmentPrice("us-east-1", "m5.large", "1yr", "reserved_no_upfront")
	assert.True(t, threeYrAll.LessThan(oneYrNo))

	_, ok = c.CommitmentPrice("us-east-1", "m5.large", "5yr", "reserved_no_upfront")
	assert.False(t, ok, "unsupported term must not fabricate a rate")

	_, ok = c.CommitmentPrice("us-east-1", "nonexistent.type", "1yr", "reserved_no_upfront")
	assert.False(t, ok)
}

func TestPricingDate(t *testing.T) {
	c := New()
	assert.False(t, c.PricingDate().IsZero())
}

func TestNew_PanicsNever(t *testing.T) {
	// The embedded book must always parse; this documents the guarantee.
	assert.NotPanics(t, func() { New() })
}
