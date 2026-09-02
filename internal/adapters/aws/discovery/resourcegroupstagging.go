// This file is the sweeper: it walks every tagged resource in the account
// through resourcegroupstaggingapi's GetResources, the one AWS API that
// indexes resources across services without a per-service client, and uses
// it for exactly the two jobs no per-service discoverer in this package can
// do:
//
//  1. Model what's possible for the five services the hard rules say have
//     no SDK client at all — MSK, Kinesis, CloudTrail, Config, Route53 —
//     by mapping each tagged resource's ARN to the cloud.Kind the domain
//     already reserves for it (KindMSKCluster, KindKinesisStream,
//     KindCloudTrail, KindConfigRecorder, KindRoute53Zone) and recording
//     its ARN, region and tags. This is deliberately low-fidelity: without
//     a service-specific Describe call there is no state, capacity or
//     engine to read, so those fields are left at their zero value and
//     every swept resource carries a "swept_by"="resourcegroupstaggingapi"
//     attribute so a downstream engine can tell a richly-discovered
//     resource from a bare ARN-and-tags one.
//
//  2. Catch a resource type this package genuinely does not model, so it
//     surfaces as cloud.KindUnknown plus a warning — per cloud.Kind's own
//     documented contract ("discovery of an unmodelled type produces
//     KindUnknown plus a warning, never a silently mistyped resource") —
//     rather than vanishing from the inventory entirely.
//
// Every service this package already has a dedicated discoverer for
// (coveredARNServices below) is skipped here: that discoverer's output is
// strictly richer (real state, capacity, relationships) than anything the
// tagging API alone can produce, and emitting a second, poorer copy of the
// same resource would double-count it for every downstream engine.
package discovery

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtatypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type resourceGroupsTaggingAPI interface {
	GetResources(ctx context.Context, in *resourcegroupstaggingapi.GetResourcesInput, optFns ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

// arnServiceToKind maps the ARN service segment of a resource this package
// has no dedicated discoverer for onto the cloud.Kind the domain already
// reserves for it. Every key here is a service explicitly called out in the
// hard rules as having no available SDK client.
var arnServiceToKind = map[string]cloud.Kind{
	"kinesis":    cloud.KindKinesisStream,
	"kafka":      cloud.KindMSKCluster,
	"cloudtrail": cloud.KindCloudTrail,
	"config":     cloud.KindConfigRecorder,
	"route53":    cloud.KindRoute53Zone,
}

// coveredARNServices are the ARN service segments a dedicated discoverer in
// this package already owns. The sweeper must not re-emit these — see the
// package-level doc comment above for why.
var coveredARNServices = map[string]bool{
	"ec2": true, "rds": true, "s3": true, "lambda": true, "sqs": true, "sns": true,
	"dynamodb": true, "ecs": true, "eks": true, "elasticloadbalancing": true,
	"cloudfront": true, "apigateway": true, "elasticache": true, "kms": true,
	"secretsmanager": true, "events": true, "autoscaling": true,
}

type ResourceGroupsTaggingDiscoverer struct {
	newClient func(aws.Config) resourceGroupsTaggingAPI
}

var _ ports.ResourceDiscoverer = (*ResourceGroupsTaggingDiscoverer)(nil)

func NewResourceGroupsTaggingDiscoverer() *ResourceGroupsTaggingDiscoverer {
	return &ResourceGroupsTaggingDiscoverer{
		newClient: func(cfg aws.Config) resourceGroupsTaggingAPI { return resourcegroupstaggingapi.NewFromConfig(cfg) },
	}
}

func (d *ResourceGroupsTaggingDiscoverer) Service() string { return "tag" }
func (d *ResourceGroupsTaggingDiscoverer) Kinds() []cloud.Kind {
	kinds := make([]cloud.Kind, 0, len(arnServiceToKind)+1)
	for _, k := range arnServiceToKind {
		kinds = append(kinds, k)
	}
	return append(kinds, cloud.KindUnknown)
}
func (d *ResourceGroupsTaggingDiscoverer) RequiredActions() []string {
	return []string{"tag:GetResources"}
}

func (d *ResourceGroupsTaggingDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := resourcegroupstaggingapi.NewGetResourcesPaginator(client, &resourcegroupstaggingapi.GetResourcesInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("resourcegroupstaggingapi: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "tag", "GetResources", "tag:GetResources")
		}
		for _, m := range page.ResourceTagMappingList {
			d.addSweptResource(b, in, m)
		}
	}
	return b.out, nil
}

func (d *ResourceGroupsTaggingDiscoverer) addSweptResource(b *builder, in ports.DiscoveryInput, m rgtatypes.ResourceTagMapping) {
	arn := aws.ToString(m.ResourceARN)
	service, region, resourcePart, ok := parseARN(arn)
	if !ok || coveredARNServices[service] {
		return
	}

	kind, known := arnServiceToKind[service]
	if !known {
		kind = cloud.KindUnknown
		b.warnf("resourcegroupstaggingapi: unmodelled resource type %q (arn %s)", service, arn)
	}

	pairs := make([][2]string, 0, len(m.Tags))
	for _, t := range m.Tags {
		pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
	}
	nativeID := arn
	name := resourceNameFromARNPart(resourcePart)

	b.add(resourceSpec{
		Kind: kind, NativeID: nativeID, ARN: core.ARN(arn),
		Name: name, Region: core.Region(region), State: cloud.StateUnknown,
		Purchase: cloud.PurchaseUnknown, Tags: tagsFromKV(pairs),
		Attributes:   attrs("swept_by", "resourcegroupstaggingapi", "arn_service", service),
		DiscoveredBy: "aws.resourcegroupstaggingapi",
	})
}

// parseARN splits an ARN into its service, region and resource segments.
// Global-service ARNs (CloudFront, Route53) carry an empty region segment,
// which is passed through as-is rather than guessed.
func parseARN(arn string) (service, region, resourcePart string, ok bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return "", "", "", false
	}
	return parts[2], parts[3], parts[5], true
}

// resourceNameFromARNPart extracts the trailing name/id from an ARN's
// resource segment, which uses "/" (arn:...:cluster/my-cluster) or ":"
// (arn:...:log-group:my-group) as the separator depending on the service.
func resourceNameFromARNPart(resourcePart string) string {
	name := resourcePart
	if idx := strings.LastIndexAny(name, "/:"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}
