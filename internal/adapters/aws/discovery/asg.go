// This file discovers Auto Scaling groups and wires their membership edges
// onto EC2 instances already known to the estate.
package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// asgAPI is the one call this discoverer makes.
type asgAPI interface {
	DescribeAutoScalingGroups(ctx context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
}

// ASGDiscoverer implements ports.ResourceDiscoverer for Auto Scaling groups.
type ASGDiscoverer struct {
	newClient func(aws.Config) asgAPI
}

var _ ports.ResourceDiscoverer = (*ASGDiscoverer)(nil)

func NewASGDiscoverer() *ASGDiscoverer {
	return &ASGDiscoverer{newClient: func(cfg aws.Config) asgAPI { return autoscaling.NewFromConfig(cfg) }}
}

func (d *ASGDiscoverer) Service() string     { return "autoscaling" }
func (d *ASGDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindAutoScalingGroup} }
func (d *ASGDiscoverer) RequiredActions() []string {
	return []string{"autoscaling:DescribeAutoScalingGroups"}
}

func (d *ASGDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := autoscaling.NewDescribeAutoScalingGroupsPaginator(client, &autoscaling.DescribeAutoScalingGroupsInput{
		MaxRecords: aws.Int32(100),
	})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("autoscaling: not available in region %s: %v", in.Region, err)
				break
			}
			return b.out, b.wrap(err, "autoscaling", "DescribeAutoScalingGroups", "autoscaling:DescribeAutoScalingGroups")
		}
		for _, g := range page.AutoScalingGroups {
			addASG(b, in, g)
		}
	}
	return b.out, nil
}

func addASG(b *builder, in ports.DiscoveryInput, g asgtypes.AutoScalingGroup) {
	tags := asgTags(g.Tags)
	nativeID := aws.ToString(g.AutoScalingGroupName)
	inService := 0
	for _, i := range g.Instances {
		if i.LifecycleState == asgtypes.LifecycleStateInService {
			inService++
		}
	}
	id := b.add(resourceSpec{
		Kind: cloud.KindAutoScalingGroup, NativeID: nativeID, ARN: core.ARN(aws.ToString(g.AutoScalingGroupARN)),
		Name: nativeID, Region: in.Region, State: cloud.StateAvailable,
		Capacity: cloud.Capacity{
			InstanceCount: len(g.Instances), DesiredCount: int(aws.ToInt32(g.DesiredCapacity)),
			MinCount: int(aws.ToInt32(g.MinSize)), MaxCount: int(aws.ToInt32(g.MaxSize)),
		},
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs(
			"health_check_type", aws.ToString(g.HealthCheckType),
			"launch_template_id", launchTemplateID(g),
			"target_group_arns", strings.Join(g.TargetGroupARNs, ","),
			"in_service_count", fmt.Sprintf("%d", inService),
		),
		CreatedAt: aws.ToTime(g.CreatedTime), DiscoveredBy: "aws.autoscaling",
	})
	for _, i := range g.Instances {
		if toID, ok := b.existingIDByNative(aws.ToString(i.InstanceId)); ok {
			b.edge(cloud.RelContains, id, toID, 1, 1.0, core.ProvenanceConfirmed)
		}
	}
}

func launchTemplateID(g asgtypes.AutoScalingGroup) string {
	if g.LaunchTemplate != nil {
		return aws.ToString(g.LaunchTemplate.LaunchTemplateId)
	}
	if g.MixedInstancesPolicy != nil && g.MixedInstancesPolicy.LaunchTemplate != nil &&
		g.MixedInstancesPolicy.LaunchTemplate.LaunchTemplateSpecification != nil {
		return aws.ToString(g.MixedInstancesPolicy.LaunchTemplate.LaunchTemplateSpecification.LaunchTemplateId)
	}
	return ""
}

func asgTags(tags []asgtypes.TagDescription) core.Tags {
	pairs := make([][2]string, 0, len(tags))
	for _, t := range tags {
		pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
	}
	return tagsFromKV(pairs)
}
