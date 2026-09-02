package executor

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// fakeEC2 is a hand-written ec2API whose Stop/Start/Modify calls actually
// mutate the stored instance/volume/etc., so a later DescribeInstances call
// (including the ones the SDK's own waiters issue inside mutate) observes
// the change — the same "fake mutates its own backing store" shape
// discovery's fakes use for read-only calls, extended here to writes.
type fakeEC2 struct {
	instance     *ec2types.Instance
	instanceTags []ec2types.Tag

	volume   *ec2types.Volume
	snap     *ec2types.Snapshot
	addr     *ec2types.Address
	nat      *ec2types.NatGateway
	rts      []ec2types.RouteTable
	vpces    []ec2types.VpcEndpoint
	notFound bool

	calls map[string]int
}

func newFakeEC2() *fakeEC2 { return &fakeEC2{calls: map[string]int{}} }

func (f *fakeEC2) count(op string) { f.calls[op]++ }

func notFoundErr(code string) error { return &smithy.GenericAPIError{Code: code, Message: "not found"} }

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.count("DescribeInstances")
	if f.notFound || f.instance == nil {
		return nil, notFoundErr("InvalidInstanceID.NotFound")
	}
	inst := *f.instance
	inst.Tags = f.instanceTags
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{inst}}}}, nil
}

func (f *fakeEC2) StopInstances(_ context.Context, in *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	f.count("StopInstances")
	f.instance.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}
	return &ec2.StopInstancesOutput{}, nil
}

func (f *fakeEC2) StartInstances(_ context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	f.count("StartInstances")
	f.instance.State = &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}
	return &ec2.StartInstancesOutput{}, nil
}

func (f *fakeEC2) ModifyInstanceAttribute(_ context.Context, in *ec2.ModifyInstanceAttributeInput, _ ...func(*ec2.Options)) (*ec2.ModifyInstanceAttributeOutput, error) {
	f.count("ModifyInstanceAttribute")
	f.instance.InstanceType = ec2types.InstanceType(aws.ToString(in.InstanceType.Value))
	return &ec2.ModifyInstanceAttributeOutput{}, nil
}

func (f *fakeEC2) CreateTags(_ context.Context, in *ec2.CreateTagsInput, _ ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	f.count("CreateTags")
	for _, t := range in.Tags {
		replaced := false
		for i, existing := range f.instanceTags {
			if aws.ToString(existing.Key) == aws.ToString(t.Key) {
				f.instanceTags[i] = t
				replaced = true
			}
		}
		if !replaced {
			f.instanceTags = append(f.instanceTags, t)
		}
	}
	return &ec2.CreateTagsOutput{}, nil
}

func (f *fakeEC2) DeleteTags(_ context.Context, in *ec2.DeleteTagsInput, _ ...func(*ec2.Options)) (*ec2.DeleteTagsOutput, error) {
	f.count("DeleteTags")
	for _, t := range in.Tags {
		var kept []ec2types.Tag
		for _, existing := range f.instanceTags {
			if aws.ToString(existing.Key) != aws.ToString(t.Key) {
				kept = append(kept, existing)
			}
		}
		f.instanceTags = kept
	}
	return &ec2.DeleteTagsOutput{}, nil
}

func (f *fakeEC2) DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	f.count("DescribeVolumes")
	if f.notFound || f.volume == nil {
		return nil, notFoundErr("InvalidVolume.NotFound")
	}
	return &ec2.DescribeVolumesOutput{Volumes: []ec2types.Volume{*f.volume}}, nil
}

func (f *fakeEC2) ModifyVolume(_ context.Context, in *ec2.ModifyVolumeInput, _ ...func(*ec2.Options)) (*ec2.ModifyVolumeOutput, error) {
	f.count("ModifyVolume")
	if in.VolumeType != "" {
		f.volume.VolumeType = in.VolumeType
	}
	if in.Iops != nil {
		f.volume.Iops = in.Iops
	}
	if in.Throughput != nil {
		f.volume.Throughput = in.Throughput
	}
	if in.Size != nil {
		f.volume.Size = in.Size
	}
	return &ec2.ModifyVolumeOutput{}, nil
}

func (f *fakeEC2) DeleteVolume(context.Context, *ec2.DeleteVolumeInput, ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	f.count("DeleteVolume")
	f.volume = nil
	return &ec2.DeleteVolumeOutput{}, nil
}

