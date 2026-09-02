// This file implements every ports.Executor whose target lives in EC2:
// resize_instance, stop_instance, schedule_shutdown, delete_volume,
// modify_volume_type, resize_volume, delete_snapshot, release_elastic_ip and
// create_vpc_endpoint. All nine share one client (*ec2.Client) and one
// generic engine (genericExecutor[ec2API], see common.go), the same reason
// discovery/ec2.go covers eleven resource kinds with a single ec2API rather
// than eleven near-identical interfaces.
//
// Two AWS realities the simulator never had to deal with show up here.
// First, changing a running instance's type requires the instance to be
// stopped first — resize_instance's mutate function really does stop, wait,
// modify and (if it was running before) start again, blocking on the SDK's
// own InstanceStoppedWaiter/InstanceRunningWaiter rather than folding the
// whole thing into one attribute write the way awssim's equivalent spec
// does. Second, EBS volumes and RDS storage can be grown but AWS will never
// let them be shrunk back, so resize_volume is marked rollback-infeasible
// here even though awssim's in-memory equivalent (which can freely shrink
// its simulated volume back down) marks it feasible — see this package's
// own doc comment for the full rationale.
package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// ec2API is every EC2 call this file's nine executors make.
type ec2API interface {
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	StopInstances(ctx context.Context, in *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	StartInstances(ctx context.Context, in *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	ModifyInstanceAttribute(ctx context.Context, in *ec2.ModifyInstanceAttributeInput, optFns ...func(*ec2.Options)) (*ec2.ModifyInstanceAttributeOutput, error)
	CreateTags(ctx context.Context, in *ec2.CreateTagsInput, optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error)
	DeleteTags(ctx context.Context, in *ec2.DeleteTagsInput, optFns ...func(*ec2.Options)) (*ec2.DeleteTagsOutput, error)
	DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	ModifyVolume(ctx context.Context, in *ec2.ModifyVolumeInput, optFns ...func(*ec2.Options)) (*ec2.ModifyVolumeOutput, error)
	DeleteVolume(ctx context.Context, in *ec2.DeleteVolumeInput, optFns ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error)
	DescribeSnapshots(ctx context.Context, in *ec2.DescribeSnapshotsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error)
	DeleteSnapshot(ctx context.Context, in *ec2.DeleteSnapshotInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error)
	DescribeAddresses(ctx context.Context, in *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error)
	ReleaseAddress(ctx context.Context, in *ec2.ReleaseAddressInput, optFns ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error)
	DescribeNatGateways(ctx context.Context, in *ec2.DescribeNatGatewaysInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
	DescribeRouteTables(ctx context.Context, in *ec2.DescribeRouteTablesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	DescribeVpcEndpoints(ctx context.Context, in *ec2.DescribeVpcEndpointsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error)
	CreateVpcEndpoint(ctx context.Context, in *ec2.CreateVpcEndpointInput, optFns ...func(*ec2.Options)) (*ec2.CreateVpcEndpointOutput, error)
}

func newEC2Client(cfg any) ec2API { return ec2.NewFromConfig(cfg.(aws.Config)) }

// ec2NotFoundCodes lists the ErrorCode() values EC2 uses for "no such
// resource", which every captureState in this file treats as ok=false
// (gone) rather than propagating as a hard error — the same contract
// discovery's mapState/skipUnavailable helpers establish for read paths.
var ec2NotFoundCodes = map[string]bool{
	"InvalidInstanceID.NotFound":    true,
	"InvalidVolume.NotFound":        true,
	"InvalidSnapshot.NotFound":      true,
	"InvalidAllocationID.NotFound":  true,
	"InvalidAddress.NotFound":       true,
	"InvalidVpcEndpointId.NotFound": true,
	"NatGatewayNotFound":            true,
}

func isEC2NotFound(err error) bool {
	apiErr, ok := awserr.APIErrorOf(err)
	return ok && ec2NotFoundCodes[apiErr.ErrorCode()]
}

const waitTimeout = 5 * time.Minute

// ---- resize_instance --------------------------------------------------

func describeInstance(ctx context.Context, c ec2API, instanceID string) (ec2types.Instance, bool, error) {
	out, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		if isEC2NotFound(err) {
			return ec2types.Instance{}, false, nil
		}
		return ec2types.Instance{}, false, awserr.Translate(err, "ec2", "DescribeInstances", "ec2:DescribeInstances")
	}
	for _, res := range out.Reservations {
		for _, inst := range res.Instances {
			if inst.State != nil && inst.State.Name == ec2types.InstanceStateNameTerminated {
				continue
			}
			return inst, true, nil
		}
	}
	return ec2types.Instance{}, false, nil
}

func captureInstance(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
	inst, ok, err := describeInstance(ctx, c, nativeID)
	if err != nil || !ok {
		return nil, ok, err
	}
	state := ""
	if inst.State != nil {
		state = string(inst.State.Name)
	}
	schedule := ""
	for _, t := range inst.Tags {
		if aws.ToString(t.Key) == "cloudoptix:schedule" {
			schedule = aws.ToString(t.Value)
		}
	}
	return map[string]any{
		"instance_type": string(inst.InstanceType),
		"state":         state,
		"schedule_tag":  schedule,
	}, true, nil
}

// ec2SetInstanceType is the shared stop -> modify -> start sequence used by
// both resize_instance's mutate and its restore: a real EC2 instance type
// change requires the instance to be stopped, unlike awssim's Estate which
// can rewrite InstanceType on a running resource with no such constraint.
// The instance is left in whatever run state it started in — a stopped
// instance is resized and left stopped, never woken up as a side effect.
func ec2SetInstanceType(ctx context.Context, c ec2API, instanceID, targetType string) (map[string]any, error) {
	inst, ok, err := describeInstance(ctx, c, instanceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, core.NotFound(string(cloud.KindEC2Instance), instanceID)
	}
	wasRunning := inst.State != nil && inst.State.Name == ec2types.InstanceStateNameRunning

	if wasRunning {
		if _, err := c.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{instanceID}}); err != nil {
			return nil, awserr.Translate(err, "ec2", "StopInstances", "ec2:StopInstances")
		}
		wctx, cancel := ctxWithTimeout(ctx, waitTimeout)
		err := ec2.NewInstanceStoppedWaiter(c).Wait(wctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}}, waitTimeout)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("aws executor: waiting for %s to stop before resizing: %w", instanceID, err)
		}
	}

	_, err = c.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
		InstanceId:   aws.String(instanceID),
		InstanceType: &ec2types.AttributeValue{Value: aws.String(targetType)},
	})
	if err != nil {
		return nil, awserr.Translate(err, "ec2", "ModifyInstanceAttribute", "ec2:ModifyInstanceAttribute")
	}

	if wasRunning {
		if _, err := c.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{instanceID}}); err != nil {
			return nil, awserr.Translate(err, "ec2", "StartInstances", "ec2:StartInstances")
		}
		wctx, cancel := ctxWithTimeout(ctx, waitTimeout)
		err := ec2.NewInstanceRunningWaiter(c).Wait(wctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}}, waitTimeout)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("aws executor: waiting for %s to come back up after resizing: %w", instanceID, err)
		}
	}
	return map[string]any{"instance_type": targetType, "restarted": wasRunning}, nil
}

