package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

type fakeCloudFront struct {
	dists []cftypes.DistributionSummary
}

func (f *fakeCloudFront) ListDistributions(context.Context, *cloudfront.ListDistributionsInput, ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error) {
	return &cloudfront.ListDistributionsOutput{DistributionList: &cftypes.DistributionList{Items: f.dists}}, nil
}
func (f *fakeCloudFront) ListTagsForResource(context.Context, *cloudfront.ListTagsForResourceInput, ...func(*cloudfront.Options)) (*cloudfront.ListTagsForResourceOutput, error) {
	return &cloudfront.ListTagsForResourceOutput{Tags: &cftypes.Tags{Items: []cftypes.Tag{{Key: aws.String("Environment"), Value: aws.String("prod")}}}}, nil
}

func TestCloudFrontDiscoverer_RoutesToS3Origin(t *testing.T) {
	f := &fakeCloudFront{dists: []cftypes.DistributionSummary{{
		Id: aws.String("E1234567890"), ARN: aws.String("arn:aws:cloudfront::222222222222:distribution/E1234567890"),
		Status: aws.String("Deployed"), DomainName: aws.String("d111abc.cloudfront.net"), Enabled: aws.Bool(true),
		PriceClass: cftypes.PriceClassPriceClass100, HttpVersion: cftypes.HttpVersionHttp2,
		DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{TargetOriginId: aws.String("origin-1")},
		Origins: &cftypes.Origins{Items: []cftypes.Origin{
			{Id: aws.String("origin-1"), DomainName: aws.String("assets-bucket.s3.us-east-1.amazonaws.com")},
		}},
	}}}
	d := &CloudFrontDiscoverer{newClient: func(aws.Config) cloudFrontAPI { return f }}

	in := discoveryInput()
	in.Existing = cloud.NewInventory([]cloud.Resource{
		{ID: "res-bucket-1", NativeID: "assets-bucket", Kind: cloud.KindS3Bucket},
	})

	out, err := d.Discover(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	dist := out.Resources[0]
	assert.Equal(t, cloud.KindCloudFront, dist.Kind)
	assert.Equal(t, "prod", dist.Tags["Environment"])
	assert.Equal(t, "assets-bucket.s3.us-east-1.amazonaws.com", dist.Attr("origin_domain", ""))

	assertHasEdge(t, out.Relationships, cloud.RelRoutesTo, dist.ID, "res-bucket-1")
}

func TestS3BucketFromOriginDomain(t *testing.T) {
	b, ok := s3BucketFromOriginDomain("my-bucket.s3.us-west-2.amazonaws.com")
	assert.True(t, ok)
	assert.Equal(t, "my-bucket", b)

	_, ok = s3BucketFromOriginDomain("api.example.com")
	assert.False(t, ok)
}

func TestCloudFrontDiscoverer_RequiredActions(t *testing.T) {
	d := NewCloudFrontDiscoverer()
	assert.Equal(t, "cloudfront", d.Service())
	assert.Contains(t, d.RequiredActions(), "cloudfront:ListDistributions")
}
