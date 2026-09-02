// This file discovers S3 buckets. S3 is the one service in this package
// whose primary ListBuckets/Describe-equivalent calls are global (they work
// against any regional endpoint and return every bucket regardless of
// location), but whose per-bucket metadata calls (versioning, lifecycle,
// encryption) must be signed against the bucket's own region — mixing that
// up produces a redirect error, not wrong data, so this discoverer resolves
// each bucket's region with GetBucketLocation before making them.
//
// Bucket size and object count are not in the S3 API at all — the only
// place AWS publishes them is the daily AWS/S3 CloudWatch namespace — so
// this discoverer also reads those two metrics per bucket, accepting up to
// a day of staleness in exchange for not needing to list every object in
// every bucket (which the S3 API itself has no cheaper way to size).
package discovery

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type s3API interface {
	ListBuckets(ctx context.Context, in *s3.ListBucketsInput, optFns ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	GetBucketLocation(ctx context.Context, in *s3.GetBucketLocationInput, optFns ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error)
	GetBucketVersioning(ctx context.Context, in *s3.GetBucketVersioningInput, optFns ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
	GetBucketLifecycleConfiguration(ctx context.Context, in *s3.GetBucketLifecycleConfigurationInput, optFns ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error)
	GetBucketEncryption(ctx context.Context, in *s3.GetBucketEncryptionInput, optFns ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error)
	GetBucketTagging(ctx context.Context, in *s3.GetBucketTaggingInput, optFns ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error)
}

type s3MetricsAPI interface {
	GetMetricStatistics(ctx context.Context, in *cloudwatch.GetMetricStatisticsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricStatisticsOutput, error)
}

type S3Discoverer struct {
	newClient   func(aws.Config) s3API
	newCWClient func(aws.Config) s3MetricsAPI
	// regionalClient rebuilds the s3API client for a bucket's own region
	// once GetBucketLocation has resolved it, so the per-bucket calls below
	// are signed correctly. Defaults to newClient(cfg-with-region-swapped);
	// overridable so tests do not need a real per-region client factory.
	regionalClient func(cfg aws.Config, region string) s3API
}

var _ ports.ResourceDiscoverer = (*S3Discoverer)(nil)

func NewS3Discoverer() *S3Discoverer {
	newClient := func(cfg aws.Config) s3API { return s3.NewFromConfig(cfg) }
	return &S3Discoverer{
		newClient:   newClient,
		newCWClient: func(cfg aws.Config) s3MetricsAPI { return cloudwatch.NewFromConfig(cfg) },
		regionalClient: func(cfg aws.Config, region string) s3API {
			regional := cfg.Copy()
			regional.Region = region
			return newClient(regional)
		},
	}
}

func (d *S3Discoverer) Service() string     { return "s3" }
func (d *S3Discoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindS3Bucket} }
func (d *S3Discoverer) RequiredActions() []string {
	return []string{
		"s3:ListAllMyBuckets", "s3:GetBucketLocation", "s3:GetBucketVersioning",
		"s3:GetBucketLifecycleConfiguration", "s3:GetEncryptionConfiguration", "s3:GetBucketTagging",
		"cloudwatch:GetMetricStatistics",
	}
}

func (d *S3Discoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	out, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	b.countCall()
	if err != nil {
		return b.out, b.wrap(err, "s3", "ListBuckets", "s3:ListAllMyBuckets")
	}

	for _, bucket := range out.Buckets {
		name := aws.ToString(bucket.Name)
		region := d.bucketRegion(ctx, b, client, name, aws.ToString(bucket.BucketRegion))
		regional := d.regionalClient(cfg, region)
		addBucket(ctx, b, regional, d.newCWClient(regionalConfig(cfg, region)), in, bucket, region)
	}
	return b.out, nil
}

func regionalConfig(cfg aws.Config, region string) aws.Config {
	c := cfg.Copy()
	c.Region = region
	return c
}

// bucketRegion prefers the region ListBuckets already reported (a newer S3
// behaviour) and falls back to one GetBucketLocation call when it did not,
// so an older-style ListBuckets response does not cost every bucket an extra
// API call.
func (d *S3Discoverer) bucketRegion(ctx context.Context, b *builder, client s3API, name, fromList string) string {
	if fromList != "" {
		return fromList
	}
	b.countCall()
	out, err := client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(name)})
	if err != nil || out.LocationConstraint == "" {
		return "us-east-1" // GetBucketLocation returns an empty constraint for us-east-1 itself
	}
	return string(out.LocationConstraint)
}