var resizeInstanceSpec = spec[ec2API]{
	action: optimize.ActionResizeInstance, kind: cloud.KindEC2Instance,
	awsAction: "ec2:ModifyInstanceAttribute", titleFmt: "resize %s to the recommended instance type",
	requiredActions: []string{
		"ec2:DescribeInstances", "ec2:StopInstances", "ec2:StartInstances", "ec2:ModifyInstanceAttribute",
	},
	rollbackFeasible: true, dataLossRisk: core.RiskLow,
	captureState: captureInstance,
	isApplied: func(current, params map[string]any) bool {
		want, ok := paramStr(params, "instance_type")
		return ok && want == current["instance_type"]
	},
	mutate: func(ctx context.Context, c ec2API, nativeID string, params map[string]any, _ core.Region) (map[string]any, error) {
		target, ok := paramStr(params, "instance_type")
		if !ok || target == "" {
			return nil, core.Invalid("resize_instance: missing instance_type parameter")
		}
		return ec2SetInstanceType(ctx, c, nativeID, target)
	},
	restore: func(ctx context.Context, c ec2API, nativeID string, before map[string]any, _ core.Region) error {
		want, _ := before["instance_type"].(string)
		if want == "" {
			return core.Invalid("resize_instance: rollback snapshot has no instance_type")
		}
		_, err := ec2SetInstanceType(ctx, c, nativeID, want)
		return err
	},
}

