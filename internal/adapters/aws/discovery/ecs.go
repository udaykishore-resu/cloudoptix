// This file discovers ECS clusters, services and the task definitions those
// services currently run. ListClusters/ListServices return ARNs only, so
// each is followed by a batched DescribeClusters/DescribeServices call
// (up to 100 ARNs per call, the API's own limit) rather than one Describe
// per resource. Task definitions are discovered only for the revision a
// live service currently references — walking every historical revision of
// every family would multiply API calls for data nothing downstream reads,
// since a superseded revision costs nothing and runs nothing.
package discovery

import (
	"context"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type ecsAPI interface {
	ListClusters(ctx context.Context, in *ecs.ListClustersInput, optFns ...func(*ecs.Options)) (*ecs.ListClustersOutput, error)
	DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, optFns ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error)
	ListServices(ctx context.Context, in *ecs.ListServicesInput, optFns ...func(*ecs.Options)) (*ecs.ListServicesOutput, error)
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
}

type ECSDiscoverer struct {
	newClient func(aws.Config) ecsAPI
}

var _ ports.ResourceDiscoverer = (*ECSDiscoverer)(nil)

func NewECSDiscoverer() *ECSDiscoverer {
	return &ECSDiscoverer{newClient: func(cfg aws.Config) ecsAPI { return ecs.NewFromConfig(cfg) }}
}

func (d *ECSDiscoverer) Service() string { return "ecs" }
func (d *ECSDiscoverer) Kinds() []cloud.Kind {
	return []cloud.Kind{cloud.KindECSCluster, cloud.KindECSService, cloud.KindECSTaskDef}
}
func (d *ECSDiscoverer) RequiredActions() []string {
	return []string{
		"ecs:ListClusters", "ecs:DescribeClusters", "ecs:ListServices", "ecs:DescribeServices",
		"ecs:DescribeTaskDefinition",
	}
}

func (d *ECSDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	var clusterArns []string
	cp := ecs.NewListClustersPaginator(client, &ecs.ListClustersInput{})
	for cp.HasMorePages() {
		b.countCall()
		page, err := cp.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("ecs: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "ecs", "ListClusters", "ecs:ListClusters")
		}
		clusterArns = append(clusterArns, page.ClusterArns...)
	}

	taskDefsSeen := map[string]bool{}
	for _, batch := range chunkStrings(clusterArns, 100) {
		b.countCall()
		desc, err := client.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: batch, Include: []ecstypes.ClusterField{ecstypes.ClusterFieldTags}})
		if err != nil {
			return b.out, b.wrap(err, "ecs", "DescribeClusters", "ecs:DescribeClusters")
		}
		for _, c := range desc.Clusters {
			addCluster(b, in, c)
			d.discoverServices(ctx, b, client, in, aws.ToString(c.ClusterArn), taskDefsSeen)
		}
	}
	return b.out, nil
}

func addCluster(b *builder, in ports.DiscoveryInput, c ecstypes.Cluster) {
	tags := ecsTags(c.Tags)
	nativeID := aws.ToString(c.ClusterName)
	b.add(resourceSpec{
		Kind: cloud.KindECSCluster, NativeID: nativeID, ARN: core.ARN(aws.ToString(c.ClusterArn)),
		Name: nativeID, Region: in.Region, State: mapState(aws.ToString(c.Status)),
		Capacity: cloud.Capacity{InstanceCount: int(c.RegisteredContainerInstancesCount)},
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes:   attrs("active_services_count", istr(int64(c.ActiveServicesCount)), "running_tasks_count", istr(int64(c.RunningTasksCount))),
		DiscoveredBy: "aws.ecs",
	})
}

