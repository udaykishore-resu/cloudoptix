// This file discovers the EC2/VPC family — the single largest concentration
// of resource kinds any AWS account has (instances, volumes, snapshots,
// AMIs, elastic IPs, VPCs, subnets, security groups, NAT gateways, VPC
// endpoints, transit gateway attachments) — as one discoverer rather than
// eleven, because every one of those kinds shares the same client (*ec2.Client)
// and the same account-level IAM policy (ec2:Describe*): splitting them would
// buy no additional failure isolation, only eleven copies of the same
// pagination boilerplate.
package discovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ec2API is every EC2 call this discoverer makes. It is intentionally one
// interface covering all eleven kinds rather than eleven near-identical
// single-method interfaces, because every method here is satisfied by the
// same *ec2.Client and a test fake gains nothing from splitting them.
type ec2API interface {
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	DescribeSnapshots(ctx context.Context, in *ec2.DescribeSnapshotsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error)
	DescribeImages(ctx context.Context, in *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DescribeAddresses(ctx context.Context, in *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error)
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribeNatGateways(ctx context.Context, in *ec2.DescribeNatGatewaysInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
	DescribeVpcEndpoints(ctx context.Context, in *ec2.DescribeVpcEndpointsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error)
	DescribeTransitGatewayAttachments(ctx context.Context, in *ec2.DescribeTransitGatewayAttachmentsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeTransitGatewayAttachmentsOutput, error)
}

// EC2Discoverer implements ports.ResourceDiscoverer for the EC2/VPC family.
type EC2Discoverer struct {
	// newClient builds the narrow ec2API from a resolved aws.Config. A field
	// rather than a package-level function so tests can substitute a fake
	// without touching AWS at all.
	newClient func(aws.Config) ec2API
}

var _ ports.ResourceDiscoverer = (*EC2Discoverer)(nil)

// NewEC2Discoverer builds the EC2/VPC discoverer against the real SDK client.
func NewEC2Discoverer() *EC2Discoverer {
	return &EC2Discoverer{newClient: func(cfg aws.Config) ec2API { return ec2.NewFromConfig(cfg) }}
}

func (d *EC2Discoverer) Service() string { return "ec2" }

func (d *EC2Discoverer) Kinds() []cloud.Kind {
	return []cloud.Kind{
		cloud.KindEC2Instance, cloud.KindEBSVolume, cloud.KindEBSSnapshot, cloud.KindAMI,
		cloud.KindElasticIP, cloud.KindVPC, cloud.KindSubnet, cloud.KindSecurityGroup,
		cloud.KindNATGateway, cloud.KindVPCEndpoint, cloud.KindTransitGateway,
	}
}

func (d *EC2Discoverer) RequiredActions() []string {
	return []string{
		"ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:DescribeSnapshots",
		"ec2:DescribeImages", "ec2:DescribeAddresses", "ec2:DescribeVpcs",
		"ec2:DescribeSubnets", "ec2:DescribeSecurityGroups", "ec2:DescribeNatGateways",
		"ec2:DescribeVpcEndpoints", "ec2:DescribeTransitGatewayAttachments",
	}
}

func (d *EC2Discoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)

	steps := []struct {
		name string
		fn   func(context.Context, *builder, ec2API, ports.DiscoveryInput) error
	}{
		{"instances", discoverInstances},
		{"volumes", discoverVolumes},
		{"snapshots", discoverSnapshots},
		{"images", discoverImages},
		{"addresses", discoverAddresses},
		{"vpcs", discoverVPCs},
		{"subnets", discoverSubnets},
		{"security groups", discoverSecurityGroups},
		{"nat gateways", discoverNATGateways},
		{"vpc endpoints", discoverVPCEndpoints},
		{"transit gateway attachments", discoverTGWAttachments},
	}
	for _, s := range steps {
		if err := s.fn(ctx, b, client, in); err != nil {
			if skipUnavailable(err) {
				b.warnf("ec2: %s not available in region %s: %v", s.name, in.Region, err)
				continue
			}
			return b.out, err
		}
	}

	linkEC2Relationships(b)
	return b.out, nil
}

// --- instances ---------------------------------------------------------

func discoverInstances(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeInstancesPaginator(client, &ec2.DescribeInstancesInput{MaxResults: aws.Int32(1000)})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeInstances", "ec2:DescribeInstances")
		}
		for _, res := range page.Reservations {
			for _, i := range res.Instances {
				addInstance(b, in, i)
			}
		}
	}
	return nil
}