// ---- stop_instance ------------------------------------------------------

var stopInstanceSpec = spec[ec2API]{
	action: optimize.ActionStopInstance, kind: cloud.KindEC2Instance,
	awsAction: "ec2:StopInstances", titleFmt: "stop idle instance %s",
	requiredActions:  []string{"ec2:DescribeInstances", "ec2:StopInstances", "ec2:StartInstances"},
	rollbackFeasible: true, dataLossRisk: core.RiskNone,
	captureState: captureInstance,
	isApplied: func(current, _ map[string]any) bool {
		return current["state"] == string(ec2types.InstanceStateNameStopped)
	},
	mutate: func(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, error) {
		if _, err := c.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{nativeID}}); err != nil {
			return nil, awserr.Translate(err, "ec2", "StopInstances", "ec2:StopInstances")
		}
		wctx, cancel := ctxWithTimeout(ctx, waitTimeout)
		defer cancel()
		if err := ec2.NewInstanceStoppedWaiter(c).Wait(wctx, &ec2.DescribeInstancesInput{InstanceIds: []string{nativeID}}, waitTimeout); err != nil {
			return nil, fmt.Errorf("aws executor: waiting for %s to stop: %w", nativeID, err)
		}
		return map[string]any{"state": string(ec2types.InstanceStateNameStopped)}, nil
	},
	restore: func(ctx context.Context, c ec2API, nativeID string, before map[string]any, _ core.Region) error {
		if before["state"] != string(ec2types.InstanceStateNameRunning) {
			return nil // was already stopped (or otherwise not running) before the change: nothing to restore
		}
		if _, err := c.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{nativeID}}); err != nil {
			return awserr.Translate(err, "ec2", "StartInstances", "ec2:StartInstances")
		}
		wctx, cancel := ctxWithTimeout(ctx, waitTimeout)
		defer cancel()
		if err := ec2.NewInstanceRunningWaiter(c).Wait(wctx, &ec2.DescribeInstancesInput{InstanceIds: []string{nativeID}}, waitTimeout); err != nil {
			return fmt.Errorf("aws executor: waiting for %s to restart: %w", nativeID, err)
		}
		return nil
	},
}

// ---- schedule_shutdown ----------------------------------------------------

// defaultShutdownSchedule matches the human-readable convention
// awssim.scheduleShutdownSpec established, so the same text appears on the
// approval screen whether a tenant is running against the simulator or a
// real account.
const defaultShutdownSchedule = "stop 19:00-07:00 Mon-Fri, all day Sat-Sun"
const scheduleTagKey = "cloudoptix:schedule"