func (d *ECSDiscoverer) discoverServices(ctx context.Context, b *builder, client ecsAPI, in ports.DiscoveryInput, clusterArn string, taskDefsSeen map[string]bool) {
	var serviceArns []string
	sp := ecs.NewListServicesPaginator(client, &ecs.ListServicesInput{Cluster: aws.String(clusterArn)})
	for sp.HasMorePages() {
		b.countCall()
		page, err := sp.NextPage(ctx)
		if err != nil {
			b.warnf("ecs: could not list services for cluster %s: %v", clusterArn, err)
			return
		}
		serviceArns = append(serviceArns, page.ServiceArns...)
	}
	clusterNativeID := clusterName(clusterArn)

	for _, batch := range chunkStrings(serviceArns, 10) { // DescribeServices caps at 10 ARNs per call
		b.countCall()
		desc, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(clusterArn), Services: batch, Include: []ecstypes.ServiceField{ecstypes.ServiceFieldTags},
		})
		if err != nil {
			b.warnf("ecs: could not describe services for cluster %s: %v", clusterArn, err)
			continue
		}
		for _, s := range desc.Services {
			addService(b, in, s, clusterNativeID)
			b.edgeNative(cloud.RelRunsOn, aws.ToString(s.ServiceName), clusterNativeID, 1)

			taskDefArn := aws.ToString(s.TaskDefinition)
			if taskDefArn == "" || taskDefsSeen[taskDefArn] {
				continue
			}
			taskDefsSeen[taskDefArn] = true
			b.countCall()
			if td, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(taskDefArn)}); err == nil && td.TaskDefinition != nil {
				addTaskDefinition(b, in, *td.TaskDefinition)
			}
		}
	}
}

func addService(b *builder, in ports.DiscoveryInput, s ecstypes.Service, clusterNativeID string) core.ID {
	tags := ecsTags(s.Tags)
	nativeID := aws.ToString(s.ServiceName)
	var cpu, memGiB float64
	// Fargate services carry CPU/memory on the task definition, not the
	// service itself; those are attached separately via addTaskDefinition.
	return b.add(resourceSpec{
		Kind: cloud.KindECSService, NativeID: nativeID, ARN: core.ARN(aws.ToString(s.ServiceArn)),
		Name: nativeID, Region: in.Region, State: mapState(aws.ToString(s.Status)),
		Capacity: cloud.Capacity{DesiredCount: int(s.DesiredCount), InstanceCount: int(s.RunningCount), VCPU: cpu, MemoryGiB: memGiB},
		Purchase: launchTypePurchase(s.LaunchType), Tags: tags,
		Attributes: attrs("launch_type", string(s.LaunchType), "cluster_id", clusterNativeID,
			"task_definition", aws.ToString(s.TaskDefinition), "platform_version", aws.ToString(s.PlatformVersion)),
		CreatedAt: aws.ToTime(s.CreatedAt), DiscoveredBy: "aws.ecs",
	})
}

func launchTypePurchase(lt ecstypes.LaunchType) cloud.PurchaseModel {
	if lt == ecstypes.LaunchTypeFargate {
		return cloud.PurchaseServerless
	}
	return cloud.PurchaseOnDemand
}

func addTaskDefinition(b *builder, in ports.DiscoveryInput, td ecstypes.TaskDefinition) {
	nativeID := aws.ToString(td.TaskDefinitionArn)
	cpuUnits := parseFloatOr(aws.ToString(td.Cpu), 0)
	memMB := parseFloatOr(aws.ToString(td.Memory), 0)
	b.add(resourceSpec{
		Kind: cloud.KindECSTaskDef, NativeID: nativeID, ARN: core.ARN(nativeID),
		Name: aws.ToString(td.Family), Region: in.Region, State: mapState(string(td.Status)),
		Capacity: cloud.Capacity{VCPU: cpuUnits / 1024, MemoryGiB: memMB / 1024},
		Purchase: cloud.PurchaseUnknown,
		Attributes: attrs("network_mode", string(td.NetworkMode), "revision", istr(int64(td.Revision)),
			"container_count", istr(int64(len(td.ContainerDefinitions)))),
		CreatedAt: aws.ToTime(td.RegisteredAt), DiscoveredBy: "aws.ecs",
	})
}

func ecsTags(tags []ecstypes.Tag) core.Tags {
	pairs := make([][2]string, 0, len(tags))
	for _, t := range tags {
		pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
	}
	return tagsFromKV(pairs)
}

func clusterName(arn string) string {
	for i := len(arn) - 1; i >= 0; i-- {
		if arn[i] == '/' {
			return arn[i+1:]
		}
	}
	return arn
}

func chunkStrings(items []string, size int) [][]string {
	if size <= 0 {
		return [][]string{items}
	}
	var out [][]string
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[i:end])
	}
	return out
}

func parseFloatOr(s string, def float64) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}