func addInstance(b *builder, in ports.DiscoveryInput, i ec2types.Instance) {
	tags := ec2Tags(i.Tags)
	az := ""
	if i.Placement != nil {
		az = aws.ToString(i.Placement.AvailabilityZone)
	}
	state := cloud.StateUnknown
	if i.State != nil {
		state = mapState(string(i.State.Name))
	}
	var vcpu float64
	if i.CpuOptions != nil {
		vcpu = float64(aws.ToInt32(i.CpuOptions.CoreCount)) * float64(aws.ToInt32(i.CpuOptions.ThreadsPerCore))
	}
	sgIDs := make([]string, 0, len(i.SecurityGroups))
	for _, sg := range i.SecurityGroups {
		sgIDs = append(sgIDs, aws.ToString(sg.GroupId))
	}
	a := attrs(
		"architecture", string(i.Architecture),
		"platform", string(i.Platform),
		"vpc_id", aws.ToString(i.VpcId),
		"subnet_id", aws.ToString(i.SubnetId),
		"security_group_ids", strings.Join(sgIDs, ","),
		"ebs_optimized", boolStr(aws.ToBool(i.EbsOptimized)),
		"private_ip", aws.ToString(i.PrivateIpAddress),
		"public_ip", aws.ToString(i.PublicIpAddress),
	)
	name := tags.First("Name")
	nativeID := aws.ToString(i.InstanceId)
	b.add(resourceSpec{
		Kind: cloud.KindEC2Instance, NativeID: nativeID,
		ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:instance/%s", in.Region, in.AccountID, nativeID)),
		Name: name, Region: in.Region, AZ: az, State: state,
		InstanceType: string(i.InstanceType), Capacity: cloud.Capacity{VCPU: vcpu, InstanceCount: 1},
		Purchase: purchaseModel(i.InstanceLifecycle), Tags: tags, Attributes: a,
		CreatedAt: aws.ToTime(i.LaunchTime), DiscoveredBy: "aws.ec2",
	})
	// The instance -> volume attached_to edge is wired from the volume side
	// (addVolume, from Volume.Attachments — the authoritative source) rather
	// than from BlockDeviceMappings here, so it is emitted exactly once
	// regardless of whether instances or volumes are discovered first.
}

func purchaseModel(lifecycle ec2types.InstanceLifecycleType) cloud.PurchaseModel {
	switch lifecycle {
	case ec2types.InstanceLifecycleTypeSpot:
		return cloud.PurchaseSpot
	case ec2types.InstanceLifecycleTypeScheduled:
		return cloud.PurchaseReserved
	default:
		return cloud.PurchaseOnDemand
	}
}

// --- volumes -------------------------------------------------------------

func discoverVolumes(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeVolumesPaginator(client, &ec2.DescribeVolumesInput{MaxResults: aws.Int32(500)})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeVolumes", "ec2:DescribeVolumes")
		}
		for _, v := range page.Volumes {
			addVolume(b, in, v)
		}
	}
	return nil
}

func addVolume(b *builder, in ports.DiscoveryInput, v ec2types.Volume) {
	tags := ec2Tags(v.Tags)
	nativeID := aws.ToString(v.VolumeId)
	state := cloud.StateAvailable
	var attachedTo string
	if len(v.Attachments) > 0 {
		state = cloud.StateInUse
		attachedTo = aws.ToString(v.Attachments[0].InstanceId)
	}
	b.add(resourceSpec{
		Kind: cloud.KindEBSVolume, NativeID: nativeID,
		ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:volume/%s", in.Region, in.AccountID, nativeID)),
		Name: tags.First("Name"), Region: in.Region, AZ: aws.ToString(v.AvailabilityZone), State: state,
		InstanceType: string(v.VolumeType),
		Capacity: cloud.Capacity{
			StorageGiB: float64(aws.ToInt32(v.Size)), ProvisionedIOPS: int64(aws.ToInt32(v.Iops)),
			ThroughputMiBps: float64(aws.ToInt32(v.Throughput)),
		},
		Purchase: cloud.PurchaseOnDemand, Tags: tags,
		Attributes: attrs("encrypted", boolStr(aws.ToBool(v.Encrypted)), "snapshot_id", aws.ToString(v.SnapshotId)),
		CreatedAt:  aws.ToTime(v.CreateTime), DiscoveredBy: "aws.ec2",
	})
	if attachedTo != "" {
		b.edgeNative(cloud.RelAttachedTo, nativeID, attachedTo, 1)
	}
}

// --- snapshots -------------------------------------------------------------