var scheduleShutdownSpec = spec[ec2API]{
	action: optimize.ActionScheduleShutdown, kind: cloud.KindEC2Instance,
	awsAction: "ec2:CreateTags,ec2:StopInstances", titleFmt: "schedule off-hours shutdown for %s",
	requiredActions:  []string{"ec2:DescribeInstances", "ec2:CreateTags", "ec2:DeleteTags", "ec2:StopInstances", "ec2:StartInstances"},
	rollbackFeasible: true, dataLossRisk: core.RiskLow,
	captureState: captureInstance,
	isApplied: func(current, params map[string]any) bool {
		want, _ := paramStr(params, "schedule")
		if want == "" {
			want = defaultShutdownSchedule
		}
		return current["schedule_tag"] == want && current["state"] == string(ec2types.InstanceStateNameStopped)
	},
	mutate: func(ctx context.Context, c ec2API, nativeID string, params map[string]any, _ core.Region) (map[string]any, error) {
		schedule, _ := paramStr(params, "schedule")
		if schedule == "" {
			schedule = defaultShutdownSchedule
		}
		_, err := c.CreateTags(ctx, &ec2.CreateTagsInput{
			Resources: []string{nativeID},
			Tags:      []ec2types.Tag{{Key: aws.String(scheduleTagKey), Value: aws.String(schedule)}},
		})
		if err != nil {
			return nil, awserr.Translate(err, "ec2", "CreateTags", "ec2:CreateTags")
		}
		if _, err := c.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{nativeID}}); err != nil {
			return nil, awserr.Translate(err, "ec2", "StopInstances", "ec2:StopInstances")
		}
		wctx, cancel := ctxWithTimeout(ctx, waitTimeout)
		defer cancel()
		if err := ec2.NewInstanceStoppedWaiter(c).Wait(wctx, &ec2.DescribeInstancesInput{InstanceIds: []string{nativeID}}, waitTimeout); err != nil {
			return nil, fmt.Errorf("aws executor: waiting for %s to stop: %w", nativeID, err)
		}
		return map[string]any{"schedule_tag": schedule, "state": string(ec2types.InstanceStateNameStopped)}, nil
	},
	restore: func(ctx context.Context, c ec2API, nativeID string, before map[string]any, _ core.Region) error {
		prevTag, _ := before["schedule_tag"].(string)
		if prevTag == "" {
			if _, err := c.DeleteTags(ctx, &ec2.DeleteTagsInput{
				Resources: []string{nativeID}, Tags: []ec2types.Tag{{Key: aws.String(scheduleTagKey)}},
			}); err != nil {
				return awserr.Translate(err, "ec2", "DeleteTags", "ec2:DeleteTags")
			}
		} else {
			if _, err := c.CreateTags(ctx, &ec2.CreateTagsInput{
				Resources: []string{nativeID}, Tags: []ec2types.Tag{{Key: aws.String(scheduleTagKey), Value: aws.String(prevTag)}},
			}); err != nil {
				return awserr.Translate(err, "ec2", "CreateTags", "ec2:CreateTags")
			}
		}
		if before["state"] != string(ec2types.InstanceStateNameRunning) {
			return nil
		}
		if _, err := c.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{nativeID}}); err != nil {
			return awserr.Translate(err, "ec2", "StartInstances", "ec2:StartInstances")
		}
		wctx, cancel := ctxWithTimeout(ctx, waitTimeout)
		defer cancel()
		return ec2.NewInstanceRunningWaiter(c).Wait(wctx, &ec2.DescribeInstancesInput{InstanceIds: []string{nativeID}}, waitTimeout)
	},
}

// ---- delete_volume --------------------------------------------------------

func captureVolume(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
	out, err := c.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{nativeID}})
	if err != nil {
		if isEC2NotFound(err) {
			return nil, false, nil
		}
		return nil, false, awserr.Translate(err, "ec2", "DescribeVolumes", "ec2:DescribeVolumes")
	}
	if len(out.Volumes) == 0 {
		return nil, false, nil
	}
	v := out.Volumes[0]
	attached := false
	for _, a := range v.Attachments {
		if a.State != ec2types.VolumeAttachmentStateDetached {
			attached = true
		}
	}
	return map[string]any{
		"volume_type": string(v.VolumeType),
		"iops":        int(aws.ToInt32(v.Iops)),
		"throughput":  int(aws.ToInt32(v.Throughput)),
		"size_gib":    int(aws.ToInt32(v.Size)),
		"attached":    boolStr(attached),
	}, true, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

var deleteVolumeSpec = spec[ec2API]{
	action: optimize.ActionDeleteVolume, kind: cloud.KindEBSVolume,
	awsAction: "ec2:DeleteVolume", titleFmt: "delete unattached volume %s",
	requiredActions:  []string{"ec2:DescribeVolumes", "ec2:DeleteVolume"},
	rollbackFeasible: false, infeasibleReason: "a deleted volume's data cannot be recovered",
	dataLossRisk: core.RiskHigh, deleteAction: true,
	captureState: captureVolume,
	extraPrecondition: func(current map[string]any) error {
		if current["attached"] == "true" {
			return core.Invalid("delete_volume: volume is still attached")
		}
		return nil
	},
	isApplied: func(_, _ map[string]any) bool { return false },
	mutate: func(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, error) {
		if _, err := c.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(nativeID)}); err != nil {
			return nil, awserr.Translate(err, "ec2", "DeleteVolume", "ec2:DeleteVolume")
		}
		return map[string]any{"deleted": true}, nil
	},
}

