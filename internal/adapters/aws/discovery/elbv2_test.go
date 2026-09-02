package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

type fakeELBv2 struct {
	lbs    []elbv2types.LoadBalancer
	tgs    []elbv2types.TargetGroup
	health map[string][]elbv2types.TargetHealthDescription
}

func (f *fakeELBv2) DescribeLoadBalancers(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
	return &elbv2.DescribeLoadBalancersOutput{LoadBalancers: f.lbs}, nil
}
func (f *fakeELBv2) DescribeTargetGroups(context.Context, *elbv2.DescribeTargetGroupsInput, ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
	return &elbv2.DescribeTargetGroupsOutput{TargetGroups: f.tgs}, nil
}
func (f *fakeELBv2) DescribeTargetHealth(_ context.Context, in *elbv2.DescribeTargetHealthInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error) {
	return &elbv2.DescribeTargetHealthOutput{TargetHealthDescriptions: f.health[aws.ToString(in.TargetGroupArn)]}, nil
}
func (f *fakeELBv2) DescribeTags(context.Context, *elbv2.DescribeTagsInput, ...func(*elbv2.Options)) (*elbv2.DescribeTagsOutput, error) {
	return &elbv2.DescribeTagsOutput{}, nil
}

func TestELBv2Discoverer_RoutesToChainFromLBThroughTargetGroupToInstance(t *testing.T) {
	lbArn := "arn:aws:elasticloadbalancing:us-east-1:222222222222:loadbalancer/app/web/abc123"
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:222222222222:targetgroup/web-tg/def456"

	f := &fakeELBv2{
		lbs: []elbv2types.LoadBalancer{{
			LoadBalancerArn: aws.String(lbArn), LoadBalancerName: aws.String("web"),
			Type: elbv2types.LoadBalancerTypeEnumApplication, Scheme: elbv2types.LoadBalancerSchemeEnumInternetFacing,
			State: &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
			VpcId: aws.String("vpc-1"),
		}},
		tgs: []elbv2types.TargetGroup{{
			TargetGroupArn: aws.String(tgArn), TargetGroupName: aws.String("web-tg"),
			TargetType: elbv2types.TargetTypeEnumInstance, Protocol: elbv2types.ProtocolEnumHttp,
			Port: aws.Int32(80), LoadBalancerArns: []string{lbArn},
		}},
		health: map[string][]elbv2types.TargetHealthDescription{
			tgArn: {
				{Target: &elbv2types.TargetDescription{Id: aws.String("i-target1")}},
				{Target: &elbv2types.TargetDescription{Id: aws.String("i-target2")}},
			},
		},
	}
	d := &ELBv2Discoverer{newClient: func(aws.Config) elbv2API { return f }}

	in := discoveryInput()
	in.Existing = cloud.NewInventory([]cloud.Resource{
		{ID: "res-inst-1", NativeID: "i-target1", Kind: cloud.KindEC2Instance},
		{ID: "res-inst-2", NativeID: "i-target2", Kind: cloud.KindEC2Instance},
	})

	out, err := d.Discover(context.Background(), in)
	require.NoError(t, err)

	lb := mustFind(t, out.Resources, lbArn)
	assert.Equal(t, cloud.KindALB, lb.Kind)
	assert.Equal(t, "vpc-1", lb.Attr("vpc_id", ""))

	tg := mustFind(t, out.Resources, tgArn)
	assert.Equal(t, cloud.KindTargetGroup, tg.Kind)

	assertHasEdge(t, out.Relationships, cloud.RelRoutesTo, lb.ID, tg.ID)
	assertHasEdge(t, out.Relationships, cloud.RelRoutesTo, tg.ID, core.ID("res-inst-1"))
	assertHasEdge(t, out.Relationships, cloud.RelRoutesTo, tg.ID, core.ID("res-inst-2"))

	for _, rel := range out.Relationships {
		if rel.Kind == cloud.RelRoutesTo && rel.FromID == tg.ID {
			assert.InDelta(t, 0.5, rel.Weight, 0.001)
		}
	}
}

func TestELBv2Discoverer_NetworkLoadBalancerKind(t *testing.T) {
	nlbArn := "arn:aws:elasticloadbalancing:us-east-1:222222222222:loadbalancer/net/nlb/xyz"
	f := &fakeELBv2{
		lbs: []elbv2types.LoadBalancer{{
			LoadBalancerArn:  aws.String(nlbArn),
			LoadBalancerName: aws.String("nlb"), Type: elbv2types.LoadBalancerTypeEnumNetwork,
			State: &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
		}},
	}
	d := &ELBv2Discoverer{newClient: func(aws.Config) elbv2API { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	nlb := mustFind(t, out.Resources, nlbArn)
	assert.Equal(t, cloud.KindNLB, nlb.Kind)
}

func TestELBv2Discoverer_RequiredActions(t *testing.T) {
	d := NewELBv2Discoverer()
	assert.Equal(t, "elasticloadbalancing", d.Service())
	assert.Contains(t, d.RequiredActions(), "elasticloadbalancing:DescribeTargetHealth")
}
