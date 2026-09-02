package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeEC2 is a hand-written ec2API. Instances are paginated across two
// pages to exercise the paginator loop; every other call returns its full
// result in one page unless a test overrides it.
type fakeEC2 struct {
	instancePages [][]ec2types.Reservation
	instanceCall  int

	volumes     []ec2types.Volume
	snapshots   []ec2types.Snapshot
	images      []ec2types.Image
	addresses   []ec2types.Address
	vpcs        []ec2types.Vpc
	subnets     []ec2types.Subnet
	sgs         []ec2types.SecurityGroup
	natGateways []ec2types.NatGateway
	endpoints   []ec2types.VpcEndpoint
	tgwAttach   []ec2types.TransitGatewayAttachment

	instancesErr error
	volumesErr   error
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if f.instancesErr != nil {
		return nil, f.instancesErr
	}
	if f.instanceCall >= len(f.instancePages) {
		return &ec2.DescribeInstancesOutput{}, nil
	}
	page := f.instancePages[f.instanceCall]
	f.instanceCall++
	out := &ec2.DescribeInstancesOutput{Reservations: page}
	if f.instanceCall < len(f.instancePages) {
		out.NextToken = aws.String("more")
	}
	return out, nil
}

func (f *fakeEC2) DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if f.volumesErr != nil {
		return nil, f.volumesErr
	}
	return &ec2.DescribeVolumesOutput{Volumes: f.volumes}, nil
}
func (f *fakeEC2) DescribeSnapshots(context.Context, *ec2.DescribeSnapshotsInput, ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	return &ec2.DescribeSnapshotsOutput{Snapshots: f.snapshots}, nil
}
func (f *fakeEC2) DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	return &ec2.DescribeImagesOutput{Images: f.images}, nil
}
func (f *fakeEC2) DescribeAddresses(context.Context, *ec2.DescribeAddressesInput, ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	return &ec2.DescribeAddressesOutput{Addresses: f.addresses}, nil
}
func (f *fakeEC2) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: f.vpcs}, nil
}
func (f *fakeEC2) DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{Subnets: f.subnets}, nil
}
func (f *fakeEC2) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: f.sgs}, nil
}
func (f *fakeEC2) DescribeNatGateways(context.Context, *ec2.DescribeNatGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error) {
	return &ec2.DescribeNatGatewaysOutput{NatGateways: f.natGateways}, nil
}
func (f *fakeEC2) DescribeVpcEndpoints(context.Context, *ec2.DescribeVpcEndpointsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error) {
	return &ec2.DescribeVpcEndpointsOutput{VpcEndpoints: f.endpoints}, nil
}
func (f *fakeEC2) DescribeTransitGatewayAttachments(context.Context, *ec2.DescribeTransitGatewayAttachmentsInput, ...func(*ec2.Options)) (*ec2.DescribeTransitGatewayAttachmentsOutput, error) {
	return &ec2.DescribeTransitGatewayAttachmentsOutput{TransitGatewayAttachments: f.tgwAttach}, nil
}

func discovererWithFake(f *fakeEC2) *EC2Discoverer {
	return &EC2Discoverer{newClient: func(aws.Config) ec2API { return f }}
}

func testSession() ports.AWSSession { return fakeAWSSession{cfg: aws.Config{Region: "us-east-1"}} }

// fakeAWSSession implements ports.AWSSession by handing back a fixed
// aws.Config, standing in for a real sts.Session in every discovery test in
// this package.
type fakeAWSSession struct{ cfg aws.Config }

func (fakeAWSSession) AccountID() core.AccountID { return "222222222222" }
func (fakeAWSSession) Scope() cloud.RoleScope    { return cloud.ScopeRead }
func (fakeAWSSession) ExpiresAt() time.Time      { return time.Now().Add(time.Hour) }
func (s fakeAWSSession) Config(core.Region) any  { return s.cfg }

func discoveryInput() ports.DiscoveryInput {
	return ports.DiscoveryInput{
		TenantID: "tenant-1", AccountID: "222222222222", Region: "us-east-1", Session: testSession(),
	}
}

