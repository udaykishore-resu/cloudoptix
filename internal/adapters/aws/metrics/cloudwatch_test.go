package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type fakeCloudWatch struct {
	callQueryCounts []int
	valuesByMetric  map[string]float64 // metric name -> canned value, applied to every query for that metric
	err             error
}

func (f *fakeCloudWatch) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.callQueryCounts = append(f.callQueryCounts, len(in.MetricDataQueries))
	var results []cwtypes.MetricDataResult
	for _, q := range in.MetricDataQueries {
		metricName := aws.ToString(q.MetricStat.Metric.MetricName)
		val, ok := f.valuesByMetric[metricName]
		if !ok {
			val = 1.0
		}
		results = append(results, cwtypes.MetricDataResult{
			Id: q.Id, StatusCode: cwtypes.StatusCodeComplete,
			Timestamps: []time.Time{time.Now()}, Values: []float64{val},
		})
	}
	return &cloudwatch.GetMetricDataOutput{MetricDataResults: results}, nil
}

func ec2Resource(id core.ID, nativeID string) cloud.Resource {
	return cloud.Resource{ID: id, Kind: cloud.KindEC2Instance, NativeID: nativeID}
}

func TestCollector_EC2_CPUAndNetworkRates(t *testing.T) {
	f := &fakeCloudWatch{valuesByMetric: map[string]float64{
		"CPUUtilization": 55.0, "NetworkIn": 6000.0, "NetworkOut": 3000.0,
	}}
	c := &Collector{newClient: func(aws.Config) cloudWatchAPI { return f }}

	out, err := c.Collect(context.Background(), ports.MetricCollectInput{
		TenantID: "tenant-1", Session: testSession(), Region: "us-east-1",
		Resources: []cloud.Resource{ec2Resource("res-1", "i-abc")}, Window: testWindow(),
	})
	require.NoError(t, err)
	require.Len(t, out, 1)

	rm := out[0]
	require.NotNil(t, rm.CPU)
	assert.InDelta(t, 55.0, rm.CPU.Mean, 0.001)
	require.NotNil(t, rm.NetworkIn)
	assert.InDelta(t, 6000.0/60, rm.NetworkIn.Mean, 0.001, "Sum-statistic NetworkIn must be divided by period to become a rate")
	require.NotNil(t, rm.NetworkOut)
	assert.InDelta(t, 3000.0/60, rm.NetworkOut.Mean, 0.001)
	assert.Equal(t, "cloudwatch", rm.Source)
	assert.Greater(t, rm.Coverage, 0.0)
}

func TestCollector_UnsupportedKindIsSkippedWithoutAnyAPICall(t *testing.T) {
	f := &fakeCloudWatch{}
	c := &Collector{newClient: func(aws.Config) cloudWatchAPI { return f }}

	out, err := c.Collect(context.Background(), ports.MetricCollectInput{
		TenantID: "tenant-1", Session: testSession(), Region: "us-east-1",
		Resources: []cloud.Resource{{ID: "res-1", Kind: cloud.KindVPC, NativeID: "vpc-1"}}, Window: testWindow(),
	})
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Empty(t, f.callQueryCounts)
}

func TestCollector_BatchesAt500QueriesPerCall(t *testing.T) {
	f := &fakeCloudWatch{}
	c := &Collector{newClient: func(aws.Config) cloudWatchAPI { return f }}

	// 200 EC2 instances * 3 metrics each = 600 queries -> two batches (500 + 100).
	var resources []cloud.Resource
	for i := 0; i < 200; i++ {
		resources = append(resources, ec2Resource(core.ID("res"), "i-x"))
	}
	_, err := c.Collect(context.Background(), ports.MetricCollectInput{
		TenantID: "tenant-1", Session: testSession(), Region: "us-east-1", Resources: resources, Window: testWindow(),
	})
	require.NoError(t, err)
	require.Len(t, f.callQueryCounts, 2)
	assert.Equal(t, 500, f.callQueryCounts[0])
	assert.Equal(t, 100, f.callQueryCounts[1])
}

