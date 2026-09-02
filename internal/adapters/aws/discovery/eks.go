// This file discovers EKS clusters and their managed node groups. Like ECS,
// EKS's List calls return names only, so each name costs one Describe call;
// unlike ECS there is no batched describe, so this is a true N+1 — accepted
// because an account's EKS cluster and node group counts are small (low
// tens, not thousands) compared to, say, EC2 instances.
package discovery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type eksAPI interface {
	ListClusters(ctx context.Context, in *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	DescribeCluster(ctx context.Context, in *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	ListNodegroups(ctx context.Context, in *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

type EKSDiscoverer struct {
	newClient func(aws.Config) eksAPI
}

var _ ports.ResourceDiscoverer = (*EKSDiscoverer)(nil)

func NewEKSDiscoverer() *EKSDiscoverer {
	return &EKSDiscoverer{newClient: func(cfg aws.Config) eksAPI { return eks.NewFromConfig(cfg) }}
}

func (d *EKSDiscoverer) Service() string { return "eks" }
func (d *EKSDiscoverer) Kinds() []cloud.Kind {
	return []cloud.Kind{cloud.KindEKSCluster, cloud.KindEKSNodeGroup}
}
func (d *EKSDiscoverer) RequiredActions() []string {
	return []string{"eks:ListClusters", "eks:DescribeCluster", "eks:ListNodegroups", "eks:DescribeNodegroup"}
}

func (d *EKSDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := eks.NewListClustersPaginator(client, &eks.ListClustersInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("eks: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "eks", "ListClusters", "eks:ListClusters")
		}
		for _, name := range page.Clusters {
			d.discoverCluster(ctx, b, client, in, name)
		}
	}
	return b.out, nil
}

func (d *EKSDiscoverer) discoverCluster(ctx context.Context, b *builder, client eksAPI, in ports.DiscoveryInput, name string) {
	b.countCall()
	desc, err := client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
	if err != nil {
		b.warnf("eks: could not describe cluster %s: %v", name, err)
		return
	}
	c := desc.Cluster
	if c == nil {
		return
	}
	b.add(resourceSpec{
		Kind: cloud.KindEKSCluster, NativeID: name, ARN: core.ARN(aws.ToString(c.Arn)),
		Name: name, Region: in.Region, State: mapState(string(c.Status)), EngineVer: aws.ToString(c.Version),
		Purchase: cloud.PurchaseUnknown, Tags: core.Tags(c.Tags),
		Attributes: attrs("platform_version", aws.ToString(c.PlatformVersion), "endpoint_private_access",
			boolStr(c.ResourcesVpcConfig != nil && c.ResourcesVpcConfig.EndpointPrivateAccess)),
		CreatedAt: aws.ToTime(c.CreatedAt), DiscoveredBy: "aws.eks",
	})

	var ngNames []string
	ngp := eks.NewListNodegroupsPaginator(client, &eks.ListNodegroupsInput{ClusterName: aws.String(name)})
	for ngp.HasMorePages() {
		b.countCall()
		page, err := ngp.NextPage(ctx)
		if err != nil {
			b.warnf("eks: could not list node groups for cluster %s: %v", name, err)
			return
		}
		ngNames = append(ngNames, page.Nodegroups...)
	}
	for _, ngName := range ngNames {
		b.countCall()
		ngDesc, err := client.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(name), NodegroupName: aws.String(ngName)})
		if err != nil || ngDesc.Nodegroup == nil {
			b.warnf("eks: could not describe node group %s/%s: %v", name, ngName, err)
			continue
		}
		addNodegroup(b, in, name, *ngDesc.Nodegroup)
	}
}

func addNodegroup(b *builder, in ports.DiscoveryInput, clusterName string, ng ekstypes.Nodegroup) {
	nativeID := aws.ToString(ng.NodegroupName)
	instanceType := ""
	if len(ng.InstanceTypes) > 0 {
		instanceType = ng.InstanceTypes[0]
	}
	var desired, min, max int
	if ng.ScalingConfig != nil {
		desired = int(aws.ToInt32(ng.ScalingConfig.DesiredSize))
		min = int(aws.ToInt32(ng.ScalingConfig.MinSize))
		max = int(aws.ToInt32(ng.ScalingConfig.MaxSize))
	}
	b.add(resourceSpec{
		Kind: cloud.KindEKSNodeGroup, NativeID: nativeID, ARN: core.ARN(aws.ToString(ng.NodegroupArn)),
		Name: nativeID, Region: in.Region, State: mapState(string(ng.Status)), InstanceType: instanceType,
		Capacity: cloud.Capacity{InstanceCount: desired, DesiredCount: desired, MinCount: min, MaxCount: max},
		Purchase: capacityTypePurchase(ng.CapacityType), Tags: core.Tags(ng.Tags),
		Attributes: attrs("cluster_id", clusterName, "ami_type", string(ng.AmiType), "disk_size_gib", istr(int64(aws.ToInt32(ng.DiskSize)))),
		CreatedAt:  aws.ToTime(ng.CreatedAt), DiscoveredBy: "aws.eks",
	})
	b.edgeNative(cloud.RelContains, clusterName, nativeID, 1)
}

func capacityTypePurchase(ct ekstypes.CapacityTypes) cloud.PurchaseModel {
	if ct == ekstypes.CapacityTypesSpot {
		return cloud.PurchaseSpot
	}
	return cloud.PurchaseOnDemand
}