// ---- modify_volume_type -----------------------------------------------

var modifyVolumeTypeSpec = spec[ec2API]{
	action: optimize.ActionModifyVolumeType, kind: cloud.KindEBSVolume,
	awsAction: "ec2:ModifyVolume", titleFmt: "change %s to the recommended volume type",
	requiredActions:  []string{"ec2:DescribeVolumes", "ec2:ModifyVolume"},
	rollbackFeasible: true, dataLossRisk: core.RiskNone,
	captureState: captureVolume,
	isApplied: func(current, params map[string]any) bool {
		if want, ok := paramStr(params, "volume_type"); ok && want != current["volume_type"] {
			return false
		}
		if want, ok := paramInt(params, "iops"); ok && want != current["iops"] {
			return false
		}
		if want, ok := paramInt(params, "throughput_mibps"); ok && want != current["throughput"] {
			return false
		}
		return true
	},
	mutate: ec2ModifyVolumeAttrs,
	restore: func(ctx context.Context, c ec2API, nativeID string, before map[string]any, region core.Region) error {
		restoreParams := map[string]any{"volume_type": before["volume_type"], "iops": before["iops"], "throughput_mibps": before["throughput"]}
		_, err := ec2ModifyVolumeAttrs(ctx, c, nativeID, restoreParams, region)
		return err
	},
}

func ec2ModifyVolumeAttrs(ctx context.Context, c ec2API, nativeID string, params map[string]any, _ core.Region) (map[string]any, error) {
	in := &ec2.ModifyVolumeInput{VolumeId: aws.String(nativeID)}
	out := map[string]any{}
	if t, ok := paramStr(params, "volume_type"); ok && t != "" {
		in.VolumeType = ec2types.VolumeType(t)
		out["volume_type"] = t
	}
	if iops, ok := paramInt(params, "iops"); ok && iops > 0 {
		in.Iops = aws.Int32(int32(iops))
		out["iops"] = iops
	}
	if tp, ok := paramInt(params, "throughput_mibps"); ok && tp > 0 {
		in.Throughput = aws.Int32(int32(tp))
		out["throughput"] = tp
	}
	if _, err := c.ModifyVolume(ctx, in); err != nil {
		return nil, awserr.Translate(err, "ec2", "ModifyVolume", "ec2:ModifyVolume")
	}
	return out, nil
}

// ---- resize_volume --------------------------------------------------------

var resizeVolumeSpec = spec[ec2API]{
	action: optimize.ActionResizeVolume, kind: cloud.KindEBSVolume,
	awsAction: "ec2:ModifyVolume", titleFmt: "grow %s to the recommended size",
	requiredActions: []string{"ec2:DescribeVolumes", "ec2:ModifyVolume"},
	// EBS volumes can be grown online but AWS provides no operation to
	// shrink one back down — unlike awssim's in-memory Estate, which can
	// freely rewrite SizeGiB in either direction, so this action is
	// rollback-infeasible for real, not merely by policy choice.
	rollbackFeasible: false, infeasibleReason: "EBS volumes cannot be shrunk back down after growing",
	dataLossRisk: core.RiskNone,
	captureState: captureVolume,
	isApplied: func(current, params map[string]any) bool {
		want, ok := paramInt(params, "size_gib")
		return ok && want == current["size_gib"]
	},
	mutate: func(ctx context.Context, c ec2API, nativeID string, params map[string]any, _ core.Region) (map[string]any, error) {
		target, ok := paramInt(params, "size_gib")
		if !ok || target <= 0 {
			return nil, core.Invalid("resize_volume: missing size_gib parameter")
		}
		current, exists, err := captureVolume(ctx, c, nativeID, nil, "")
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, core.NotFound(string(cloud.KindEBSVolume), nativeID)
		}
		if currentSize, _ := current["size_gib"].(int); target <= currentSize {
			return nil, core.Invalid("resize_volume: target size %dGiB is not larger than the current %dGiB — EBS volumes cannot be shrunk", target, currentSize)
		}
		if _, err := c.ModifyVolume(ctx, &ec2.ModifyVolumeInput{VolumeId: aws.String(nativeID), Size: aws.Int32(int32(target))}); err != nil {
			return nil, awserr.Translate(err, "ec2", "ModifyVolume", "ec2:ModifyVolume")
		}
		return map[string]any{"size_gib": target}, nil
	},
}

