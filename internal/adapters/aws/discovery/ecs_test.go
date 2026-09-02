package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

type fakeECS struct {
	clusterArns []string
	clusters    []ecstypes.Cluster
	serviceArns []string
	services    []ecstypes.Service
	taskDef     *ecstypes.TaskDefinition
}

func (f *fakeECS) ListClusters(context.Context, *ecs.ListClustersInput, ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	return &ecs.ListClustersOutput{ClusterArns: f.clusterArns}, nil
}
func (f *fakeECS) DescribeClusters(context.Context, *ecs.DescribeClustersInput, ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	return &ecs.DescribeClustersOutput{Clusters: f.clusters}, nil
}
func (f *fakeECS) ListServices(context.Context, *ecs.ListServicesInput, ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	return &ecs.ListServicesOutput{ServiceArns: f.serviceArns}, nil
}
func (f *fakeECS) DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	return &ecs.DescribeServicesOutput{Services: f.services}, nil
}
func (f *fakeECS) DescribeTaskDefinition(context.Context, *ecs.DescribeTaskDefinitionInput, ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: f.taskDef}, nil
}

func TestECSDiscoverer_ClusterServiceTaskDefAndEdges(t *testing.T) {
	f := &fakeECS{
		clusterArns: []string{"arn:aws:ecs:us-east-1:222222222222:cluster/prod"},
		clusters: []ecstypes.Cluster{{
			ClusterName: aws.String("prod"), ClusterArn: aws.String("arn:aws:ecs:us-east-1:222222222222:cluster/prod"),
			Status: aws.String("ACTIVE"),
		}},
		serviceArns: []string{"arn:aws:ecs:us-east-1:222222222222:service/prod/checkout"},
		services: []ecstypes.Service{{
			ServiceName: aws.String("checkout"), ServiceArn: aws.String("arn:aws:ecs:us-east-1:222222222222:service/prod/checkout"),
			Status: aws.String("ACTIVE"), DesiredCount: 3, RunningCount: 3, LaunchType: ecstypes.LaunchTypeFargate,
			TaskDefinition: aws.String("arn:aws:ecs:us-east-1:222222222222:task-definition/checkout:7"),
		}},
		taskDef: &ecstypes.TaskDefinition{
			TaskDefinitionArn: aws.String("arn:aws:ecs:us-east-1:222222222222:task-definition/checkout:7"),
			Family:            aws.String("checkout"), Cpu: aws.String("1024"), Memory: aws.String("2048"),
			Revision: 7, Status: ecstypes.TaskDefinitionStatusActive,
		},
	}
	d := &ECSDiscoverer{newClient: func(aws.Config) ecsAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)

	cluster := mustFind(t, out.Resources, "prod")
	assert.Equal(t, cloud.KindECSCluster, cluster.Kind)

	svc := mustFind(t, out.Resources, "checkout")
	assert.Equal(t, cloud.PurchaseServerless, svc.Purchase)
	assert.Equal(t, 3, svc.Capacity.DesiredCount)

	td := mustFind(t, out.Resources, "arn:aws:ecs:us-east-1:222222222222:task-definition/checkout:7")
	assert.Equal(t, float64(1), td.Capacity.VCPU)
	assert.Equal(t, float64(2), td.Capacity.MemoryGiB)

	assertHasEdge(t, out.Relationships, cloud.RelRunsOn, svc.ID, cluster.ID)
}

func TestECSDiscoverer_RequiredActions(t *testing.T) {
	d := NewECSDiscoverer()
	assert.Equal(t, "ecs", d.Service())
	assert.Contains(t, d.RequiredActions(), "ecs:DescribeServices")
}

func TestChunkStrings(t *testing.T) {
	assert.Equal(t, [][]string{{"a", "b"}, {"c"}}, chunkStrings([]string{"a", "b", "c"}, 2))
	assert.Empty(t, chunkStrings(nil, 2))
}