func discoverSnapshots(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	// OwnerIds: self — an account's own DescribeSnapshots call otherwise
	// returns every public snapshot on the platform, which is not this
	// tenant's estate and would make the pass unbounded.
	p := ec2.NewDescribeSnapshotsPaginator(client, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"}, MaxResults: aws.Int32(1000),
	})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeSnapshots", "ec2:DescribeSnapshots")
		}
		for _, s := range page.Snapshots {
			addSnapshot(b, in, s)
		}
	}
	return nil
}

func addSnapshot(b *builder, in ports.DiscoveryInput, s ec2types.Snapshot) {
	tags := ec2Tags(s.Tags)
	nativeID := aws.ToString(s.SnapshotId)
	b.add(resourceSpec{
		Kind: cloud.KindEBSSnapshot, NativeID: nativeID,
		ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:snapshot/%s", in.Region, in.AccountID, nativeID)),
		Name: tags.First("Name"), Region: in.Region, State: mapState(string(s.State)),
		Capacity: cloud.Capacity{StorageGiB: float64(aws.ToInt32(s.VolumeSize))},
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("volume_id", aws.ToString(s.VolumeId), "encrypted", boolStr(aws.ToBool(s.Encrypted)),
			"storage_tier", string(s.StorageTier)),
		CreatedAt: aws.ToTime(s.StartTime), DiscoveredBy: "aws.ec2",
	})
}

// --- AMIs --------------------------------------------------------------

func discoverImages(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeImagesPaginator(client, &ec2.DescribeImagesInput{Owners: []string{"self"}})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeImages", "ec2:DescribeImages")
		}
		for _, img := range page.Images {
			addImage(b, in, img)
		}
	}
	return nil
}

func addImage(b *builder, in ports.DiscoveryInput, img ec2types.Image) {
	tags := ec2Tags(img.Tags)
	nativeID := aws.ToString(img.ImageId)
	var sizeGiB float64
	for _, bdm := range img.BlockDeviceMappings {
		if bdm.Ebs != nil {
			sizeGiB += float64(aws.ToInt32(bdm.Ebs.VolumeSize))
		}
	}
	createdAt, _ := time.Parse(time.RFC3339, aws.ToString(img.CreationDate))
	b.add(resourceSpec{
		Kind: cloud.KindAMI, NativeID: nativeID,
		ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:image/%s", in.Region, in.AccountID, nativeID)),
		Name: aws.ToString(img.Name), Region: in.Region, State: mapState(string(img.State)),
		Capacity: cloud.Capacity{StorageGiB: sizeGiB}, Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("architecture", string(img.Architecture), "public", boolStr(aws.ToBool(img.Public))),
		CreatedAt:  createdAt, DiscoveredBy: "aws.ec2",
	})
}

// --- elastic IPs -----------------------------------------------------------

func discoverAddresses(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	// DescribeAddresses has no paginator: an account's Elastic IP count is
	// capped in the low hundreds by AWS's own default quota, so a single call
	// always returns the complete set.
	b.countCall()
	out, err := client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
	if err != nil {
		return b.wrap(err, "ec2", "DescribeAddresses", "ec2:DescribeAddresses")
	}
	for _, a := range out.Addresses {
		addAddress(b, in, a)
	}
	return nil
}

func addAddress(b *builder, in ports.DiscoveryInput, a ec2types.Address) {
	tags := ec2Tags(a.Tags)
	nativeID := aws.ToString(a.AllocationId)
	if nativeID == "" {
		nativeID = aws.ToString(a.PublicIp) // EC2-Classic addresses have no allocation id
	}
	state := cloud.StateAvailable
	attachedTo := aws.ToString(a.InstanceId)
	if attachedTo != "" {
		state = cloud.StateInUse
	}
	b.add(resourceSpec{
		Kind: cloud.KindElasticIP, NativeID: nativeID,
		ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:elastic-ip/%s", in.Region, in.AccountID, nativeID)),
		Name: tags.First("Name"), Region: in.Region, State: state, Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes:   attrs("public_ip", aws.ToString(a.PublicIp), "domain", string(a.Domain)),
		DiscoveredBy: "aws.ec2",
	})
	if attachedTo != "" {
		b.edgeNative(cloud.RelAttachedTo, nativeID, attachedTo, 1)
	}
	// A NAT gateway holds its EIP via NatGatewayAddresses.AllocationId, not
	// InstanceId; that attachment edge is wired directly in
	// discoverNATGateways, which has both ids in scope without needing an
	// ENI cross-reference here.
}

