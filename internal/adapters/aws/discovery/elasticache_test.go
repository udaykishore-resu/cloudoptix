package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ecachetypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

type fakeElastiCache struct {
	pages [][]ecachetypes.CacheCluster
	call  int
	err   error
}

func (f *fakeElastiCache) DescribeCacheClusters(context.Context, *elasticache.DescribeCacheClustersInput, ...func(*elasticache.Options)) (*elasticache.DescribeCacheClustersOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.call >= len(f.pages) {
		return &elasticache.DescribeCacheClustersOutput{}, nil
	}
	page := f.pages[f.call]
	f.call++
	out := &elasticache.DescribeCacheClustersOutput{CacheClusters: page}
	if f.call < len(f.pages) {
		out.Marker = aws.String("more")
	}
	return out, nil
}
func (f *fakeElastiCache) ListTagsForResource(context.Context, *elasticache.ListTagsForResourceInput, ...func(*elasticache.Options)) (*elasticache.ListTagsForResourceOutput, error) {
	return &elasticache.ListTagsForResourceOutput{TagList: []ecachetypes.Tag{{Key: aws.String("Team"), Value: aws.String("platform")}}}, nil
}

func TestElastiCacheDiscoverer_NormalizesClusterAndTags(t *testing.T) {
	f := &fakeElastiCache{pages: [][]ecachetypes.CacheCluster{{{
		CacheClusterId: aws.String("redis-sessions"), ARN: aws.String("arn:aws:elasticache:us-east-1:222222222222:cluster:redis-sessions"),
		CacheClusterStatus: aws.String("available"), CacheNodeType: aws.String("cache.r6g.large"),
		Engine: aws.String("redis"), EngineVersion: aws.String("7.0"), NumCacheNodes: aws.Int32(1),
		PreferredAvailabilityZone: aws.String("us-east-1a"), ReplicationGroupId: aws.String("rg-1"),
	}}}}
	d := &ElastiCacheDiscoverer{newClient: func(aws.Config) elastiCacheAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	c := out.Resources[0]
	assert.Equal(t, "redis", c.Engine)
	assert.Equal(t, "cache.r6g.large", c.InstanceType)
	assert.Equal(t, 1, c.Capacity.InstanceCount)
	assert.Equal(t, "platform", c.Tags["Team"])
	assert.Equal(t, "rg-1", c.Attr("replication_group_id", ""))
}

func TestElastiCacheDiscoverer_ThrottleTranslates(t *testing.T) {
	d := &ElastiCacheDiscoverer{newClient: func(aws.Config) elastiCacheAPI {
		return &fakeElastiCache{err: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}}
	}}
	_, err := d.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrThrottled)
}

func TestElastiCacheDiscoverer_RequiredActions(t *testing.T) {
	d := NewElastiCacheDiscoverer()
	assert.Equal(t, "elasticache", d.Service())
	assert.Contains(t, d.RequiredActions(), "elasticache:DescribeCacheClusters")
}
