package executor

import "github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
import "github.com/udaykishore-resu/cloudoptix/internal/ports"

func newResizeInstanceExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: resizeInstanceSpec, newClient: newEC2Client}
}
func newStopInstanceExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: stopInstanceSpec, newClient: newEC2Client}
}
func newScheduleShutdownExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: scheduleShutdownSpec, newClient: newEC2Client}
}
func newDeleteVolumeExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: deleteVolumeSpec, newClient: newEC2Client}
}
func newModifyVolumeTypeExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: modifyVolumeTypeSpec, newClient: newEC2Client}
}
func newResizeVolumeExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: resizeVolumeSpec, newClient: newEC2Client}
}
func newDeleteSnapshotExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: deleteSnapshotSpec, newClient: newEC2Client}
}
func newReleaseElasticIPExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: releaseElasticIPSpec, newClient: newEC2Client}
}
func newCreateVPCEndpointExecutor() ports.Executor {
	return &genericExecutor[ec2API]{spec: createVPCEndpointSpec, newClient: newEC2Client}
}

func newResizeRDSExecutor() ports.Executor {
	return &genericExecutor[rdsAPI]{spec: resizeRDSSpec, newClient: newRDSClient}
}
func newModifyRDSStorageExecutor() ports.Executor {
	return &genericExecutor[rdsAPI]{spec: modifyRDSStorageSpec, newClient: newRDSClient}
}

func newResizeNodeGroupExecutor() ports.Executor {
	return &genericExecutor[eksAPI]{spec: resizeNodeGroupSpec, newClient: newEKSClient}
}

func newApplyS3LifecycleExecutor() ports.Executor {
	return &genericExecutor[s3API]{spec: applyS3LifecycleSpec, newClient: newS3Client}
}
func newAbortMultipartUploadsExecutor() ports.Executor {
	return &genericExecutor[s3API]{spec: abortMultipartUploadsSpec, newClient: newS3Client}
}

func newResizeLambdaMemoryExecutor() ports.Executor {
	return &genericExecutor[lambdaAPI]{spec: resizeLambdaMemorySpec, newClient: newLambdaClient}
}
func newRemoveProvisionedConcurrencyExecutor() ports.Executor {
	return &genericExecutor[lambdaAPI]{spec: removeProvisionedConcurrencySpec, newClient: newLambdaClient}
}

// NewExecutors returns one Executor per action this package can actually
// perform against real AWS — sixteen of the seventeen actions this
// package's doc comment lists; set_log_retention is deliberately absent
// (see logs_unsupported.go and NewSetLogRetentionExecutor).
func NewExecutors() []ports.Executor {
	return []ports.Executor{
		newResizeInstanceExecutor(), newStopInstanceExecutor(), newScheduleShutdownExecutor(),
		newDeleteVolumeExecutor(), newModifyVolumeTypeExecutor(), newResizeVolumeExecutor(),
		newDeleteSnapshotExecutor(), newReleaseElasticIPExecutor(), newCreateVPCEndpointExecutor(),
		newResizeRDSExecutor(), newModifyRDSStorageExecutor(),
		newResizeNodeGroupExecutor(),
		newApplyS3LifecycleExecutor(), newAbortMultipartUploadsExecutor(),
		newResizeLambdaMemoryExecutor(), newRemoveProvisionedConcurrencyExecutor(),
	}
}

// NewExecutor returns the single executor for one action, if this package
// implements it (working or, for set_log_retention specifically, documented
// as unsupported — callers that want that distinction should use
// NewSetLogRetentionExecutor directly).
func NewExecutor(action optimize.ActionType) (ports.Executor, bool) {
	for _, e := range NewExecutors() {
		if e.Action() == action {
			return e, true
		}
	}
	return nil, false
}