func (f *fakeEC2) DescribeSnapshots(context.Context, *ec2.DescribeSnapshotsInput, ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	f.count("DescribeSnapshots")
	if f.notFound || f.snap == nil {
		return nil, notFoundErr("InvalidSnapshot.NotFound")
	}
	return &ec2.DescribeSnapshotsOutput{Snapshots: []ec2types.Snapshot{*f.snap}}, nil
}

func (f *fakeEC2) DeleteSnapshot(context.Context, *ec2.DeleteSnapshotInput, ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error) {
	f.count("DeleteSnapshot")
	f.snap = nil
	return &ec2.DeleteSnapshotOutput{}, nil
}

func (f *fakeEC2) DescribeAddresses(context.Context, *ec2.DescribeAddressesInput, ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	f.count("DescribeAddresses")
	if f.notFound || f.addr == nil {
		return nil, notFoundErr("InvalidAllocationID.NotFound")
	}
	return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{*f.addr}}, nil
}

func (f *fakeEC2) ReleaseAddress(context.Context, *ec2.ReleaseAddressInput, ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error) {
	f.count("ReleaseAddress")
	f.addr = nil
	return &ec2.ReleaseAddressOutput{}, nil
}

func (f *fakeEC2) DescribeNatGateways(context.Context, *ec2.DescribeNatGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error) {
	f.count("DescribeNatGateways")
	if f.notFound || f.nat == nil {
		return &ec2.DescribeNatGatewaysOutput{}, nil
	}
	return &ec2.DescribeNatGatewaysOutput{NatGateways: []ec2types.NatGateway{*f.nat}}, nil
}

func (f *fakeEC2) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	f.count("DescribeRouteTables")
	return &ec2.DescribeRouteTablesOutput{RouteTables: f.rts}, nil
}

func (f *fakeEC2) DescribeVpcEndpoints(context.Context, *ec2.DescribeVpcEndpointsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error) {
	f.count("DescribeVpcEndpoints")
	return &ec2.DescribeVpcEndpointsOutput{VpcEndpoints: f.vpces}, nil
}

func (f *fakeEC2) CreateVpcEndpoint(_ context.Context, in *ec2.CreateVpcEndpointInput, _ ...func(*ec2.Options)) (*ec2.CreateVpcEndpointOutput, error) {
	f.count("CreateVpcEndpoint")
	ep := ec2types.VpcEndpoint{VpcEndpointId: aws.String("vpce-created"), VpcId: in.VpcId, ServiceName: in.ServiceName}
	if len(in.TagSpecifications) > 0 {
		ep.Tags = in.TagSpecifications[0].Tags
	}
	f.vpces = append(f.vpces, ep)
	return &ec2.CreateVpcEndpointOutput{VpcEndpoint: &ep}, nil
}

func ec2Executor(spec spec[ec2API], f *fakeEC2) *genericExecutor[ec2API] {
	return &genericExecutor[ec2API]{spec: spec, newClient: func(any) ec2API { return f }}
}

// ---- resize_instance --------------------------------------------------

func TestResizeInstance_StopsModifiesAndRestartsARunningInstance(t *testing.T) {
	f := newFakeEC2()
	f.instance = &ec2types.Instance{InstanceId: aws.String("i-1"), InstanceType: "m5.xlarge", State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}}
	ex := ec2Executor(resizeInstanceSpec, f)

	res := testResource(cloud.KindEC2Instance, "i-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeInstance, res, map[string]any{"instance_type": "m5.large"}))
	require.NoError(t, err)
	assert.True(t, plan.Rollback.Feasible)

	mutateStep := plan.Steps[2]
	out, err := ex.Apply(context.Background(), testSession(), plan, mutateStep)
	require.NoError(t, err)
	assert.Equal(t, "m5.large", out["instance_type"])
	assert.Equal(t, "m5.large", string(f.instance.InstanceType))
	assert.Equal(t, ec2types.InstanceStateNameRunning, f.instance.State.Name, "an instance that was running must be restarted after resizing")
	assert.Equal(t, 1, f.calls["StopInstances"])
	assert.Equal(t, 1, f.calls["StartInstances"])
	assert.Equal(t, 1, f.calls["ModifyInstanceAttribute"])

	// A retried mutate against the now-resized instance is idempotent: no
	// further Stop/Modify/Start calls.
	out, err = ex.Apply(context.Background(), testSession(), plan, mutateStep)
	require.NoError(t, err)
	assert.Equal(t, true, out["idempotent"])
	assert.Equal(t, 1, f.calls["StopInstances"])

	// Rollback restores the original type, again stopping/starting since it
	// was running.
	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	assert.Equal(t, "m5.xlarge", string(f.instance.InstanceType))
}