func TestCollector_ErrorRateFromNumeratorAndDenominator(t *testing.T) {
	f := &fakeCloudWatch{valuesByMetric: map[string]float64{"Errors": 5.0, "Invocations": 100.0}}
	c := &Collector{newClient: func(aws.Config) cloudWatchAPI { return f }}

	out, err := c.Collect(context.Background(), ports.MetricCollectInput{
		TenantID: "tenant-1", Session: testSession(), Region: "us-east-1",
		Resources: []cloud.Resource{{ID: "res-1", Kind: cloud.KindLambdaFunction, NativeID: "fn"}}, Window: testWindow(),
	})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].ErrorRate)
	// Errors=5 summed over the window, Invocations=100 summed: 5/100 = 0.05.
	assert.InDelta(t, 0.05, out[0].ErrorRate.Mean, 0.001)
}

func TestCollector_ThrottleTranslates(t *testing.T) {
	f := &fakeCloudWatch{err: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}}
	c := &Collector{newClient: func(aws.Config) cloudWatchAPI { return f }}
	_, err := c.Collect(context.Background(), ports.MetricCollectInput{
		TenantID: "tenant-1", Session: testSession(), Region: "us-east-1",
		Resources: []cloud.Resource{ec2Resource("res-1", "i-abc")}, Window: testWindow(),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrThrottled)
}

func TestCollector_Available(t *testing.T) {
	f := &fakeCloudWatch{}
	c := &Collector{newClient: func(aws.Config) cloudWatchAPI { return f }}
	assert.True(t, c.Available(context.Background(), testSession()))
	assert.False(t, c.Available(context.Background(), nil))
}

func TestCollector_Source(t *testing.T) {
	assert.Equal(t, "cloudwatch", NewCollector().Source())
}

func TestSpecsFor_EveryDocumentedKindHasSpecs(t *testing.T) {
	kinds := []cloud.Kind{
		cloud.KindEC2Instance, cloud.KindRDSInstance, cloud.KindLambdaFunction, cloud.KindALB, cloud.KindNLB,
		cloud.KindDynamoDBTable, cloud.KindElastiCache, cloud.KindS3Bucket, cloud.KindECSService,
	}
	for _, k := range kinds {
		specs := specsFor(cloud.Resource{Kind: k, NativeID: "x", ARN: "arn:aws:elasticloadbalancing:us-east-1:1:loadbalancer/app/x/1"}, 60)
		assert.NotEmptyf(t, specs, "expected metric specs for %s", k)
	}
}

func TestSpecsFor_UnsupportedKindReturnsNil(t *testing.T) {
	assert.Nil(t, specsFor(cloud.Resource{Kind: cloud.KindVPC}, 60))
}

func TestAlbDimensionValue(t *testing.T) {
	v := albDimensionValue("arn:aws:elasticloadbalancing:us-east-1:222222222222:loadbalancer/app/web/abc123")
	assert.Equal(t, "app/web/abc123", v)
	assert.Empty(t, albDimensionValue("not-an-arn"))
}

func TestMinPeriodForWindow(t *testing.T) {
	now := time.Now().UTC()
	assert.EqualValues(t, 60, minPeriodForWindow(core.NewPeriod(now.Add(-time.Hour), now)))
	assert.EqualValues(t, 300, minPeriodForWindow(core.NewPeriod(now.AddDate(0, 0, -30), now)))
	assert.EqualValues(t, 3600, minPeriodForWindow(core.NewPeriod(now.AddDate(0, 0, -90), now)))
}

func TestPeriodFor_HonorsCoarserRequestedStep(t *testing.T) {
	window := core.NewPeriod(time.Now().Add(-time.Hour), time.Now())
	assert.EqualValues(t, 60, periodFor(window, 0))
	assert.EqualValues(t, 300, periodFor(window, 300), "a coarser requested step must be honored")
	assert.EqualValues(t, 60, periodFor(window, 10), "a finer requested step must not undercut the retention-driven floor")
}

func TestChunkQueries(t *testing.T) {
	items := make([]cwtypes.MetricDataQuery, 7)
	chunks := chunkQueries(items, 3)
	require.Len(t, chunks, 3)
	assert.Len(t, chunks[0], 3)
	assert.Len(t, chunks[1], 3)
	assert.Len(t, chunks[2], 1)
}

func TestErrorRate_NoDenominatorIsUndefined(t *testing.T) {
	_, _, ok := errorRate([]float64{5}, nil, 10)
	assert.False(t, ok)
}

func TestErrorRate_ZeroDenominatorIsZeroRate(t *testing.T) {
	rate, _, ok := errorRate(nil, []float64{0, 0}, 10)
	require.True(t, ok)
	assert.Equal(t, 0.0, rate)
}