// --- VPCs, subnets, security groups -----------------------------------

func discoverVPCs(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeVpcsPaginator(client, &ec2.DescribeVpcsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeVpcs", "ec2:DescribeVpcs")
		}
		for _, v := range page.Vpcs {
			tags := ec2Tags(v.Tags)
			nativeID := aws.ToString(v.VpcId)
			b.add(resourceSpec{
				Kind: cloud.KindVPC, NativeID: nativeID,
				ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:vpc/%s", in.Region, in.AccountID, nativeID)),
				Name: tags.First("Name"), Region: in.Region, State: mapState(string(v.State)),
				Purchase: cloud.PurchaseUnknown, Tags: tags,
				Attributes:   attrs("cidr", aws.ToString(v.CidrBlock), "is_default", boolStr(aws.ToBool(v.IsDefault))),
				DiscoveredBy: "aws.ec2",
			})
		}
	}
	return nil
}

func discoverSubnets(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeSubnetsPaginator(client, &ec2.DescribeSubnetsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeSubnets", "ec2:DescribeSubnets")
		}
		for _, s := range page.Subnets {
			tags := ec2Tags(s.Tags)
			nativeID := aws.ToString(s.SubnetId)
			b.add(resourceSpec{
				Kind: cloud.KindSubnet, NativeID: nativeID, ARN: core.ARN(aws.ToString(s.SubnetArn)),
				Name: tags.First("Name"), Region: in.Region, AZ: aws.ToString(s.AvailabilityZone),
				State: mapState(string(s.State)), Purchase: cloud.PurchaseUnknown, Tags: tags,
				Attributes: attrs("cidr", aws.ToString(s.CidrBlock), "vpc_id", aws.ToString(s.VpcId),
					"map_public_ip_on_launch", boolStr(aws.ToBool(s.MapPublicIpOnLaunch))),
				DiscoveredBy: "aws.ec2",
			})
			b.edgeNative(cloud.RelContains, aws.ToString(s.VpcId), nativeID, 1)
		}
	}
	return nil
}

func discoverSecurityGroups(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeSecurityGroupsPaginator(client, &ec2.DescribeSecurityGroupsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeSecurityGroups", "ec2:DescribeSecurityGroups")
		}
		for _, sg := range page.SecurityGroups {
			tags := ec2Tags(sg.Tags)
			nativeID := aws.ToString(sg.GroupId)
			arn := aws.ToString(sg.SecurityGroupArn)
			if arn == "" {
				arn = fmt.Sprintf("arn:aws:ec2:%s:%s:security-group/%s", in.Region, in.AccountID, nativeID)
			}
			b.add(resourceSpec{
				Kind: cloud.KindSecurityGroup, NativeID: nativeID, ARN: core.ARN(arn),
				Name: aws.ToString(sg.GroupName), Region: in.Region, State: cloud.StateAvailable,
				Purchase: cloud.PurchaseUnknown, Tags: tags,
				Attributes: attrs("vpc_id", aws.ToString(sg.VpcId)), DiscoveredBy: "aws.ec2",
			})
			b.edgeNative(cloud.RelContains, aws.ToString(sg.VpcId), nativeID, 1)
		}
	}
	return nil
}

// --- NAT gateways, VPC endpoints, transit gateway attachments ---------

func discoverNATGateways(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeNatGatewaysPaginator(client, &ec2.DescribeNatGatewaysInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeNatGateways", "ec2:DescribeNatGateways")
		}
		for _, n := range page.NatGateways {
			tags := ec2Tags(n.Tags)
			nativeID := aws.ToString(n.NatGatewayId)
			b.add(resourceSpec{
				Kind: cloud.KindNATGateway, NativeID: nativeID,
				ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:natgateway/%s", in.Region, in.AccountID, nativeID)),
				Name: tags.First("Name"), Region: in.Region, State: mapState(string(n.State)),
				Purchase: cloud.PurchaseUnknown, Tags: tags,
				Attributes: attrs("subnet_id", aws.ToString(n.SubnetId), "vpc_id", aws.ToString(n.VpcId),
					"connectivity_type", string(n.ConnectivityType)),
				CreatedAt: aws.ToTime(n.CreateTime), DiscoveredBy: "aws.ec2",
			})
			b.edgeNative(cloud.RelContains, aws.ToString(n.SubnetId), nativeID, 1)
			for _, addr := range n.NatGatewayAddresses {
				if allocID := aws.ToString(addr.AllocationId); allocID != "" {
					b.edgeNative(cloud.RelAttachedTo, allocID, nativeID, 1)
				}
			}
		}
	}
	return nil
}