func TestResizeInstance_LeavesAStoppedInstanceStopped(t *testing.T) {
	f := newFakeEC2()
	f.instance = &ec2types.Instance{InstanceId: aws.String("i-1"), InstanceType: "m5.xlarge", State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}}
	ex := ec2Executor(resizeInstanceSpec, f)
	res := testResource(cloud.KindEC2Instance, "i-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeInstance, res, map[string]any{"instance_type": "m5.large"}))
	require.NoError(t, err)

	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, "m5.large", string(f.instance.InstanceType))
	assert.Equal(t, ec2types.InstanceStateNameStopped, f.instance.State.Name)
	assert.Equal(t, 0, f.calls["StopInstances"], "an already-stopped instance must not be stopped again")
	assert.Equal(t, 0, f.calls["StartInstances"], "an instance that was stopped before the resize must not be started as a side effect")
}

func TestResizeInstance_PlanMissingReturnsNotFound(t *testing.T) {
	f := newFakeEC2()
	f.notFound = true
	ex := ec2Executor(resizeInstanceSpec, f)
	res := testResource(cloud.KindEC2Instance, "i-missing")
	_, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeInstance, res, map[string]any{"instance_type": "m5.large"}))
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNotFound)
}

// ---- stop_instance ------------------------------------------------------

func TestStopInstance_ApplyThenRollbackRestartsIt(t *testing.T) {
	f := newFakeEC2()
	f.instance = &ec2types.Instance{InstanceId: aws.String("i-1"), InstanceType: "m5.large", State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}}
	ex := ec2Executor(stopInstanceSpec, f)
	res := testResource(cloud.KindEC2Instance, "i-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionStopInstance, res, nil))
	require.NoError(t, err)

	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, ec2types.InstanceStateNameStopped, f.instance.State.Name)

	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	assert.Equal(t, ec2types.InstanceStateNameRunning, f.instance.State.Name)
}

func TestStopInstance_PreflightFailsOnceInstanceIsGone(t *testing.T) {
	f := newFakeEC2()
	f.instance = &ec2types.Instance{InstanceId: aws.String("i-1"), InstanceType: "m5.large", State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}}
	ex := ec2Executor(stopInstanceSpec, f)
	res := testResource(cloud.KindEC2Instance, "i-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionStopInstance, res, nil))
	require.NoError(t, err)

	f.notFound = true
	err = ex.Preflight(context.Background(), testSession(), plan)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNotFound)
}

// ---- schedule_shutdown ----------------------------------------------------

func TestScheduleShutdown_TagsAndStopsThenRollbackUntags(t *testing.T) {
	f := newFakeEC2()
	f.instance = &ec2types.Instance{InstanceId: aws.String("i-1"), InstanceType: "m5.large", State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}}
	ex := ec2Executor(scheduleShutdownSpec, f)
	res := testResource(cloud.KindEC2Instance, "i-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionScheduleShutdown, res, nil))
	require.NoError(t, err)

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, defaultShutdownSchedule, out["schedule_tag"])
	assert.Equal(t, ec2types.InstanceStateNameStopped, f.instance.State.Name)

	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	assert.Empty(t, f.instanceTags)
	assert.Equal(t, ec2types.InstanceStateNameRunning, f.instance.State.Name)
}

// ---- delete_volume --------------------------------------------------------