// ---- delete_snapshot --------------------------------------------------

var deleteSnapshotSpec = spec[ec2API]{
	action: optimize.ActionDeleteSnapshot, kind: cloud.KindEBSSnapshot,
	awsAction: "ec2:DeleteSnapshot", titleFmt: "delete stale snapshot %s",
	requiredActions:  []string{"ec2:DescribeSnapshots", "ec2:DeleteSnapshot"},
	rollbackFeasible: false, infeasibleReason: "a deleted snapshot cannot be recovered",
	dataLossRisk: core.RiskHigh, deleteAction: true,
	captureState: func(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
		out, err := c.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{nativeID}})
		if err != nil {
			if isEC2NotFound(err) {
				return nil, false, nil
			}
			return nil, false, awserr.Translate(err, "ec2", "DescribeSnapshots", "ec2:DescribeSnapshots")
		}
		if len(out.Snapshots) == 0 {
			return nil, false, nil
		}
		s := out.Snapshots[0]
		return map[string]any{"volume_id": aws.ToString(s.VolumeId), "size_gib": int(aws.ToInt32(s.VolumeSize))}, true, nil
	},
	isApplied: func(_, _ map[string]any) bool { return false },
	mutate: func(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, error) {
		if _, err := c.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: aws.String(nativeID)}); err != nil {
			return nil, awserr.Translate(err, "ec2", "DeleteSnapshot", "ec2:DeleteSnapshot")
		}
		return map[string]any{"deleted": true}, nil
	},
}

// ---- release_elastic_ip ----------------------------------------------

var releaseElasticIPSpec = spec[ec2API]{
	action: optimize.ActionReleaseElasticIP, kind: cloud.KindElasticIP,
	awsAction: "ec2:ReleaseAddress", titleFmt: "release unassociated Elastic IP %s",
	requiredActions:  []string{"ec2:DescribeAddresses", "ec2:ReleaseAddress"},
	rollbackFeasible: false, infeasibleReason: "a released address cannot be reclaimed; a new allocation gets a different IP",
	dataLossRisk: core.RiskMedium, deleteAction: true,
	captureState: func(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
		out, err := c.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{AllocationIds: []string{nativeID}})
		if err != nil {
			if isEC2NotFound(err) {
				return nil, false, nil
			}
			return nil, false, awserr.Translate(err, "ec2", "DescribeAddresses", "ec2:DescribeAddresses")
		}
		if len(out.Addresses) == 0 {
			return nil, false, nil
		}
		a := out.Addresses[0]
		return map[string]any{"public_ip": aws.ToString(a.PublicIp), "associated": boolStr(a.AssociationId != nil)}, true, nil
	},
	extraPrecondition: func(current map[string]any) error {
		if current["associated"] == "true" {
			return core.Invalid("release_elastic_ip: address is still associated with a resource")
		}
		return nil
	},
	isApplied: func(_, _ map[string]any) bool { return false },
	mutate: func(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, error) {
		if _, err := c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: aws.String(nativeID)}); err != nil {
			return nil, awserr.Translate(err, "ec2", "ReleaseAddress", "ec2:ReleaseAddress")
		}
		return map[string]any{"released": true}, nil
	},
}

// ---- create_vpc_endpoint --------------------------------------------------

// vpceSourceTagKey marks the S3 gateway endpoint this action creates with
// the NAT gateway id it was created to offload, which is how captureState
// detects an endpoint this action already created — a real VPC endpoint's
// ID is assigned by AWS, not chosen by the caller, so (unlike awssim's
// deterministic "vpce-"+natID convention over its own in-memory ids) a tag
// is the only idempotency key available.
const vpceSourceTagKey = "cloudoptix:source-nat"