func discoverVPCEndpoints(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeVpcEndpointsPaginator(client, &ec2.DescribeVpcEndpointsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeVpcEndpoints", "ec2:DescribeVpcEndpoints")
		}
		for _, ep := range page.VpcEndpoints {
			tags := ec2Tags(ep.Tags)
			nativeID := aws.ToString(ep.VpcEndpointId)
			b.add(resourceSpec{
				Kind: cloud.KindVPCEndpoint, NativeID: nativeID,
				ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:vpc-endpoint/%s", in.Region, in.AccountID, nativeID)),
				Name: tags.First("Name"), Region: in.Region, State: mapState(string(ep.State)),
				Purchase: cloud.PurchaseUnknown, Tags: tags,
				Attributes: attrs("vpc_id", aws.ToString(ep.VpcId), "service_name", aws.ToString(ep.ServiceName),
					"endpoint_type", string(ep.VpcEndpointType)),
				CreatedAt: aws.ToTime(ep.CreationTimestamp), DiscoveredBy: "aws.ec2",
			})
			b.edgeNative(cloud.RelContains, aws.ToString(ep.VpcId), nativeID, 1)
			for _, subnetID := range ep.SubnetIds {
				// A gateway-type endpoint (S3/DynamoDB) attaches to route
				// tables, not subnets, so SubnetIds is empty for those and
				// this loop is a no-op — exactly the interface-type case
				// egress_via is meant to describe.
				b.edgeNative(cloud.RelEgressVia, subnetID, nativeID, 1)
			}
		}
	}
	return nil
}

func discoverTGWAttachments(ctx context.Context, b *builder, client ec2API, in ports.DiscoveryInput) error {
	p := ec2.NewDescribeTransitGatewayAttachmentsPaginator(client, &ec2.DescribeTransitGatewayAttachmentsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			return b.wrap(err, "ec2", "DescribeTransitGatewayAttachments", "ec2:DescribeTransitGatewayAttachments")
		}
		for _, a := range page.TransitGatewayAttachments {
			tags := ec2Tags(a.Tags)
			nativeID := aws.ToString(a.TransitGatewayAttachmentId)
			b.add(resourceSpec{
				// Modelled under KindTransitGateway (the closed Kind
				// enumeration has no separate "attachment" kind, matching how
				// AWS itself bills the gateway rather than the attachment);
				// the attachment id is preserved as the native id and the
				// gateway id as an attribute so both are still queryable.
				Kind: cloud.KindTransitGateway, NativeID: nativeID,
				ARN:  core.ARN(fmt.Sprintf("arn:aws:ec2:%s:%s:transit-gateway-attachment/%s", in.Region, in.AccountID, nativeID)),
				Name: tags.First("Name"), Region: in.Region, State: mapState(string(a.State)),
				Purchase: cloud.PurchaseUnknown, Tags: tags,
				Attributes: attrs("transit_gateway_id", aws.ToString(a.TransitGatewayId),
					"resource_type", string(a.ResourceType), "resource_id", aws.ToString(a.ResourceId)),
				CreatedAt: aws.ToTime(a.CreationTime), DiscoveredBy: "aws.ec2",
			})
			if strings.EqualFold(string(a.ResourceType), "vpc") {
				b.edgeNative(cloud.RelContains, aws.ToString(a.ResourceId), nativeID, 1)
			}
		}
	}
	return nil
}

// linkEC2Relationships wires the edges that need more than one kind's ids in
// scope at once (instance -> subnet -> vpc), run once every kind in this
// pass has been added.
func linkEC2Relationships(b *builder) {
	// Instance -> subnet containment (and, transitively via the subnet ->
	// VPC edge discoverSubnets already emitted, instance -> VPC) is recorded
	// via the instance's own subnet_id attribute rather than re-reading the
	// API a second time.
	for i := range b.out.Resources {
		r := &b.out.Resources[i]
		if r.Kind != cloud.KindEC2Instance {
			continue
		}
		subnetID := r.Attr("subnet_id", "")
		if subnetID != "" {
			b.edgeNative(cloud.RelContains, subnetID, r.NativeID, 1)
		}
	}
}

// ec2Tags converts the EC2 tag slice shape into core.Tags.
func ec2Tags(tags []ec2types.Tag) core.Tags {
	pairs := make([][2]string, 0, len(tags))
	for _, t := range tags {
		pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
	}
	return tagsFromKV(pairs)
}
