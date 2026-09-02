// This file discovers CloudFront distributions. CloudFront is a global
// service (one API endpoint, us-east-1, regardless of where content is
// served from) so, matching cloud.Resource's own rule that KindCloudFront
// resources carry no Region, this discoverer is expected to run once per
// account rather than once per region — a concern for the orchestrator that
// calls it, not for this file.
//
// A distribution's default-cache-behavior origin is resolved to a
// routes_to edge on a best-effort basis: an S3 origin domain
// (bucket.s3.amazonaws.com or bucket.s3.<region>.amazonaws.com) is matched
// against a bucket's own native id, and a custom origin domain is matched
// against an ALB/NLB's dns_name attribute (set by elbv2.go). Neither match
// is guaranteed — a distribution can point at an origin CloudOptix has never
// discovered (an origin outside this account, or a service without a
// dedicated discoverer) — so a miss is silently skipped rather than treated
// as an error.
package discovery

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type cloudFrontAPI interface {
	ListDistributions(ctx context.Context, in *cloudfront.ListDistributionsInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error)
	ListTagsForResource(ctx context.Context, in *cloudfront.ListTagsForResourceInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListTagsForResourceOutput, error)
}

type CloudFrontDiscoverer struct {
	newClient func(aws.Config) cloudFrontAPI
}

var _ ports.ResourceDiscoverer = (*CloudFrontDiscoverer)(nil)

func NewCloudFrontDiscoverer() *CloudFrontDiscoverer {
	return &CloudFrontDiscoverer{newClient: func(cfg aws.Config) cloudFrontAPI { return cloudfront.NewFromConfig(cfg) }}
}

func (d *CloudFrontDiscoverer) Service() string     { return "cloudfront" }
func (d *CloudFrontDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindCloudFront} }
func (d *CloudFrontDiscoverer) RequiredActions() []string {
	return []string{"cloudfront:ListDistributions", "cloudfront:ListTagsForResource"}
}

func (d *CloudFrontDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := cloudfront.NewListDistributionsPaginator(client, &cloudfront.ListDistributionsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("cloudfront: not available: %v", err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "cloudfront", "ListDistributions", "cloudfront:ListDistributions")
		}
		if page.DistributionList == nil {
			continue
		}
		for _, dist := range page.DistributionList.Items {
			d.addDistribution(ctx, b, client, dist)
		}
	}
	return b.out, nil
}

func (d *CloudFrontDiscoverer) addDistribution(ctx context.Context, b *builder, client cloudFrontAPI, dist cftypes.DistributionSummary) {
	nativeID := aws.ToString(dist.Id)
	arn := aws.ToString(dist.ARN)

	tags := core.Tags{}
	b.countCall()
	if tagResp, err := client.ListTagsForResource(ctx, &cloudfront.ListTagsForResourceInput{Resource: dist.ARN}); err == nil && tagResp.Tags != nil {
		pairs := make([][2]string, 0, len(tagResp.Tags.Items))
		for _, t := range tagResp.Tags.Items {
			pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
		}
		tags = tagsFromKV(pairs)
	} else if err != nil {
		b.warnf("cloudfront: could not list tags for %s: %v", nativeID, err)
	}

	originDomain := ""
	originID := ""
	if dist.DefaultCacheBehavior != nil {
		originID = aws.ToString(dist.DefaultCacheBehavior.TargetOriginId)
	}
	if dist.Origins != nil {
		for _, o := range dist.Origins.Items {
			if aws.ToString(o.Id) == originID || originID == "" {
				originDomain = aws.ToString(o.DomainName)
				break
			}
		}
	}

	id := b.add(resourceSpec{
		Kind: cloud.KindCloudFront, NativeID: nativeID, ARN: core.ARN(arn),
		Name: nativeID, State: mapState(aws.ToString(dist.Status)), Tags: tags,
		Purchase: cloud.PurchaseUnknown,
		Attributes: attrs("domain_name", aws.ToString(dist.DomainName), "origin_id", originID,
			"origin_domain", originDomain, "price_class", string(dist.PriceClass),
			"http_version", string(dist.HttpVersion), "enabled", boolStr(aws.ToBool(dist.Enabled))),
		CreatedAt: aws.ToTime(dist.LastModifiedTime), DiscoveredBy: "aws.cloudfront",
	})

	if toID, ok := resolveOriginTarget(b, originDomain); ok {
		b.edge(cloud.RelRoutesTo, id, toID, 1, 1.0, core.ProvenanceConfirmed)
	}
}

// resolveOriginTarget matches a CloudFront origin domain name against a
// previously discovered S3 bucket (by native id) or ALB/NLB (by dns_name
// attribute). See the package doc comment above for why this is
// best-effort.
func resolveOriginTarget(b *builder, originDomain string) (core.ID, bool) {
	if originDomain == "" || b.in.Existing == nil {
		return "", false
	}
	if bucket, ok := s3BucketFromOriginDomain(originDomain); ok {
		if id, ok := b.existingIDByNative(bucket); ok {
			return id, true
		}
	}
	for _, lb := range b.in.Existing.OfKinds(cloud.KindALB, cloud.KindNLB) {
		if lb.Attr("dns_name", "") == originDomain {
			return lb.ID, true
		}
	}
	return "", false
}

// s3BucketFromOriginDomain extracts a bucket name from an S3 REST origin
// domain such as "my-bucket.s3.amazonaws.com" or
// "my-bucket.s3.us-west-2.amazonaws.com". Website-endpoint origins
// (my-bucket.s3-website-us-west-2.amazonaws.com) use a different domain
// shape entirely and are not handled here.
func s3BucketFromOriginDomain(domain string) (string, bool) {
	const marker = ".s3."
	idx := strings.Index(domain, marker)
	if idx <= 0 {
		return "", false
	}
	return domain[:idx], true
}
