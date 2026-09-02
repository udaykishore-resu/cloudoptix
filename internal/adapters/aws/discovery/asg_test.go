package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type fakeASG struct {
	pages [][]asgtypes.AutoScalingGroup
	call  int
	err   error
}

func (f *fakeASG) DescribeAutoScalingGroups(context.Context, *autoscaling.DescribeAutoScalingGroupsInput, ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.call >= len(f.pages) {
		return &autoscaling.DescribeAutoScalingGroupsOutput{}, nil
	}
	page := f.pages[f.call]
	f.call++
	out := &autoscaling.DescribeAutoScalingGroupsOutput{AutoScalingGroups: page}
	if f.call < len(f.pages) {
		out.NextToken = aws.String("more")
	}
	return out, nil
}

func TestASGDiscoverer_PaginatesAndNormalizes(t *testing.T) {
	f := &fakeASG{pages: [][]asgtypes.AutoScalingGroup{
		{{AutoScalingGroupName: aws.String("web-asg"), DesiredCapacity: aws.Int32(3), MinSize: aws.Int32(1), MaxSize: aws.Int32(6),
			Instances: []asgtypes.Instance{
				{InstanceId: aws.String("i-1"), LifecycleState: asgtypes.LifecycleStateInService},
				{InstanceId: aws.String("i-2"), LifecycleState: asgtypes.LifecycleStateInService},
			}}},
		{{AutoScalingGroupName: aws.String("worker-asg"), DesiredCapacity: aws.Int32(2), MinSize: aws.Int32(0), MaxSize: aws.Int32(4)}},
	}}
	d := &ASGDiscoverer{newClient: func(aws.Config) asgAPI { return f }}

	in := discoveryInput()
	existingInstance := cloud.Resource{ID: "res-i-1", NativeID: "i-1", Kind: cloud.KindEC2Instance}
	in.Existing = cloud.NewInventory([]cloud.Resource{existingInstance})

	out, err := d.Discover(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, 2, f.call)
	require.Len(t, out.Resources, 2)

	web := mustFind(t, out.Resources, "web-asg")
	assert.Equal(t, 2, web.Capacity.InstanceCount)
	assert.Equal(t, 3, web.Capacity.DesiredCount)
	assert.Equal(t, 1, web.Capacity.MinCount)
	assert.Equal(t, 6, web.Capacity.MaxCount)

	// Only i-1 was resolvable via in.Existing, so exactly one contains edge.
	require.Len(t, out.Relationships, 1)
	assert.Equal(t, cloud.RelContains, out.Relationships[0].Kind)
	assert.Equal(t, existingInstance.ID, out.Relationships[0].ToID)
}

func TestASGDiscoverer_ThrottleAndDenied(t *testing.T) {
	d := &ASGDiscoverer{newClient: func(aws.Config) asgAPI {
		return &fakeASG{err: &smithy.GenericAPIError{Code: "RequestLimitExceeded", Message: "slow down"}}
	}}
	_, err := d.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrThrottled)

	d2 := &ASGDiscoverer{newClient: func(aws.Config) asgAPI {
		return &fakeASG{err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: autoscaling:DescribeAutoScalingGroups"}}
	}}
	_, err = d2.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
	var ce *core.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "autoscaling:DescribeAutoScalingGroups", ce.Details["action"])
}

func TestASGDiscoverer_RequiredActions(t *testing.T) {
	d := NewASGDiscoverer()
	assert.Equal(t, "autoscaling", d.Service())
	assert.Equal(t, []cloud.Kind{cloud.KindAutoScalingGroup}, d.Kinds())
	assert.Contains(t, d.RequiredActions(), "autoscaling:DescribeAutoScalingGroups")
}

var _ ports.ResourceDiscoverer = (*ASGDiscoverer)(nil)
