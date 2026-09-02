// This file discovers RDS instances, Aurora clusters and RDS snapshots, and
// is also where the flagship security-group-derived depends_on inference
// lives (see inferSGDependencies in common.go): after normalizing its own
// instances, this discoverer reads back the ingress rules of the security
// groups attached to them and, for every EC2 instance already on file whose
// own security group is a permitted ingress source, emits an INFERRED
// depends_on edge from that compute resource onto the database. This is the
// one place in the package that crosses into ec2:DescribeSecurityGroups from
// a non-EC2 discoverer, which is why RequiredActions lists it explicitly
// rather than assuming the EC2 discoverer's policy covers it.
package discovery

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// rdsAPI is every RDS call this discoverer makes.
type rdsAPI interface {
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, optFns ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBClusters(ctx context.Context, in *rds.DescribeDBClustersInput, optFns ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
	DescribeDBSnapshots(ctx context.Context, in *rds.DescribeDBSnapshotsInput, optFns ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error)
}

// rdsSGAPI is the one EC2 call the security-group inference step needs.
type rdsSGAPI interface {
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

// RDSDiscoverer implements ports.ResourceDiscoverer for RDS.
type RDSDiscoverer struct {
	newClient   func(aws.Config) rdsAPI
	newSGClient func(aws.Config) rdsSGAPI
}

var _ ports.ResourceDiscoverer = (*RDSDiscoverer)(nil)

func NewRDSDiscoverer() *RDSDiscoverer {
	return &RDSDiscoverer{
		newClient:   func(cfg aws.Config) rdsAPI { return rds.NewFromConfig(cfg) },
		newSGClient: func(cfg aws.Config) rdsSGAPI { return ec2.NewFromConfig(cfg) },
	}
}

func (d *RDSDiscoverer) Service() string { return "rds" }
func (d *RDSDiscoverer) Kinds() []cloud.Kind {
	return []cloud.Kind{cloud.KindRDSInstance, cloud.KindRDSCluster, cloud.KindRDSSnapshot}
}
func (d *RDSDiscoverer) RequiredActions() []string {
	return []string{
		"rds:DescribeDBInstances", "rds:DescribeDBClusters", "rds:DescribeDBSnapshots",
		"ec2:DescribeSecurityGroups",
	}
}

func (d *RDSDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	instanceSGIDs := map[core.ID][]string{} // RDS resource id -> its attached VPC security group ids

	instP := rds.NewDescribeDBInstancesPaginator(client, &rds.DescribeDBInstancesInput{})
	for instP.HasMorePages() {
		b.countCall()
		page, err := instP.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("rds: instances not available in region %s: %v", in.Region, err)
				break
			}
			return b.out, b.wrap(err, "rds", "DescribeDBInstances", "rds:DescribeDBInstances")
		}
		for _, i := range page.DBInstances {
			id, sgIDs := addDBInstance(b, in, i)
			instanceSGIDs[id] = sgIDs
		}
	}

	clusterP := rds.NewDescribeDBClustersPaginator(client, &rds.DescribeDBClustersInput{})
	for clusterP.HasMorePages() {
		b.countCall()
		page, err := clusterP.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("rds: clusters not available in region %s: %v", in.Region, err)
				break
			}
			return b.out, b.wrap(err, "rds", "DescribeDBClusters", "rds:DescribeDBClusters")
		}
		for _, c := range page.DBClusters {
			addDBCluster(b, in, c)
		}
	}

	snapP := rds.NewDescribeDBSnapshotsPaginator(client, &rds.DescribeDBSnapshotsInput{})
	for snapP.HasMorePages() {
		b.countCall()
		page, err := snapP.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("rds: snapshots not available in region %s: %v", in.Region, err)
				break
			}
			return b.out, b.wrap(err, "rds", "DescribeDBSnapshots", "rds:DescribeDBSnapshots")
		}
		for _, s := range page.DBSnapshots {
			addDBSnapshot(b, in, s)
		}
	}

	if len(instanceSGIDs) > 0 {
		d.inferDatabaseDependencies(ctx, b, cfg, instanceSGIDs)
	}
	return b.out, nil
}

func addDBInstance(b *builder, in ports.DiscoveryInput, i rdstypes.DBInstance) (core.ID, []string) {
	tags := rdsTags(i.TagList)
	nativeID := aws.ToString(i.DBInstanceIdentifier)
	sgIDs := make([]string, 0, len(i.VpcSecurityGroups))
	for _, sg := range i.VpcSecurityGroups {
		sgIDs = append(sgIDs, aws.ToString(sg.VpcSecurityGroupId))
	}
	isReplica := aws.ToString(i.ReadReplicaSourceDBInstanceIdentifier) != ""
	id := b.add(resourceSpec{
		Kind: cloud.KindRDSInstance, NativeID: nativeID, ARN: core.ARN(aws.ToString(i.DBInstanceArn)),
		Name: nativeID, Region: in.Region, AZ: aws.ToString(i.AvailabilityZone), State: mapState(aws.ToString(i.DBInstanceStatus)),
		InstanceType: aws.ToString(i.DBInstanceClass), Engine: aws.ToString(i.Engine), EngineVer: aws.ToString(i.EngineVersion),
		Capacity: cloud.Capacity{
			StorageGiB: float64(aws.ToInt32(i.AllocatedStorage)), ProvisionedIOPS: int64(aws.ToInt32(i.Iops)),
			ThroughputMiBps: float64(aws.ToInt32(i.StorageThroughput)),
		},
		Purchase: cloud.PurchaseOnDemand, Tags: tags,
		Attributes: attrs(
			"multi_az", boolStr(aws.ToBool(i.MultiAZ)), "storage_type", aws.ToString(i.StorageType),
			"is_read_replica", boolStr(isReplica), "primary_id", aws.ToString(i.ReadReplicaSourceDBInstanceIdentifier),
			"cluster_id", aws.ToString(i.DBClusterIdentifier), "publicly_accessible", boolStr(aws.ToBool(i.PubliclyAccessible)),
			"security_group_ids", strings.Join(sgIDs, ","),
		),
		CreatedAt: aws.ToTime(i.InstanceCreateTime), DiscoveredBy: "aws.rds",
	})
	if isReplica {
		if primaryID, ok := b.idOf(aws.ToString(i.ReadReplicaSourceDBInstanceIdentifier)); ok {
			b.edge(cloud.RelReplicaOf, id, primaryID, 1, 1.0, core.ProvenanceConfirmed)
		}
	}
	if clusterID := aws.ToString(i.DBClusterIdentifier); clusterID != "" {
		b.edgeNative(cloud.RelContains, clusterID, nativeID, 1)
	}
	return id, sgIDs
}