func TestEC2Discoverer_InstancesPaginateAndNormalize(t *testing.T) {
	f := &fakeEC2{
		instancePages: [][]ec2types.Reservation{
			{{Instances: []ec2types.Instance{{
				InstanceId: aws.String("i-aaa"), InstanceType: ec2types.InstanceTypeM5Large,
				State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
				Placement:  &ec2types.Placement{AvailabilityZone: aws.String("us-east-1a")},
				CpuOptions: &ec2types.CpuOptions{CoreCount: aws.Int32(2), ThreadsPerCore: aws.Int32(2)},
				VpcId:      aws.String("vpc-1"), SubnetId: aws.String("subnet-1"),
				SecurityGroups: []ec2types.GroupIdentifier{{GroupId: aws.String("sg-1")}},
				LaunchTime:     aws.Time(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
				Tags:           []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("web-1")}, {Key: aws.String("Environment"), Value: aws.String("production")}},
			}}}},
			{{Instances: []ec2types.Instance{{
				InstanceId: aws.String("i-bbb"), InstanceType: ec2types.InstanceTypeT3Micro,
				State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped},
			}}}},
		},
	}
	d := discovererWithFake(f)
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Equal(t, 2, f.instanceCall, "both pages must be consumed")

	instances := filterKind(out.Resources, cloud.KindEC2Instance)
	require.Len(t, instances, 2)

	web1 := mustFind(t, instances, "i-aaa")
	assert.Equal(t, "web-1", web1.Name)
	assert.Equal(t, cloud.StateRunning, web1.State)
	assert.Equal(t, "m5.large", web1.InstanceType)
	assert.Equal(t, float64(4), web1.Capacity.VCPU) // 2 cores * 2 threads
	assert.Equal(t, "us-east-1a", web1.AZ)
	assert.Equal(t, core.EnvProduction, web1.Environment)
	assert.Equal(t, "vpc-1", web1.Attr("vpc_id", ""))
	assert.Equal(t, "sg-1", web1.Attr("security_group_ids", ""))

	stopped := mustFind(t, instances, "i-bbb")
	assert.Equal(t, cloud.StateStopped, stopped.State)
}

func TestEC2Discoverer_VolumeAttachedToInstance(t *testing.T) {
	f := &fakeEC2{
		instancePages: [][]ec2types.Reservation{{{Instances: []ec2types.Instance{{
			InstanceId: aws.String("i-aaa"), InstanceType: ec2types.InstanceTypeM5Large,
			State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		}}}}},
		volumes: []ec2types.Volume{{
			VolumeId: aws.String("vol-1"), Size: aws.Int32(100), VolumeType: ec2types.VolumeTypeGp3,
			Attachments: []ec2types.VolumeAttachment{{InstanceId: aws.String("i-aaa")}},
		}},
	}
	d := discovererWithFake(f)
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)

	vols := filterKind(out.Resources, cloud.KindEBSVolume)
	require.Len(t, vols, 1)
	assert.Equal(t, cloud.StateInUse, vols[0].State)
	assert.Equal(t, float64(100), vols[0].Capacity.StorageGiB)

	found := false
	for _, rel := range out.Relationships {
		if rel.Kind == cloud.RelAttachedTo {
			found = true
		}
	}
	assert.True(t, found, "expected a volume attached_to instance edge")
}

