// This file implements the two RDS executors: resize_rds_instance (changes
// DBInstanceClass) and modify_rds_storage (changes AllocatedStorage and/or
// StorageType). The second has no awssim equivalent at all — awssim's own
// resize_rds_instance spec only ever touches DBInstanceClass — so its real
// behavior is designed here from first principles rather than translated
// from a simulator spec.
//
// Both mutate functions block on rds.NewDBInstanceAvailableWaiter after
// calling ModifyDBInstance: AWS applies most class and all storage changes
// asynchronously, and a class change in particular can trigger a brief
// failover, so "the change landed" genuinely means "the instance is
// available again", not merely "the API call returned 200". Storage can be
// grown but RDS, like EBS, never lets it shrink back — modify_rds_storage is
// rollback-infeasible for that reason, the same divergence from awssim's
// freely-reversible simulated storage that resize_volume documents in
// ec2.go.
package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

type rdsAPI interface {
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, optFns ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	ModifyDBInstance(ctx context.Context, in *rds.ModifyDBInstanceInput, optFns ...func(*rds.Options)) (*rds.ModifyDBInstanceOutput, error)
}

func newRDSClient(cfg any) rdsAPI { return rds.NewFromConfig(cfg.(aws.Config)) }

func isRDSNotFound(err error) bool {
	apiErr, ok := awserr.APIErrorOf(err)
	return ok && apiErr.ErrorCode() == "DBInstanceNotFound"
}

const rdsWaitTimeout = 15 * time.Minute

func captureRDSInstance(ctx context.Context, c rdsAPI, nativeID string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
	out, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(nativeID)})
	if err != nil {
		if isRDSNotFound(err) {
			return nil, false, nil
		}
		return nil, false, awserr.Translate(err, "rds", "DescribeDBInstances", "rds:DescribeDBInstances")
	}
	if len(out.DBInstances) == 0 {
		return nil, false, nil
	}
	db := out.DBInstances[0]
	return map[string]any{
		"instance_class":    aws.ToString(db.DBInstanceClass),
		"status":            aws.ToString(db.DBInstanceStatus),
		"allocated_storage": int(aws.ToInt32(db.AllocatedStorage)),
		"storage_type":      aws.ToString(db.StorageType),
		"iops":              int(aws.ToInt32(db.Iops)),
	}, true, nil
}

func rdsWaitAvailable(ctx context.Context, c rdsAPI, nativeID string) error {
	wctx, cancel := ctxWithTimeout(ctx, rdsWaitTimeout)
	defer cancel()
	err := rds.NewDBInstanceAvailableWaiter(c).Wait(wctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(nativeID)}, rdsWaitTimeout)
	if err != nil {
		return fmt.Errorf("aws executor: waiting for RDS instance %s to become available: %w", nativeID, err)
	}
	return nil
}

var resizeRDSSpec = spec[rdsAPI]{
	action: optimize.ActionResizeRDS, kind: cloud.KindRDSInstance,
	awsAction: "rds:ModifyDBInstance", titleFmt: "resize RDS instance %s to the recommended class",
	requiredActions:  []string{"rds:DescribeDBInstances", "rds:ModifyDBInstance"},
	rollbackFeasible: true, dataLossRisk: core.RiskMedium, // a class change can trigger a brief failover
	captureState: captureRDSInstance,
	extraPrecondition: func(current map[string]any) error {
		switch current["status"] {
		case "creating", "deleting", "failed":
			return core.Invalid("resize_rds_instance: instance is in status %q, not modifiable", current["status"])
		}
		return nil
	},
	isApplied: func(current, params map[string]any) bool {
		want, ok := paramStr(params, "instance_class")
		return ok && want == current["instance_class"]
	},
	mutate: func(ctx context.Context, c rdsAPI, nativeID string, params map[string]any, _ core.Region) (map[string]any, error) {
		target, ok := paramStr(params, "instance_class")
		if !ok || target == "" {
			return nil, core.Invalid("resize_rds_instance: missing instance_class parameter")
		}
		_, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
			DBInstanceIdentifier: aws.String(nativeID), DBInstanceClass: aws.String(target), ApplyImmediately: aws.Bool(true),
		})
		if err != nil {
			return nil, awserr.Translate(err, "rds", "ModifyDBInstance", "rds:ModifyDBInstance")
		}
		if err := rdsWaitAvailable(ctx, c, nativeID); err != nil {
			return nil, err
		}
		return map[string]any{"instance_class": target}, nil
	},
	restore: func(ctx context.Context, c rdsAPI, nativeID string, before map[string]any, _ core.Region) error {
		want, _ := before["instance_class"].(string)
		if want == "" {
			return core.Invalid("resize_rds_instance: rollback snapshot has no instance_class")
		}
		_, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
			DBInstanceIdentifier: aws.String(nativeID), DBInstanceClass: aws.String(want), ApplyImmediately: aws.Bool(true),
		})
		if err != nil {
			return awserr.Translate(err, "rds", "ModifyDBInstance", "rds:ModifyDBInstance")
		}
		return rdsWaitAvailable(ctx, c, nativeID)
	},
}