func addDBCluster(b *builder, in ports.DiscoveryInput, c rdstypes.DBCluster) {
	tags := rdsTags(c.TagList)
	nativeID := aws.ToString(c.DBClusterIdentifier)
	b.add(resourceSpec{
		Kind: cloud.KindRDSCluster, NativeID: nativeID, ARN: core.ARN(aws.ToString(c.DBClusterArn)),
		Name: nativeID, Region: in.Region, State: mapState(aws.ToString(c.Status)),
		Engine: aws.ToString(c.Engine), EngineVer: aws.ToString(c.EngineVersion),
		Capacity: cloud.Capacity{InstanceCount: len(c.DBClusterMembers), StorageGiB: float64(aws.ToInt32(c.AllocatedStorage))},
		Purchase: cloud.PurchaseOnDemand, Tags: tags,
		Attributes: attrs("engine_mode", aws.ToString(c.EngineMode), "multi_az", boolStr(aws.ToBool(c.MultiAZ))),
		CreatedAt:  aws.ToTime(c.ClusterCreateTime), DiscoveredBy: "aws.rds",
	})
}

func addDBSnapshot(b *builder, in ports.DiscoveryInput, s rdstypes.DBSnapshot) {
	tags := rdsTags(s.TagList)
	nativeID := aws.ToString(s.DBSnapshotIdentifier)
	b.add(resourceSpec{
		Kind: cloud.KindRDSSnapshot, NativeID: nativeID, ARN: core.ARN(aws.ToString(s.DBSnapshotArn)),
		Name: nativeID, Region: in.Region, State: mapState(aws.ToString(s.Status)),
		Engine:   aws.ToString(s.Engine),
		Capacity: cloud.Capacity{StorageGiB: float64(aws.ToInt32(s.AllocatedStorage))},
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("source_id", aws.ToString(s.DBInstanceIdentifier), "snapshot_type", aws.ToString(s.SnapshotType),
			"encrypted", boolStr(aws.ToBool(s.Encrypted))),
		CreatedAt: aws.ToTime(s.SnapshotCreateTime), DiscoveredBy: "aws.rds",
	})
}

// inferDatabaseDependencies reads back the ingress rules of every security
// group attached to a discovered RDS instance and, for each permitted
// source security group, calls inferSGDependencies so any already-known EC2
// instance carrying that source group gets an INFERRED depends_on edge onto
// the database.
func (d *RDSDiscoverer) inferDatabaseDependencies(ctx context.Context, b *builder, cfg aws.Config, instanceSGIDs map[core.ID][]string) {
	all := map[string]bool{}
	for _, ids := range instanceSGIDs {
		for _, id := range ids {
			all[id] = true
		}
	}
	if len(all) == 0 {
		return
	}
	groupIDs := make([]string, 0, len(all))
	for id := range all {
		groupIDs = append(groupIDs, id)
	}

	client := d.newSGClient(cfg)
	b.countCall()
	out, err := client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: groupIDs})
	if err != nil {
		// The dependency graph is an enrichment, not a correctness
		// requirement of the resources themselves: a permission gap here
		// (or the account simply not granting ec2:DescribeSecurityGroups to
		// the RDS discoverer's role) degrades to "no inferred edges" rather
		// than failing the whole discovery pass.
		b.warnf("rds: could not read security group rules for dependency inference: %v", err)
		return
	}
	permittedByGroup := map[string][]string{}
	for _, sg := range out.SecurityGroups {
		permittedByGroup[aws.ToString(sg.GroupId)] = permittedSources(sg.IpPermissions)
	}

	for instanceID, sgIDs := range instanceSGIDs {
		var permitted []string
		for _, sgID := range sgIDs {
			permitted = append(permitted, permittedByGroup[sgID]...)
		}
		b.inferSGDependencies(instanceID, permitted)
	}
}

// permittedSources extracts the security group ids named as an ingress
// source across a security group's rules — the set that
// inferSGDependencies matches against every known EC2 instance's own
// security groups.
func permittedSources(perms []ec2types.IpPermission) []string {
	var out []string
	for _, p := range perms {
		for _, pair := range p.UserIdGroupPairs {
			if id := aws.ToString(pair.GroupId); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func rdsTags(tags []rdstypes.Tag) core.Tags {
	pairs := make([][2]string, 0, len(tags))
	for _, t := range tags {
		pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
	}
	return tagsFromKV(pairs)
}