func TestEC2Discoverer_NetworkContainmentAndEgress(t *testing.T) {
	f := &fakeEC2{
		vpcs:    []ec2types.Vpc{{VpcId: aws.String("vpc-1"), State: ec2types.VpcStateAvailable, CidrBlock: aws.String("10.0.0.0/16")}},
		subnets: []ec2types.Subnet{{SubnetId: aws.String("subnet-1"), VpcId: aws.String("vpc-1"), State: ec2types.SubnetStateAvailable}},
		natGateways: []ec2types.NatGateway{{
			NatGatewayId: aws.String("nat-1"), SubnetId: aws.String("subnet-1"), VpcId: aws.String("vpc-1"),
			State:               ec2types.NatGatewayStateAvailable,
			NatGatewayAddresses: []ec2types.NatGatewayAddress{{AllocationId: aws.String("eipalloc-1")}},
		}},
		addresses: []ec2types.Address{{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}},
		endpoints: []ec2types.VpcEndpoint{{
			VpcEndpointId: aws.String("vpce-1"), VpcId: aws.String("vpc-1"), State: ec2types.StateAvailable,
			SubnetIds: []string{"subnet-1"}, ServiceName: aws.String("com.amazonaws.us-east-1.s3"),
		}},
		instancePages: [][]ec2types.Reservation{{{Instances: []ec2types.Instance{{
			InstanceId: aws.String("i-aaa"), InstanceType: ec2types.InstanceTypeM5Large,
			State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}, SubnetId: aws.String("subnet-1"),
		}}}}},
	}
	d := discovererWithFake(f)
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)

	assertHasEdge(t, out.Relationships, cloud.RelContains, findID(out, "vpc-1"), findID(out, "subnet-1"))
	assertHasEdge(t, out.Relationships, cloud.RelContains, findID(out, "subnet-1"), findID(out, "i-aaa"))
	assertHasEdge(t, out.Relationships, cloud.RelAttachedTo, findID(out, "eipalloc-1"), findID(out, "nat-1"))
	assertHasEdge(t, out.Relationships, cloud.RelEgressVia, findID(out, "subnet-1"), findID(out, "vpce-1"))
}

func TestEC2Discoverer_ThrottleTranslatesToErrThrottled(t *testing.T) {
	f := &fakeEC2{instancesErr: &smithy.GenericAPIError{Code: "RequestLimitExceeded", Message: "slow down"}}
	d := discovererWithFake(f)
	_, err := d.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrThrottled)
}

func TestEC2Discoverer_AccessDeniedCarriesAction(t *testing.T) {
	f := &fakeEC2{instancesErr: &smithy.GenericAPIError{
		Code:    "UnauthorizedOperation",
		Message: "You are not authorized to perform: ec2:DescribeInstances",
	}}
	d := discovererWithFake(f)
	_, err := d.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
	var ce *core.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "ec2:DescribeInstances", ce.Details["action"])
}

func TestEC2Discoverer_RegionUnavailableIsWarningNotFailure(t *testing.T) {
	f := &fakeEC2{volumesErr: &smithy.GenericAPIError{
		Code: "UnknownOperationException", Message: "The requested operation is not supported in this region",
	}}
	d := discovererWithFake(f)
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	assert.NotEmpty(t, out.Warnings)
}

func TestEC2Discoverer_RequiredActionsAndKinds(t *testing.T) {
	d := NewEC2Discoverer()
	assert.Equal(t, "ec2", d.Service())
	assert.Contains(t, d.RequiredActions(), "ec2:DescribeInstances")
	assert.Contains(t, d.Kinds(), cloud.KindVPCEndpoint)
}

// --- shared test helpers used by every discoverer test file in this package ---

func filterKind(resources []cloud.Resource, kind cloud.Kind) []cloud.Resource {
	var out []cloud.Resource
	for _, r := range resources {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func mustFind(t *testing.T, resources []cloud.Resource, nativeID string) cloud.Resource {
	t.Helper()
	for _, r := range resources {
		if r.NativeID == nativeID {
			return r
		}
	}
	t.Fatalf("resource with native id %q not found", nativeID)
	return cloud.Resource{}
}

func findID(out ports.DiscoveryOutput, nativeID string) core.ID {
	for _, r := range out.Resources {
		if r.NativeID == nativeID {
			return r.ID
		}
	}
	return ""
}

func assertHasEdge(t *testing.T, rels []cloud.Relationship, kind cloud.RelationKind, from, to core.ID) {
	t.Helper()
	require.NotEmpty(t, from, "from id must resolve")
	require.NotEmpty(t, to, "to id must resolve")
	for _, r := range rels {
		if r.Kind == kind && r.FromID == from && r.ToID == to {
			return
		}
	}
	t.Fatalf("expected edge %s %s -> %s not found among %d relationships", kind, from, to, len(rels))
}