func captureNATForEndpoint(ctx context.Context, c ec2API, nativeID string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
	out, err := c.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{Filter: []ec2types.Filter{{Name: aws.String("nat-gateway-id"), Values: []string{nativeID}}}})
	if err != nil {
		if isEC2NotFound(err) {
			return nil, false, nil
		}
		return nil, false, awserr.Translate(err, "ec2", "DescribeNatGateways", "ec2:DescribeNatGateways")
	}
	if len(out.NatGateways) == 0 {
		return nil, false, nil
	}
	nat := out.NatGateways[0]
	vpcID := aws.ToString(nat.VpcId)

	epOut, err := c.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{Filters: []ec2types.Filter{
		{Name: aws.String("vpc-id"), Values: []string{vpcID}},
		{Name: aws.String("tag:" + vpceSourceTagKey), Values: []string{nativeID}},
	}})
	if err != nil {
		return nil, false, awserr.Translate(err, "ec2", "DescribeVpcEndpoints", "ec2:DescribeVpcEndpoints")
	}
	endpointID := ""
	if len(epOut.VpcEndpoints) > 0 {
		endpointID = aws.ToString(epOut.VpcEndpoints[0].VpcEndpointId)
	}
	return map[string]any{"vpc_id": vpcID, "endpoint_id": endpointID}, true, nil
}

var createVPCEndpointSpec = spec[ec2API]{
	action: optimize.ActionCreateVPCEndpoint, kind: cloud.KindNATGateway,
	awsAction: "ec2:CreateVpcEndpoint", titleFmt: "add an S3 gateway endpoint to offload NAT gateway %s",
	requiredActions:  []string{"ec2:DescribeNatGateways", "ec2:DescribeRouteTables", "ec2:DescribeVpcEndpoints", "ec2:CreateVpcEndpoint"},
	rollbackFeasible: true, dataLossRisk: core.RiskNone,
	captureState: captureNATForEndpoint,
	isApplied: func(current, _ map[string]any) bool {
		return current["endpoint_id"] != ""
	},
	mutate: func(ctx context.Context, c ec2API, nativeID string, _ map[string]any, region core.Region) (map[string]any, error) {
		current, exists, err := captureNATForEndpoint(ctx, c, nativeID, nil, region)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, core.NotFound(string(cloud.KindNATGateway), nativeID)
		}
		vpcID, _ := current["vpc_id"].(string)

		rtOut, err := c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}}})
		if err != nil {
			return nil, awserr.Translate(err, "ec2", "DescribeRouteTables", "ec2:DescribeRouteTables")
		}
		routeTableIDs := make([]string, 0, len(rtOut.RouteTables))
		for _, rt := range rtOut.RouteTables {
			routeTableIDs = append(routeTableIDs, aws.ToString(rt.RouteTableId))
		}

		serviceName := fmt.Sprintf("com.amazonaws.%s.s3", region)
		out, err := c.CreateVpcEndpoint(ctx, &ec2.CreateVpcEndpointInput{
			VpcId: aws.String(vpcID), ServiceName: aws.String(serviceName),
			VpcEndpointType: ec2types.VpcEndpointTypeGateway, RouteTableIds: routeTableIDs,
			TagSpecifications: []ec2types.TagSpecification{{
				ResourceType: ec2types.ResourceTypeVpcEndpoint,
				Tags:         []ec2types.Tag{{Key: aws.String(vpceSourceTagKey), Value: aws.String(nativeID)}},
			}},
		})
		if err != nil {
			return nil, awserr.Translate(err, "ec2", "CreateVpcEndpoint", "ec2:CreateVpcEndpoint")
		}
		endpointID := ""
		if out.VpcEndpoint != nil {
			endpointID = aws.ToString(out.VpcEndpoint.VpcEndpointId)
		}
		return map[string]any{"endpoint_id": endpointID}, nil
	},
	// Rollback of "created an endpoint" is itself not a delete — an S3
	// gateway endpoint left in place costs nothing and breaks nothing, so
	// restore is a deliberate no-op rather than a DeleteVpcEndpoints call:
	// undoing a free, additive safety net is not worth the operational
	// risk of touching every route table it was just wired into again.
	restore: func(ctx context.Context, c ec2API, nativeID string, before map[string]any, region core.Region) error {
		return nil
	},
}

// newExecutors' constructors for these nine specs live in registry.go.