var modifyRDSStorageSpec = spec[rdsAPI]{
	action: optimize.ActionModifyRDSStorage, kind: cloud.KindRDSInstance,
	awsAction: "rds:ModifyDBInstance", titleFmt: "modify storage for RDS instance %s",
	requiredActions: []string{"rds:DescribeDBInstances", "rds:ModifyDBInstance"},
	// Like EBS (see ec2.go's resize_volume), RDS allocated storage can be
	// grown but AWS provides no operation to shrink it back down, so this
	// is rollback-infeasible even when the request only changes storage
	// type (not size) — the size half of a combined change still cannot be
	// undone once applied.
	rollbackFeasible: false, infeasibleReason: "RDS allocated storage cannot be reduced once increased",
	dataLossRisk: core.RiskLow,
	captureState: captureRDSInstance,
	extraPrecondition: func(current map[string]any) error {
		switch current["status"] {
		case "creating", "deleting", "failed":
			return core.Invalid("modify_rds_storage: instance is in status %q, not modifiable", current["status"])
		}
		return nil
	},
	isApplied: func(current, params map[string]any) bool {
		if want, ok := paramInt(params, "allocated_storage_gb"); ok && want != current["allocated_storage"] {
			return false
		}
		if want, ok := paramStr(params, "storage_type"); ok && want != "" && want != current["storage_type"] {
			return false
		}
		if want, ok := paramInt(params, "iops"); ok && want != current["iops"] {
			return false
		}
		return true
	},
	mutate: func(ctx context.Context, c rdsAPI, nativeID string, params map[string]any, _ core.Region) (map[string]any, error) {
		current, exists, err := captureRDSInstance(ctx, c, nativeID, nil, "")
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, core.NotFound(string(cloud.KindRDSInstance), nativeID)
		}
		in := &rds.ModifyDBInstanceInput{DBInstanceIdentifier: aws.String(nativeID), ApplyImmediately: aws.Bool(true)}
		out := map[string]any{}
		if target, ok := paramInt(params, "allocated_storage_gb"); ok && target > 0 {
			if currentSize, _ := current["allocated_storage"].(int); target <= currentSize {
				return nil, core.Invalid("modify_rds_storage: target %dGB is not larger than the current %dGB — RDS storage cannot be shrunk", target, currentSize)
			}
			in.AllocatedStorage = aws.Int32(int32(target))
			out["allocated_storage"] = target
		}
		if st, ok := paramStr(params, "storage_type"); ok && st != "" {
			in.StorageType = aws.String(st)
			out["storage_type"] = st
		}
		if iops, ok := paramInt(params, "iops"); ok && iops > 0 {
			in.Iops = aws.Int32(int32(iops))
			out["iops"] = iops
		}
		if len(out) == 0 {
			return nil, core.Invalid("modify_rds_storage: no storage parameters given")
		}
		if _, err := c.ModifyDBInstance(ctx, in); err != nil {
			return nil, awserr.Translate(err, "rds", "ModifyDBInstance", "rds:ModifyDBInstance")
		}
		if err := rdsWaitAvailable(ctx, c, nativeID); err != nil {
			return nil, err
		}
		return out, nil
	},
}