func TestDeleteVolume_RefusesWhenAttached(t *testing.T) {
	f := newFakeEC2()
	f.volume = &ec2types.Volume{VolumeId: aws.String("vol-1"), VolumeType: ec2types.VolumeTypeGp3, Size: aws.Int32(100),
		Attachments: []ec2types.VolumeAttachment{{State: ec2types.VolumeAttachmentStateAttached}}}
	ex := ec2Executor(deleteVolumeSpec, f)
	res := testResource(cloud.KindEBSVolume, "vol-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionDeleteVolume, res, nil))
	require.NoError(t, err)
	assert.False(t, plan.Rollback.Feasible)

	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.Error(t, err)
}

func TestDeleteVolume_DeletesAnUnattachedVolumeIdempotently(t *testing.T) {
	f := newFakeEC2()
	f.volume = &ec2types.Volume{VolumeId: aws.String("vol-1"), VolumeType: ec2types.VolumeTypeGp3, Size: aws.Int32(100)}
	ex := ec2Executor(deleteVolumeSpec, f)
	res := testResource(cloud.KindEBSVolume, "vol-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionDeleteVolume, res, nil))
	require.NoError(t, err)

	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Nil(t, f.volume)

	// Retried against an already-gone volume is idempotent, not an error.
	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, true, out["already_deleted"])
}

// ---- resize_volume ------------------------------------------------------

func TestResizeVolume_RejectsShrink(t *testing.T) {
	f := newFakeEC2()
	f.volume = &ec2types.Volume{VolumeId: aws.String("vol-1"), VolumeType: ec2types.VolumeTypeGp3, Size: aws.Int32(100)}
	ex := ec2Executor(resizeVolumeSpec, f)
	res := testResource(cloud.KindEBSVolume, "vol-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeVolume, res, map[string]any{"size_gib": 50}))
	require.NoError(t, err)
	assert.False(t, plan.Rollback.Feasible, "growth-only changes AWS cannot reverse must be rollback-infeasible")

	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.Error(t, err)
}

func TestResizeVolume_Grows(t *testing.T) {
	f := newFakeEC2()
	f.volume = &ec2types.Volume{VolumeId: aws.String("vol-1"), VolumeType: ec2types.VolumeTypeGp3, Size: aws.Int32(100)}
	ex := ec2Executor(resizeVolumeSpec, f)
	res := testResource(cloud.KindEBSVolume, "vol-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeVolume, res, map[string]any{"size_gib": 200}))
	require.NoError(t, err)

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, 200, out["size_gib"])
	assert.Equal(t, int32(200), aws.ToInt32(f.volume.Size))
}

// ---- release_elastic_ip ----------------------------------------------

func TestReleaseElasticIP_RefusesWhenAssociated(t *testing.T) {
	f := newFakeEC2()
	f.addr = &ec2types.Address{AllocationId: aws.String("eipalloc-1"), AssociationId: aws.String("eipassoc-1")}
	ex := ec2Executor(releaseElasticIPSpec, f)
	res := testResource(cloud.KindElasticIP, "eipalloc-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionReleaseElasticIP, res, nil))
	require.NoError(t, err)
	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.Error(t, err)
}

// ---- create_vpc_endpoint --------------------------------------------------

func TestCreateVPCEndpoint_CreatesAGatewayEndpointAndIsIdempotent(t *testing.T) {
	f := newFakeEC2()
	f.nat = &ec2types.NatGateway{NatGatewayId: aws.String("nat-1"), VpcId: aws.String("vpc-1"), State: ec2types.NatGatewayStateAvailable}
	f.rts = []ec2types.RouteTable{{RouteTableId: aws.String("rtb-1")}, {RouteTableId: aws.String("rtb-2")}}
	ex := ec2Executor(createVPCEndpointSpec, f)
	res := testResource(cloud.KindNATGateway, "nat-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionCreateVPCEndpoint, res, nil))
	require.NoError(t, err)

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, "vpce-created", out["endpoint_id"])
	assert.Equal(t, 1, f.calls["CreateVpcEndpoint"])

	// A retried mutate finds the endpoint (via its source-nat tag) and does
	// not create a second one.
	out, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, true, out["idempotent"])
	assert.Equal(t, 1, f.calls["CreateVpcEndpoint"])
}

func TestRegistry_NewExecutorsCoversSixteenActions(t *testing.T) {
	execs := NewExecutors()
	require.Len(t, execs, 16)
	seen := map[optimize.ActionType]bool{}
	for _, e := range execs {
		assert.NotEmpty(t, e.RequiredActions())
		seen[e.Action()] = true
	}
	assert.Len(t, seen, 16, "every registered executor must handle a distinct action")

	_, ok := NewExecutor(optimize.ActionResizeInstance)
	assert.True(t, ok)
	_, ok = NewExecutor(optimize.ActionSetLogRetention)
	assert.False(t, ok, "set_log_retention is deliberately not in the working registry")
}
