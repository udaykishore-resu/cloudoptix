package executor

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

type fakeRDS struct {
	db       *rdstypes.DBInstance
	notFound bool
	calls    map[string]int
}

func newFakeRDS() *fakeRDS { return &fakeRDS{calls: map[string]int{}} }

func (f *fakeRDS) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	f.calls["DescribeDBInstances"]++
	if f.notFound || f.db == nil {
		return nil, notFoundErr("DBInstanceNotFound")
	}
	return &rds.DescribeDBInstancesOutput{DBInstances: []rdstypes.DBInstance{*f.db}}, nil
}

func (f *fakeRDS) ModifyDBInstance(_ context.Context, in *rds.ModifyDBInstanceInput, _ ...func(*rds.Options)) (*rds.ModifyDBInstanceOutput, error) {
	f.calls["ModifyDBInstance"]++
	if in.DBInstanceClass != nil {
		f.db.DBInstanceClass = in.DBInstanceClass
	}
	if in.AllocatedStorage != nil {
		f.db.AllocatedStorage = in.AllocatedStorage
	}
	if in.StorageType != nil {
		f.db.StorageType = in.StorageType
	}
	if in.Iops != nil {
		f.db.Iops = in.Iops
	}
	f.db.DBInstanceStatus = aws.String("available")
	return &rds.ModifyDBInstanceOutput{}, nil
}

func rdsExecutor(sp spec[rdsAPI], f *fakeRDS) *genericExecutor[rdsAPI] {
	return &genericExecutor[rdsAPI]{spec: sp, newClient: func(any) rdsAPI { return f }}
}

func TestResizeRDS_ChangesClassAndIsIdempotent(t *testing.T) {
	f := newFakeRDS()
	f.db = &rdstypes.DBInstance{DBInstanceIdentifier: aws.String("db-1"), DBInstanceClass: aws.String("db.m5.large"), DBInstanceStatus: aws.String("available")}
	ex := rdsExecutor(resizeRDSSpec, f)
	res := testResource(cloud.KindRDSInstance, "db-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeRDS, res, map[string]any{"instance_class": "db.m5.xlarge"}))
	require.NoError(t, err)
	assert.True(t, plan.Rollback.Feasible)

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, "db.m5.xlarge", out["instance_class"])
	assert.Equal(t, 1, f.calls["ModifyDBInstance"])

	out, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, true, out["idempotent"])
	assert.Equal(t, 1, f.calls["ModifyDBInstance"])

	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	assert.Equal(t, "db.m5.large", aws.ToString(f.db.DBInstanceClass))
}

func TestResizeRDS_RefusesWhenNotModifiable(t *testing.T) {
	f := newFakeRDS()
	f.db = &rdstypes.DBInstance{DBInstanceIdentifier: aws.String("db-1"), DBInstanceClass: aws.String("db.m5.large"), DBInstanceStatus: aws.String("creating")}
	ex := rdsExecutor(resizeRDSSpec, f)
	res := testResource(cloud.KindRDSInstance, "db-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeRDS, res, map[string]any{"instance_class": "db.m5.xlarge"}))
	require.NoError(t, err)
	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.Error(t, err)
}

func TestModifyRDSStorage_GrowsAndIsRollbackInfeasible(t *testing.T) {
	f := newFakeRDS()
	f.db = &rdstypes.DBInstance{
		DBInstanceIdentifier: aws.String("db-1"), DBInstanceClass: aws.String("db.m5.large"), DBInstanceStatus: aws.String("available"),
		AllocatedStorage: aws.Int32(100), StorageType: aws.String("gp2"),
	}
	ex := rdsExecutor(modifyRDSStorageSpec, f)
	res := testResource(cloud.KindRDSInstance, "db-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionModifyRDSStorage, res, map[string]any{"allocated_storage_gb": 200, "storage_type": "gp3"}))
	require.NoError(t, err)
	assert.False(t, plan.Rollback.Feasible, "RDS storage cannot be shrunk back, so this must be rollback-infeasible")

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, 200, out["allocated_storage"])
	assert.Equal(t, "gp3", out["storage_type"])
	assert.Equal(t, int32(200), aws.ToInt32(f.db.AllocatedStorage))

	err = ex.Rollback(context.Background(), testSession(), plan, execute.Step{Target: "db-1"})
	require.Error(t, err, "rollback of an infeasible plan must refuse outright")
}

func TestModifyRDSStorage_RejectsShrink(t *testing.T) {
	f := newFakeRDS()
	f.db = &rdstypes.DBInstance{DBInstanceIdentifier: aws.String("db-1"), DBInstanceStatus: aws.String("available"), AllocatedStorage: aws.Int32(200)}
	ex := rdsExecutor(modifyRDSStorageSpec, f)
	res := testResource(cloud.KindRDSInstance, "db-1")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionModifyRDSStorage, res, map[string]any{"allocated_storage_gb": 100}))
	require.NoError(t, err)
	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.Error(t, err)
}

func TestResizeRDS_PlanMissingReturnsNotFound(t *testing.T) {
	f := newFakeRDS()
	f.notFound = true
	ex := rdsExecutor(resizeRDSSpec, f)
	res := testResource(cloud.KindRDSInstance, "db-missing")
	_, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeRDS, res, map[string]any{"instance_class": "db.m5.xlarge"}))
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNotFound)
}
