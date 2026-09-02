package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

type fakeS3 struct {
	buckets    []s3types.Bucket
	versioning s3types.BucketVersioningStatus
	lifecycle  int
	encrypted  bool
}

func (f *fakeS3) ListBuckets(context.Context, *s3.ListBucketsInput, ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	return &s3.ListBucketsOutput{Buckets: f.buckets}, nil
}
func (f *fakeS3) GetBucketLocation(context.Context, *s3.GetBucketLocationInput, ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error) {
	return &s3.GetBucketLocationOutput{}, nil
}
func (f *fakeS3) GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	return &s3.GetBucketVersioningOutput{Status: f.versioning}, nil
}
func (f *fakeS3) GetBucketLifecycleConfiguration(context.Context, *s3.GetBucketLifecycleConfigurationInput, ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error) {
	rules := make([]s3types.LifecycleRule, f.lifecycle)
	return &s3.GetBucketLifecycleConfigurationOutput{Rules: rules}, nil
}
func (f *fakeS3) GetBucketEncryption(context.Context, *s3.GetBucketEncryptionInput, ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error) {
	if !f.encrypted {
		return &s3.GetBucketEncryptionOutput{}, nil
	}
	return &s3.GetBucketEncryptionOutput{ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{
		Rules: []s3types.ServerSideEncryptionRule{{}},
	}}, nil
}
func (f *fakeS3) GetBucketTagging(context.Context, *s3.GetBucketTaggingInput, ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error) {
	return &s3.GetBucketTaggingOutput{TagSet: []s3types.Tag{{Key: aws.String("Team"), Value: aws.String("data")}}}, nil
}

type fakeS3CW struct{}

func (fakeS3CW) GetMetricStatistics(_ context.Context, in *cloudwatch.GetMetricStatisticsInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricStatisticsOutput, error) {
	var avg float64
	switch aws.ToString(in.MetricName) {
	case "BucketSizeBytes":
		avg = 5 * 1024 * 1024 * 1024 // 5 GiB
	case "NumberOfObjects":
		avg = 42000
	}
	return &cloudwatch.GetMetricStatisticsOutput{Datapoints: []cwtypes.Datapoint{
		{Timestamp: aws.Time(time.Now()), Average: aws.Float64(avg)},
	}}, nil
}

func TestS3Discoverer_NormalizesBucketWithStorageMetrics(t *testing.T) {
	f := &fakeS3{
		buckets:    []s3types.Bucket{{Name: aws.String("cloudoptix-data"), BucketRegion: aws.String("us-west-2"), CreationDate: aws.Time(time.Now())}},
		versioning: s3types.BucketVersioningStatusEnabled,
		lifecycle:  1,
		encrypted:  true,
	}
	d := &S3Discoverer{
		newClient:      func(aws.Config) s3API { return f },
		newCWClient:    func(aws.Config) s3MetricsAPI { return fakeS3CW{} },
		regionalClient: func(aws.Config, string) s3API { return f },
	}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	b := out.Resources[0]
	assert.Equal(t, "cloudoptix-data", b.NativeID)
	assert.Equal(t, core.Region("us-west-2"), b.Region)
	assert.Equal(t, "true", b.Attr("versioning_enabled", ""))
	assert.Equal(t, "true", b.Attr("has_lifecycle_policy", ""))
	assert.Equal(t, "true", b.Attr("encrypted", ""))
	assert.Equal(t, float64(5), b.Capacity.StorageGiB)
	assert.Equal(t, int64(42000), b.Capacity.ObjectCount)
	assert.Equal(t, "data", b.Tags["Team"])
}

func TestS3Discoverer_RequiredActions(t *testing.T) {
	d := NewS3Discoverer()
	assert.Equal(t, "s3", d.Service())
	assert.Contains(t, d.RequiredActions(), "cloudwatch:GetMetricStatistics")
}
