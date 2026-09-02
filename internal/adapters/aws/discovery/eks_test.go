package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

type fakeEKS struct {
	clusterNames []string
	cluster      *ekstypes.Cluster
	ngNames      []string
	nodegroup    *ekstypes.Nodegroup
}

func (f *fakeEKS) ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	return &eks.ListClustersOutput{Clusters: f.clusterNames}, nil
}
func (f *fakeEKS) DescribeCluster(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	return &eks.DescribeClusterOutput{Cluster: f.cluster}, nil
}
func (f *fakeEKS) ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	return &eks.ListNodegroupsOutput{Nodegroups: f.ngNames}, nil
}
func (f *fakeEKS) DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	return &eks.DescribeNodegroupOutput{Nodegroup: f.nodegroup}, nil
}

func TestEKSDiscoverer_ClusterAndNodeGroupContainment(t *testing.T) {
	f := &fakeEKS{
		clusterNames: []string{"prod-cluster"},
		cluster: &ekstypes.Cluster{
			Name: aws.String("prod-cluster"), Status: ekstypes.ClusterStatusActive, Version: aws.String("1.29"),
			ResourcesVpcConfig: &ekstypes.VpcConfigResponse{EndpointPrivateAccess: true},
		},
		ngNames: []string{"default-ng"},
		nodegroup: &ekstypes.Nodegroup{
			NodegroupName: aws.String("default-ng"), Status: ekstypes.NodegroupStatusActive,
			InstanceTypes: []string{"m5.large"}, CapacityType: ekstypes.CapacityTypesOnDemand,
			ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(3), MinSize: aws.Int32(1), MaxSize: aws.Int32(6)},
		},
	}
	d := &EKSDiscoverer{newClient: func(aws.Config) eksAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)

	cluster := mustFind(t, out.Resources, "prod-cluster")
	ng := mustFind(t, out.Resources, "default-ng")
	assert.Equal(t, 3, ng.Capacity.InstanceCount)
	assert.Equal(t, "m5.large", ng.InstanceType)
	assertHasEdge(t, out.Relationships, cloud.RelContains, cluster.ID, ng.ID)
}

func TestEKSDiscoverer_RequiredActions(t *testing.T) {
	d := NewEKSDiscoverer()
	assert.Equal(t, "eks", d.Service())
	assert.Contains(t, d.RequiredActions(), "eks:DescribeNodegroup")
}
