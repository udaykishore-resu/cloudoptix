package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

type fakeRDS struct {
	instances    []rdstypes.DBInstance
	clusters     []rdstypes.DBCluster
	snapshots    []rdstypes.DBSnapshot
	instancesErr error
}

func (f *fakeRDS) DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	if f.instancesErr != nil {
		return nil, f.instancesErr
	}
	return &rds.DescribeDBInstancesOutput{DBInstances: f.instances}, nil
}
func (f *fakeRDS) DescribeDBClusters(context.Context, *rds.DescribeDBClustersInput, ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	return &rds.DescribeDBClustersOutput{DBClusters: f.clusters}, nil
}
func (f *fakeRDS) DescribeDBSnapshots(context.Context, *rds.DescribeDBSnapshotsInput, ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error) {
	return &rds.DescribeDBSnapshotsOutput{DBSnapshots: f.snapshots}, nil
}

type fakeSG struct {
	groups []ec2types.SecurityGroup
}

func (f *fakeSG) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: f.groups}, nil
}

func newRDSDiscovererWithFakes(r *fakeRDS, sg *fakeSG) *RDSDiscoverer {
	return &RDSDiscoverer{
		newClient:   func(aws.Config) rdsAPI { return r },
		newSGClient: func(aws.Config) rdsSGAPI { return sg },
	}
}

func TestRDSDiscoverer_NormalizesInstancesClustersSnapshots(t *testing.T) {
	r := &fakeRDS{
		instances: []rdstypes.DBInstance{
			{DBInstanceIdentifier: aws.String("primary-db"), DBInstanceClass: aws.String("db.r5.large"),
				Engine: aws.String("postgres"), EngineVersion: aws.String("15.4"),
				DBInstanceStatus: aws.String("available"), MultiAZ: aws.Bool(true),
				StorageType: aws.String("gp3"), AllocatedStorage: aws.Int32(200)},
			{DBInstanceIdentifier: aws.String("replica-db"), DBInstanceClass: aws.String("db.r5.large"),
				Engine: aws.String("postgres"), DBInstanceStatus: aws.String("available"),
				ReadReplicaSourceDBInstanceIdentifier: aws.String("primary-db")},
		},
		clusters:  []rdstypes.DBCluster{{DBClusterIdentifier: aws.String("aurora-1"), Engine: aws.String("aurora-postgresql"), Status: aws.String("available")}},
		snapshots: []rdstypes.DBSnapshot{{DBSnapshotIdentifier: aws.String("snap-1"), DBInstanceIdentifier: aws.String("primary-db"), Status: aws.String("available"), AllocatedStorage: aws.Int32(200)}},
	}
	d := newRDSDiscovererWithFakes(r, &fakeSG{})
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)

	primary := mustFind(t, out.Resources, "primary-db")
	assert.Equal(t, cloud.StateAvailable, primary.State)
	assert.Equal(t, "true", primary.Attr("multi_az", ""))
	assert.Equal(t, "false", primary.Attr("is_read_replica", ""))

	replica := mustFind(t, out.Resources, "replica-db")
	assert.Equal(t, "true", replica.Attr("is_read_replica", ""))
	assertHasEdge(t, out.Relationships, cloud.RelReplicaOf, findID(out, "replica-db"), findID(out, "primary-db"))

	cluster := mustFind(t, out.Resources, "aurora-1")
	assert.Equal(t, cloud.KindRDSCluster, cluster.Kind)

	snap := mustFind(t, out.Resources, "snap-1")
	assert.Equal(t, "primary-db", snap.Attr("source_id", ""))
}

func TestRDSDiscoverer_InfersDependencyFromSecurityGroupRules(t *testing.T) {
	r := &fakeRDS{
		instances: []rdstypes.DBInstance{
			{DBInstanceIdentifier: aws.String("orders-db"), DBInstanceStatus: aws.String("available"),
				VpcSecurityGroups: []rdstypes.VpcSecurityGroupMembership{{VpcSecurityGroupId: aws.String("sg-db")}}},
		},
	}
	sg := &fakeSG{groups: []ec2types.SecurityGroup{
		{GroupId: aws.String("sg-db"), IpPermissions: []ec2types.IpPermission{
			{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(5432), ToPort: aws.Int32(5432),
				UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-app")}}},
		}},
	}}
	d := newRDSDiscovererWithFakes(r, sg)

	in := discoveryInput()
	appInstance := cloud.Resource{
		ID: "res-app-1", NativeID: "i-app-1", Kind: cloud.KindEC2Instance,
		Attributes: map[string]string{"security_group_ids": "sg-app"},
	}
	unrelated := cloud.Resource{
		ID: "res-other-1", NativeID: "i-other-1", Kind: cloud.KindEC2Instance,
		Attributes: map[string]string{"security_group_ids": "sg-unrelated"},
	}
	in.Existing = cloud.NewInventory([]cloud.Resource{appInstance, unrelated})

	out, err := d.Discover(context.Background(), in)
	require.NoError(t, err)

	dbID := findID(out, "orders-db")
	require.NotEmpty(t, dbID)

	var found *cloud.Relationship
	for i := range out.Relationships {
		rel := out.Relationships[i]
		if rel.Kind == cloud.RelDependsOn && rel.ToID == dbID {
			found = &out.Relationships[i]
		}
	}
	require.NotNil(t, found, "expected an inferred depends_on edge from the app instance to the database")
	assert.Equal(t, appInstance.ID, found.FromID)
	assert.Equal(t, core.ProvenanceInferred, found.Source)
	assert.Less(t, float64(found.Confidence), 1.0, "an inferred edge must carry lower confidence than a confirmed one")

	for _, rel := range out.Relationships {
		assert.NotEqual(t, unrelated.ID, rel.FromID, "the unrelated instance's security group does not permit ingress, so it must not get an edge")
	}
}

func TestRDSDiscoverer_ThrottleAndDenied(t *testing.T) {
	d := newRDSDiscovererWithFakes(&fakeRDS{instancesErr: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}}, &fakeSG{})
	_, err := d.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrThrottled)

	d2 := newRDSDiscovererWithFakes(&fakeRDS{instancesErr: &smithy.GenericAPIError{
		Code: "AccessDenied", Message: "not authorized to perform: rds:DescribeDBInstances",
	}}, &fakeSG{})
	_, err = d2.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	var ce *core.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "rds:DescribeDBInstances", ce.Details["action"])
}

func TestRDSDiscoverer_RequiredActions(t *testing.T) {
	d := NewRDSDiscoverer()
	assert.Equal(t, "rds", d.Service())
	assert.Contains(t, d.RequiredActions(), "rds:DescribeDBInstances")
	assert.Contains(t, d.RequiredActions(), "ec2:DescribeSecurityGroups")
}
