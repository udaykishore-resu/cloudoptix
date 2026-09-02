// This file discovers ELBv2 load balancers (ALB/NLB) and their target
// groups. Two relationship hops matter here and are wired in the order the
// data becomes available: load balancer routes_to target group (known from
// TargetGroup.LoadBalancerArns, same pass) and target group routes_to each
// EC2 instance it forwards to (known from DescribeTargetHealth, resolved
// against in.Existing since EC2 is a different discoverer's resource and may
// not have run yet in this scan). Gateway Load Balancers are folded into
// KindNLB — cloud.Kind has no separate "gateway" kind and a GWLB is, like an
// NLB, a layer-3/4 balancer rather than an HTTP-aware one.
package discovery

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type elbv2API interface {
	DescribeLoadBalancers(ctx context.Context, in *elbv2.DescribeLoadBalancersInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error)
	DescribeTargetGroups(ctx context.Context, in *elbv2.DescribeTargetGroupsInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(ctx context.Context, in *elbv2.DescribeTargetHealthInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error)
	DescribeTags(ctx context.Context, in *elbv2.DescribeTagsInput, optFns ...func(*elbv2.Options)) (*elbv2.DescribeTagsOutput, error)
}

type ELBv2Discoverer struct {
	newClient func(aws.Config) elbv2API
}

var _ ports.ResourceDiscoverer = (*ELBv2Discoverer)(nil)

func NewELBv2Discoverer() *ELBv2Discoverer {
	return &ELBv2Discoverer{newClient: func(cfg aws.Config) elbv2API { return elbv2.NewFromConfig(cfg) }}
}

func (d *ELBv2Discoverer) Service() string { return "elasticloadbalancing" }
func (d *ELBv2Discoverer) Kinds() []cloud.Kind {
	return []cloud.Kind{cloud.KindALB, cloud.KindNLB, cloud.KindTargetGroup}
}
func (d *ELBv2Discoverer) RequiredActions() []string {
	return []string{
		"elasticloadbalancing:DescribeLoadBalancers", "elasticloadbalancing:DescribeTargetGroups",
		"elasticloadbalancing:DescribeTargetHealth", "elasticloadbalancing:DescribeTags",
	}
}

func (d *ELBv2Discoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)

	var lbs []elbv2types.LoadBalancer
	lp := elbv2.NewDescribeLoadBalancersPaginator(client, &elbv2.DescribeLoadBalancersInput{})
	for lp.HasMorePages() {
		b.countCall()
		page, err := lp.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("elbv2: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "elasticloadbalancing", "DescribeLoadBalancers", "elasticloadbalancing:DescribeLoadBalancers")
		}
		lbs = append(lbs, page.LoadBalancers...)
	}

	lbArns := make([]string, 0, len(lbs))
	for _, lb := range lbs {
		lbArns = append(lbArns, aws.ToString(lb.LoadBalancerArn))
	}
	lbTags := d.describeTags(ctx, b, client, lbArns)
	for _, lb := range lbs {
		addLoadBalancer(b, in, lb, lbTags[aws.ToString(lb.LoadBalancerArn)])
	}

	var tgs []elbv2types.TargetGroup
	tp := elbv2.NewDescribeTargetGroupsPaginator(client, &elbv2.DescribeTargetGroupsInput{})
	for tp.HasMorePages() {
		b.countCall()
		page, err := tp.NextPage(ctx)
		if err != nil {
			b.warnf("elbv2: could not describe target groups: %v", err)
			break
		}
		tgs = append(tgs, page.TargetGroups...)
	}

	tgArns := make([]string, 0, len(tgs))
	for _, tg := range tgs {
		tgArns = append(tgArns, aws.ToString(tg.TargetGroupArn))
	}
	tgTags := d.describeTags(ctx, b, client, tgArns)

	for _, tg := range tgs {
		tgID := addTargetGroup(b, in, tg, tgTags[aws.ToString(tg.TargetGroupArn)])
		for _, lbArn := range tg.LoadBalancerArns {
			if lbID, ok := b.idOf(lbArn); ok {
				b.edge(cloud.RelRoutesTo, lbID, tgID, 1, 1.0, core.ProvenanceConfirmed)
			}
		}
		d.linkTargets(ctx, b, client, tg, tgID)
	}
	return b.out, nil
}

