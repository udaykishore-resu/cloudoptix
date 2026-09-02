// This file discovers ElastiCache cache clusters. DescribeCacheClusters (as
// opposed to DescribeReplicationGroups) is used because it is the one call
// that covers both engines — Memcached, which has no replication group
// concept at all, and Redis/Valkey, where it still enumerates each node of
// a replication group individually — matching awssim's one-cluster-per-node
// model (ElastiCacheCluster.NumNodes). Tags require a second,
// per-cluster ListTagsForResource call (ElastiCache has no bulk tag-fetch
// API for cache clusters), so this is an N+1 like dynamodb.go and eks.go.
package discovery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ecachetypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type elastiCacheAPI interface {
	DescribeCacheClusters(ctx context.Context, in *elasticache.DescribeCacheClustersInput, optFns ...func(*elasticache.Options)) (*elasticache.DescribeCacheClustersOutput, error)
	ListTagsForResource(ctx context.Context, in *elasticache.ListTagsForResourceInput, optFns ...func(*elasticache.Options)) (*elasticache.ListTagsForResourceOutput, error)
}

type ElastiCacheDiscoverer struct {
	newClient func(aws.Config) elastiCacheAPI
}

var _ ports.ResourceDiscoverer = (*ElastiCacheDiscoverer)(nil)

func NewElastiCacheDiscoverer() *ElastiCacheDiscoverer {
	return &ElastiCacheDiscoverer{newClient: func(cfg aws.Config) elastiCacheAPI { return elasticache.NewFromConfig(cfg) }}
}

func (d *ElastiCacheDiscoverer) Service() string { return "elasticache" }
func (d *ElastiCacheDiscoverer) Kinds() []cloud.Kind {
	return []cloud.Kind{cloud.KindElastiCache}
}
func (d *ElastiCacheDiscoverer) RequiredActions() []string {
	return []string{"elasticache:DescribeCacheClusters", "elasticache:ListTagsForResource"}
}

func (d *ElastiCacheDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := elasticache.NewDescribeCacheClustersPaginator(client, &elasticache.DescribeCacheClustersInput{ShowCacheNodeInfo: aws.Bool(true)})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("elasticache: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			if isThrottledOrDenied(err) {
				return b.out, b.wrap(err, "elasticache", "DescribeCacheClusters", "elasticache:DescribeCacheClusters")
			}
			b.warnf("elasticache: could not describe cache clusters: %v", err)
			break
		}
		for _, c := range page.CacheClusters {
			d.addCluster(ctx, b, client, in, c)
		}
	}
	return b.out, nil
}

func (d *ElastiCacheDiscoverer) addCluster(ctx context.Context, b *builder, client elastiCacheAPI, in ports.DiscoveryInput, c ecachetypes.CacheCluster) {
	nativeID := aws.ToString(c.CacheClusterId)
	arn := aws.ToString(c.ARN)

	tags := core.Tags{}
	if arn != "" {
		b.countCall()
		if resp, err := client.ListTagsForResource(ctx, &elasticache.ListTagsForResourceInput{ResourceName: c.ARN}); err == nil {
			pairs := make([][2]string, 0, len(resp.TagList))
			for _, t := range resp.TagList {
				pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
			}
			tags = tagsFromKV(pairs)
		} else {
			b.warnf("elasticache: could not list tags for %s: %v", nativeID, err)
		}
	}

	b.add(resourceSpec{
		Kind: cloud.KindElastiCache, NativeID: nativeID, ARN: core.ARN(arn),
		Name: nativeID, Region: in.Region, AZ: aws.ToString(c.PreferredAvailabilityZone),
		State: mapState(aws.ToString(c.CacheClusterStatus)), InstanceType: aws.ToString(c.CacheNodeType),
		Engine: aws.ToString(c.Engine), EngineVer: aws.ToString(c.EngineVersion),
		Capacity: cloud.Capacity{InstanceCount: int(aws.ToInt32(c.NumCacheNodes))},
		Purchase: cloud.PurchaseOnDemand, Tags: tags,
		Attributes: attrs("replication_group_id", aws.ToString(c.ReplicationGroupId),
			"subnet_group", aws.ToString(c.CacheSubnetGroupName),
			"transit_encryption_enabled", boolStr(aws.ToBool(c.TransitEncryptionEnabled)),
			"at_rest_encryption_enabled", boolStr(aws.ToBool(c.AtRestEncryptionEnabled))),
		CreatedAt: aws.ToTime(c.CacheClusterCreateTime), DiscoveredBy: "aws.elasticache",
	})
}
