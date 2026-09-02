// This file implements resize_node_group, the one EKS executor. Its
// captureState needs more than the node group's own NativeID: both
// DescribeNodegroup and UpdateNodegroupConfig require the owning cluster's
// name too, which discovery/eks.go stores as the "cluster_id" resource
// attribute rather than folding into the node group's NativeID (see that
// file's own addNodegroup). The spec's identityParams hook (see common.go)
// is what carries that attribute from the planned cloud.Resource into every
// later captureState/mutate call as params["cluster_name"], since Preflight
// and Apply only ever see the plan's own Target string and Parameters, not
// the original cloud.Resource.
package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

type eksAPI interface {
	DescribeNodegroup(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	UpdateNodegroupConfig(ctx context.Context, in *eks.UpdateNodegroupConfigInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error)
}

func newEKSClient(cfg any) eksAPI { return eks.NewFromConfig(cfg.(aws.Config)) }

func isEKSNotFound(err error) bool {
	apiErr, ok := awserr.APIErrorOf(err)
	return ok && apiErr.ErrorCode() == "ResourceNotFoundException"
}

const eksWaitTimeout = 15 * time.Minute

func captureNodegroup(ctx context.Context, c eksAPI, nativeID string, params map[string]any, _ core.Region) (map[string]any, bool, error) {
	clusterName, _ := paramStr(params, "cluster_name")
	if clusterName == "" {
		return nil, false, core.Invalid("resize_node_group: missing cluster_name identity parameter for node group %s", nativeID)
	}
	out, err := c.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(nativeID)})
	if err != nil {
		if isEKSNotFound(err) {
			return nil, false, nil
		}
		return nil, false, awserr.Translate(err, "eks", "DescribeNodegroup", "eks:DescribeNodegroup")
	}
	if out.Nodegroup == nil {
		return nil, false, nil
	}
	ng := out.Nodegroup
	desired, min, max := 0, 0, 0
	if ng.ScalingConfig != nil {
		desired = int(aws.ToInt32(ng.ScalingConfig.DesiredSize))
		min = int(aws.ToInt32(ng.ScalingConfig.MinSize))
		max = int(aws.ToInt32(ng.ScalingConfig.MaxSize))
	}
	return map[string]any{
		"cluster_name": clusterName, // copied forward so restore, which receives only this map, still knows it
		"status":       string(ng.Status),
		"desired_size": desired, "min_size": min, "max_size": max,
	}, true, nil
}

var resizeNodeGroupSpec = spec[eksAPI]{
	action: optimize.ActionResizeNodeGroup, kind: cloud.KindEKSNodeGroup,
	awsAction: "eks:UpdateNodegroupConfig", titleFmt: "resize node group %s to the recommended capacity",
	requiredActions: []string{"eks:DescribeNodegroup", "eks:UpdateNodegroupConfig"},
	// Shrinking a node group evicts and reschedules every pod that was
	// running on the nodes it removes, which is a real (if usually
	// tolerated) disruption a resize of an EC2 instance's type does not
	// carry — hence RiskMedium here against RiskLow for resize_instance.
	rollbackFeasible: true, dataLossRisk: core.RiskMedium,
	identityParams: func(r cloud.Resource) map[string]any {
		return map[string]any{"cluster_name": r.Attr("cluster_id", "")}
	},
	captureState: captureNodegroup,
	extraPrecondition: func(current map[string]any) error {
		if current["status"] != string(ekstypes.NodegroupStatusActive) {
			return core.Invalid("resize_node_group: node group is in status %q, not modifiable", current["status"])
		}
		return nil
	},
	isApplied: func(current, params map[string]any) bool {
		want, ok := paramInt(params, "desired_size")
		return ok && want == current["desired_size"]
	},
	mutate: func(ctx context.Context, c eksAPI, nativeID string, params map[string]any, _ core.Region) (map[string]any, error) {
		clusterName, _ := paramStr(params, "cluster_name")
		target, ok := paramInt(params, "desired_size")
		if !ok {
			return nil, core.Invalid("resize_node_group: missing desired_size parameter")
		}
		minSize, _ := paramInt(params, "min_size")
		maxSize, _ := paramInt(params, "max_size")
		scaling := &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(int32(target))}
		if minSize > 0 {
			scaling.MinSize = aws.Int32(int32(minSize))
		}
		if maxSize > 0 {
			scaling.MaxSize = aws.Int32(int32(maxSize))
		}
		_, err := c.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
			ClusterName: aws.String(clusterName), NodegroupName: aws.String(nativeID), ScalingConfig: scaling,
		})
		if err != nil {
			return nil, awserr.Translate(err, "eks", "UpdateNodegroupConfig", "eks:UpdateNodegroupConfig")
		}
		if err := eksWaitActive(ctx, c, clusterName, nativeID); err != nil {
			return nil, err
		}
		return map[string]any{"desired_size": target}, nil
	},
	restore: func(ctx context.Context, c eksAPI, nativeID string, before map[string]any, _ core.Region) error {
		clusterName, _ := before["cluster_name"].(string)
		desired, _ := before["desired_size"].(int)
		if clusterName == "" {
			return core.Invalid("resize_node_group: rollback snapshot has no cluster_name")
		}
		scaling := &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(int32(desired))}
		if v, ok := before["min_size"].(int); ok && v > 0 {
			scaling.MinSize = aws.Int32(int32(v))
		}
		if v, ok := before["max_size"].(int); ok && v > 0 {
			scaling.MaxSize = aws.Int32(int32(v))
		}
		_, err := c.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
			ClusterName: aws.String(clusterName), NodegroupName: aws.String(nativeID), ScalingConfig: scaling,
		})
		if err != nil {
			return awserr.Translate(err, "eks", "UpdateNodegroupConfig", "eks:UpdateNodegroupConfig")
		}
		return eksWaitActive(ctx, c, clusterName, nativeID)
	},
}

func eksWaitActive(ctx context.Context, c eksAPI, clusterName, nodegroupName string) error {
	wctx, cancel := ctxWithTimeout(ctx, eksWaitTimeout)
	defer cancel()
	err := eks.NewNodegroupActiveWaiter(c).Wait(wctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(nodegroupName)}, eksWaitTimeout)
	if err != nil {
		return fmt.Errorf("aws executor: waiting for node group %s/%s to become active: %w", clusterName, nodegroupName, err)
	}
	return nil
}