// describeTags batches ARNs into DescribeTags calls of at most 20 (the API's
// own limit) and returns a map of ARN -> tags. Errors are warnings, not
// failures: tags are enrichment, and a permission gap on
// elasticloadbalancing:DescribeTags should not stop resources and edges
// (the parts every downstream engine actually depends on) from being
// discovered.
func (d *ELBv2Discoverer) describeTags(ctx context.Context, b *builder, client elbv2API, arns []string) map[string]core.Tags {
	out := map[string]core.Tags{}
	for _, batch := range chunkStrings(arns, 20) {
		if len(batch) == 0 {
			continue
		}
		b.countCall()
		resp, err := client.DescribeTags(ctx, &elbv2.DescribeTagsInput{ResourceArns: batch})
		if err != nil {
			b.warnf("elbv2: could not describe tags: %v", err)
			continue
		}
		for _, td := range resp.TagDescriptions {
			pairs := make([][2]string, 0, len(td.Tags))
			for _, t := range td.Tags {
				pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
			}
			out[aws.ToString(td.ResourceArn)] = tagsFromKV(pairs)
		}
	}
	return out
}

func (d *ELBv2Discoverer) linkTargets(ctx context.Context, b *builder, client elbv2API, tg elbv2types.TargetGroup, tgID core.ID) {
	if tg.TargetType != elbv2types.TargetTypeEnumInstance {
		// ip/lambda/alb target types don't resolve to an EC2 instance id;
		// modeling those would need the target group's own discoverer to
		// know about Lambda/ALB kinds it doesn't otherwise touch, so they
		// are left as target-group-level facts (Capacity.InstanceCount is
		// still correct) rather than mis-wired edges.
		return
	}
	b.countCall()
	health, err := client.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{TargetGroupArn: tg.TargetGroupArn})
	if err != nil {
		b.warnf("elbv2: could not describe target health for %s: %v", aws.ToString(tg.TargetGroupName), err)
		return
	}
	weight := 1.0
	if n := len(health.TargetHealthDescriptions); n > 0 {
		weight = 1.0 / float64(n)
	}
	for _, th := range health.TargetHealthDescriptions {
		if th.Target == nil {
			continue
		}
		instID := aws.ToString(th.Target.Id)
		if instanceID, ok := b.existingIDByNative(instID); ok {
			b.edge(cloud.RelRoutesTo, tgID, instanceID, weight, 1.0, core.ProvenanceConfirmed)
		}
	}
}

func addLoadBalancer(b *builder, in ports.DiscoveryInput, lb elbv2types.LoadBalancer, tags core.Tags) core.ID {
	kind := cloud.KindALB
	if lb.Type == elbv2types.LoadBalancerTypeEnumNetwork || lb.Type == elbv2types.LoadBalancerTypeEnumGateway {
		kind = cloud.KindNLB
	}
	stateCode := ""
	if lb.State != nil {
		stateCode = string(lb.State.Code)
	}
	nativeID := aws.ToString(lb.LoadBalancerArn)
	return b.add(resourceSpec{
		Kind: kind, NativeID: nativeID, ARN: core.ARN(nativeID),
		Name: aws.ToString(lb.LoadBalancerName), Region: in.Region, State: mapState(stateCode),
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("scheme", string(lb.Scheme), "vpc_id", aws.ToString(lb.VpcId),
			"dns_name", aws.ToString(lb.DNSName), "type", string(lb.Type),
			"security_group_ids", strings.Join(lb.SecurityGroups, ",")),
		CreatedAt: aws.ToTime(lb.CreatedTime), DiscoveredBy: "aws.elbv2",
	})
}

func addTargetGroup(b *builder, in ports.DiscoveryInput, tg elbv2types.TargetGroup, tags core.Tags) core.ID {
	nativeID := aws.ToString(tg.TargetGroupArn)
	primaryLB := ""
	if len(tg.LoadBalancerArns) > 0 {
		primaryLB = tg.LoadBalancerArns[0]
	}
	return b.add(resourceSpec{
		Kind: cloud.KindTargetGroup, NativeID: nativeID, ARN: core.ARN(nativeID),
		Name: aws.ToString(tg.TargetGroupName), Region: in.Region, State: cloud.StateInUse,
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("target_type", string(tg.TargetType), "protocol", string(tg.Protocol),
			"port", istr(int64(aws.ToInt32(tg.Port))), "load_balancer_id", primaryLB),
		DiscoveredBy: "aws.elbv2",
	})
}
