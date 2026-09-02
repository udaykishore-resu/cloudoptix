package executor

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

type fakeEKS struct {
	ng       *ekstypes.Nodegroup
	notFound bool
	calls    map[string]int
}

func newFakeEKS() *fakeEKS { return &fakeEKS{calls: map[string]int{}} }

func (f *fakeEKS) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	f.calls["DescribeNodegroup"]++
	if f.notFound || f.ng == nil {
		return nil, notFoundErr("ResourceNotFoundException")
	}
	ng := *f.ng
	return &eks.DescribeNodegroupOutput{Nodegroup: &ng}, nil
}

func (f *fakeEKS) UpdateNodegroupConfig(_ context.Context, in *eks.UpdateNodegroupConfigInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
	f.calls["UpdateNodegroupConfig"]++
	if in.ScalingConfig != nil {
		f.ng.ScalingConfig = in.ScalingConfig
	}
	return &eks.UpdateNodegroupConfigOutput{}, nil
}

func eksExecutor(sp spec[eksAPI], f *fakeEKS) *genericExecutor[eksAPI] {
	return &genericExecutor[eksAPI]{spec: sp, newClient: func(any) eksAPI { return f }}
}

func testNodeGroupResource(nativeID, clusterName string) cloud.Resource {
	r := testResource(cloud.KindEKSNodeGroup, nativeID)
	r.Attributes = map[string]string{"cluster_id": clusterName}
	return r
}

func TestResizeNodeGroup_ScalesDownAndRollsBack(t *testing.T) {
	f := newFakeEKS()
	f.ng = &ekstypes.Nodegroup{
		ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-1"), Status: ekstypes.NodegroupStatusActive,
		ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(10), MinSize: aws.Int32(2), MaxSize: aws.Int32(20)},
	}
	ex := eksExecutor(resizeNodeGroupSpec, f)
	res := testNodeGroupResource("ng-1", "prod")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeNodeGroup, res, map[string]any{"desired_size": 4}))
	require.NoError(t, err)
	// identityParams must have baked cluster_name into the mutate step's params.
	assert.Equal(t, "prod", plan.Steps[2].Parameters["cluster_name"])

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, 4, out["desired_size"])
	assert.Equal(t, int32(4), aws.ToInt32(f.ng.ScalingConfig.DesiredSize))

	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	assert.Equal(t, int32(10), aws.ToInt32(f.ng.ScalingConfig.DesiredSize))
}

func TestResizeNodeGroup_RefusesWhenNotActive(t *testing.T) {
	f := newFakeEKS()
	f.ng = &ekstypes.Nodegroup{
		ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-1"), Status: ekstypes.NodegroupStatusUpdating,
		ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(10)},
	}
	ex := eksExecutor(resizeNodeGroupSpec, f)
	res := testNodeGroupResource("ng-1", "prod")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeNodeGroup, res, map[string]any{"desired_size": 4}))
	require.NoError(t, err)
	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.Error(t, err)
}

func TestResizeNodeGroup_PlanMissingReturnsNotFound(t *testing.T) {
	f := newFakeEKS()
	f.notFound = true
	ex := eksExecutor(resizeNodeGroupSpec, f)
	res := testNodeGroupResource("ng-missing", "prod")
	_, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeNodeGroup, res, map[string]any{"desired_size": 4}))
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNotFound)
}