func addBucket(ctx context.Context, b *builder, client s3API, cw s3MetricsAPI, in ports.DiscoveryInput, bucket s3types.Bucket, region string) {
	name := aws.ToString(bucket.Name)

	versioning := "Disabled"
	b.countCall()
	if v, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(name)}); err == nil && v.Status != "" {
		versioning = string(v.Status)
	}

	hasLifecycle := false
	b.countCall()
	if lc, err := client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(name)}); err == nil {
		hasLifecycle = len(lc.Rules) > 0
	}

	encrypted := false
	b.countCall()
	if enc, err := client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(name)}); err == nil && enc.ServerSideEncryptionConfiguration != nil {
		encrypted = len(enc.ServerSideEncryptionConfiguration.Rules) > 0
	}

	tags := core.Tags{}
	b.countCall()
	if tg, err := client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(name)}); err == nil {
		pairs := make([][2]string, 0, len(tg.TagSet))
		for _, t := range tg.TagSet {
			pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
		}
		tags = tagsFromKV(pairs)
	}

	sizeBytes, objectCount := s3StorageMetrics(ctx, b, cw, name)

	b.add(resourceSpec{
		Kind: cloud.KindS3Bucket, NativeID: name,
		ARN:  core.ARN("arn:aws:s3:::" + name),
		Name: name, Region: core.Region(region), State: cloud.StateAvailable,
		Capacity: cloud.Capacity{StorageGiB: sizeBytes / (1024 * 1024 * 1024), ObjectCount: objectCount},
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs(
			"versioning_enabled", boolStr(versioning == string(s3types.BucketVersioningStatusEnabled)),
			"has_lifecycle_policy", boolStr(hasLifecycle),
			"encrypted", boolStr(encrypted),
		),
		CreatedAt: aws.ToTime(bucket.CreationDate), DiscoveredBy: "aws.s3",
	})
}

// s3StorageMetrics reads the two daily AWS/S3 storage metrics over a
// trailing 2-day window (the metric is published once every 24h, so a
// 1-day window can legitimately miss the day's only data point) and takes
// the most recent data point rather than averaging across the window, since
// size is a gauge, not a rate.
func s3StorageMetrics(ctx context.Context, b *builder, cw s3MetricsAPI, bucket string) (bytes float64, objects int64) {
	end := time.Now().UTC()
	start := end.Add(-48 * time.Hour)

	b.countCall()
	sizeOut, err := cw.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace: aws.String("AWS/S3"), MetricName: aws.String("BucketSizeBytes"),
		Dimensions: []cwtypes.Dimension{
			{Name: aws.String("BucketName"), Value: aws.String(bucket)},
			{Name: aws.String("StorageType"), Value: aws.String("StandardStorage")},
		},
		StartTime: aws.Time(start), EndTime: aws.Time(end), Period: aws.Int32(86400),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticAverage},
	})
	if err == nil {
		bytes = latestAverage(sizeOut.Datapoints)
	} else {
		b.warnf("s3: could not read BucketSizeBytes for %s: %v", bucket, err)
	}

	b.countCall()
	objOut, err := cw.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace: aws.String("AWS/S3"), MetricName: aws.String("NumberOfObjects"),
		Dimensions: []cwtypes.Dimension{
			{Name: aws.String("BucketName"), Value: aws.String(bucket)},
			{Name: aws.String("StorageType"), Value: aws.String("AllStorageTypes")},
		},
		StartTime: aws.Time(start), EndTime: aws.Time(end), Period: aws.Int32(86400),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticAverage},
	})
	if err == nil {
		objects = int64(latestAverage(objOut.Datapoints))
	} else {
		b.warnf("s3: could not read NumberOfObjects for %s: %v", bucket, err)
	}
	return bytes, objects
}

func latestAverage(points []cwtypes.Datapoint) float64 {
	var latest *cwtypes.Datapoint
	for i := range points {
		if latest == nil || (points[i].Timestamp != nil && points[i].Timestamp.After(aws.ToTime(latest.Timestamp))) {
			latest = &points[i]
		}
	}
	if latest == nil {
		return 0
	}
	return aws.ToFloat64(latest.Average)
}
