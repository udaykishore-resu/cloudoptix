package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtatypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

type fakeResourceGroupsTagging struct {
	mappings []rgtatypes.ResourceTagMapping
}

func (f *fakeResourceGroupsTagging) GetResources(context.Context, *resourcegroupstaggingapi.GetResourcesInput, ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	return &resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: f.mappings}, nil
}

func TestResourceGroupsTaggingDiscoverer_ModelsUncoveredServices(t *testing.T) {
	f := &fakeResourceGroupsTagging{mappings: []rgtatypes.ResourceTagMapping{
		{
			ResourceARN: aws.String("arn:aws:kinesis:us-east-1:222222222222:stream/clickstream"),
			Tags:        []rgtatypes.Tag{{Key: aws.String("Environment"), Value: aws.String("prod")}},
		},
		{
			ResourceARN: aws.String("arn:aws:kafka:us-east-1:222222222222:cluster/analytics-msk/abc-123"),
		},
		{
			// Already covered by ec2.go's own discoverer — must be skipped.
			ResourceARN: aws.String("arn:aws:ec2:us-east-1:222222222222:instance/i-0123456789abcdef0"),
		},
		{
			// A service this package genuinely has no model for at all.
			ResourceARN: aws.String("arn:aws:redshift:us-east-1:222222222222:cluster:my-warehouse"),
		},
	}}
	d := &ResourceGroupsTaggingDiscoverer{newClient: func(aws.Config) resourceGroupsTaggingAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 3, "the ec2 instance must be skipped, not re-emitted")

	stream := mustFind(t, out.Resources, "arn:aws:kinesis:us-east-1:222222222222:stream/clickstream")
	assert.Equal(t, cloud.KindKinesisStream, stream.Kind)
	assert.Equal(t, "clickstream", stream.Name)
	assert.Equal(t, "prod", stream.Tags["Environment"])
	assert.Equal(t, "resourcegroupstaggingapi", stream.Attr("swept_by", ""))

	msk := mustFind(t, out.Resources, "arn:aws:kafka:us-east-1:222222222222:cluster/analytics-msk/abc-123")
	assert.Equal(t, cloud.KindMSKCluster, msk.Kind)

	unknown := mustFind(t, out.Resources, "arn:aws:redshift:us-east-1:222222222222:cluster:my-warehouse")
	assert.Equal(t, cloud.KindUnknown, unknown.Kind)
	assert.Equal(t, "my-warehouse", unknown.Name)
	assert.NotEmpty(t, out.Warnings)
}

func TestParseARN(t *testing.T) {
	service, region, resourcePart, ok := parseARN("arn:aws:kinesis:us-east-1:222222222222:stream/clickstream")
	require.True(t, ok)
	assert.Equal(t, "kinesis", service)
	assert.Equal(t, "us-east-1", region)
	assert.Equal(t, "stream/clickstream", resourcePart)

	_, _, _, ok = parseARN("not-an-arn")
	assert.False(t, ok)
}

func TestResourceNameFromARNPart(t *testing.T) {
	assert.Equal(t, "clickstream", resourceNameFromARNPart("stream/clickstream"))
	assert.Equal(t, "my-warehouse", resourceNameFromARNPart("cluster:my-warehouse"))
}

func TestResourceGroupsTaggingDiscoverer_RequiredActions(t *testing.T) {
	d := NewResourceGroupsTaggingDiscoverer()
	assert.Equal(t, "tag", d.Service())
	assert.Contains(t, d.RequiredActions(), "tag:GetResources")
}
