package awssim

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const execRegion = core.Region("us-east-1")

func newExecEstate(t *testing.T) *Estate {
	t.Helper()
	return NewEstate(core.AccountID("111111111111"), "exec-test", []core.Region{execRegion}, pricing.New())
}

func execSession(t *testing.T, e *Estate) ports.AWSSession {
	t.Helper()
	b := NewBroker(e, cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute)
	s, err := b.Assume(context.Background(), cloud.AWSAccount{AccountID: e.AccountID}, cloud.ScopeExecute)
	require.NoError(t, err)
	return s
}

// execResource builds the minimal cloud.Resource an Executor needs: enough
// to find the target back in the estate (NativeID, Region) and to price a
// baseline (MonthlyCost, informational only).
func execResource(kind cloud.Kind, nativeID string) cloud.Resource {
	return cloud.Resource{
		ID: core.NewID("res"), TenantID: testTenant, Region: execRegion, Kind: kind, NativeID: nativeID,
		Environment: core.EnvProduction,
	}
}

// runLifecycle drives Plan -> Preflight -> Apply for every step in order,
// then re-applies the mutate step once more to prove it is idempotent. It
// returns the built plan and the estate's total cost immediately before
// and immediately after the mutate step, leaving cost-delta and rollback
// assertions to the caller since those differ by action.
func runLifecycle(t *testing.T, exec ports.Executor, estate *Estate, resource cloud.Resource, params map[string]any) (plan execute.Plan, before, after core.Money) {
	t.Helper()
	ctx := context.Background()
	session := execSession(t, estate)

	before = estate.TotalMonthlyCost()

	rec := optimize.Recommendation{
		ID: core.NewID("rec"), Action: exec.Action(), Parameters: params,
	}
	plan, err := exec.Plan(ctx, ports.ExecutionPlanInput{
		TenantID: testTenant, Recommendation: rec, Resource: resource,
		Account: cloud.AWSAccount{AccountID: estate.AccountID}, Session: session, RequestedBy: "test-suite",
	})
	require.NoError(t, err)
	require.Len(t, plan.Steps, 4)
	require.NotNil(t, plan.Rollback)

	require.NoError(t, exec.Preflight(ctx, session, plan))

	for _, step := range plan.Steps {
		out, err := exec.Apply(ctx, session, plan, step)
		require.NoError(t, err, "step %s (%s) failed", step.Name, step.Kind)
		require.NotNil(t, out)
		if step.Kind == execute.StepVerify {
			applied, _ := out["applied"].(bool)
			assert.True(t, applied, "verify step should report the change as applied")
		}
	}

	after = estate.TotalMonthlyCost()

	// Idempotency: re-applying the same mutate step (same IdempotencyKey,
	// same Parameters) must report "already applied" and must not change
	// the estate's cost any further.
	mutateStep, ok := firstStepOfKind(plan, execute.StepMutate)
	require.True(t, ok)
	replay, err := exec.Apply(ctx, session, plan, mutateStep)
	require.NoError(t, err)
	idem, _ := replay["idempotent"].(bool)
	assert.True(t, idem, "re-applying the mutate step should report idempotent, got %#v", replay)
	assert.Equal(t, after.Micros(), estate.TotalMonthlyCost().Micros(), "re-applying the mutate step must not change cost further")

	return plan, before, after
}

func TestExecutors_ResizeInstance(t *testing.T) {
	e := newExecEstate(t)
	e.EC2Instances["i-resize"] = &EC2Instance{
		Base:         Base{ID: "i-resize", Region: execRegion, AZ: "us-east-1a", State: cloud.StateRunning, Tags: core.Tags{}},
		InstanceType: "m5.4xlarge", Platform: "linux",
	}
	resource := execResource(cloud.KindEC2Instance, "i-resize")

	exec, ok := NewExecutor(optimize.ActionResizeInstance)
	require.True(t, ok)

	plan, before, after := runLifecycle(t, exec, e, resource, map[string]any{"instance_type": "m5.large"})
	assert.True(t, after.LessThan(before), "resizing down should reduce total cost: before=%s after=%s", before, after)
	assert.Equal(t, "m5.large", e.EC2Instances["i-resize"].InstanceType)

	// Rollback restores the original instance type and cost.
	session := execSession(t, e)
	rbStep := plan.Rollback.Steps[0]
	require.NoError(t, exec.Rollback(context.Background(), session, plan, rbStep))
	assert.Equal(t, "m5.4xlarge", e.EC2Instances["i-resize"].InstanceType)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_ResizeInstance_DryRun(t *testing.T) {
	e := newExecEstate(t)
	e.EC2Instances["i-dry"] = &EC2Instance{
		Base: Base{ID: "i-dry", Region: execRegion, State: cloud.StateRunning, Tags: core.Tags{}}, InstanceType: "m5.4xlarge", Platform: "linux",
	}
	resource := execResource(cloud.KindEC2Instance, "i-dry")
	exec, _ := NewExecutor(optimize.ActionResizeInstance)
	session := execSession(t, e)

	plan, err := exec.Plan(context.Background(), ports.ExecutionPlanInput{
		TenantID: testTenant, Resource: resource, Account: cloud.AWSAccount{AccountID: e.AccountID}, Session: session,
		DryRun: true, Recommendation: optimize.Recommendation{Action: exec.Action(), Parameters: map[string]any{"instance_type": "m5.large"}},
	})
	require.NoError(t, err)
	assert.True(t, plan.DryRun)

	before := e.TotalMonthlyCost()
	mutateStep, _ := firstStepOfKind(plan, execute.StepMutate)
	out, err := exec.Apply(context.Background(), session, plan, mutateStep)
	require.NoError(t, err)
	dry, _ := out["dry_run"].(bool)
	assert.True(t, dry, "a dry-run mutate step must not mutate the estate")
	assert.Equal(t, "m5.4xlarge", e.EC2Instances["i-dry"].InstanceType, "instance type must be unchanged after a dry run")
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_StopInstance(t *testing.T) {
	e := newExecEstate(t)
	e.EC2Instances["i-stop"] = &EC2Instance{
		Base: Base{ID: "i-stop", Region: execRegion, State: cloud.StateRunning, Tags: core.Tags{}}, InstanceType: "m5.xlarge", Platform: "linux",
	}
	resource := execResource(cloud.KindEC2Instance, "i-stop")
	exec, _ := NewExecutor(optimize.ActionStopInstance)

	plan, before, after := runLifecycle(t, exec, e, resource, nil)
	assert.True(t, after.LessThan(before))
	assert.Equal(t, cloud.StateStopped, e.EC2Instances["i-stop"].State)
	require.NotNil(t, e.EC2Instances["i-stop"].StoppedAt)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, cloud.StateRunning, e.EC2Instances["i-stop"].State)
	assert.Nil(t, e.EC2Instances["i-stop"].StoppedAt)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_ScheduleShutdown(t *testing.T) {
	e := newExecEstate(t)
	e.EC2Instances["i-sched"] = &EC2Instance{
		Base: Base{ID: "i-sched", Region: execRegion, State: cloud.StateRunning, Tags: core.Tags{}}, InstanceType: "m5.large", Platform: "linux",
	}
	resource := execResource(cloud.KindEC2Instance, "i-sched")
	exec, _ := NewExecutor(optimize.ActionScheduleShutdown)

	plan, before, after := runLifecycle(t, exec, e, resource, map[string]any{"schedule": "stop nights and weekends"})
	assert.True(t, after.LessThan(before))
	assert.Equal(t, cloud.StateStopped, e.EC2Instances["i-sched"].State)
	assert.Equal(t, "stop nights and weekends", e.EC2Instances["i-sched"].Tags["cloudoptix:schedule"])

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, cloud.StateRunning, e.EC2Instances["i-sched"].State)
	assert.Empty(t, e.EC2Instances["i-sched"].Tags["cloudoptix:schedule"])
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_ResizeRDS(t *testing.T) {
	e := newExecEstate(t)
	e.RDSInstances["db-1"] = &RDSInstance{
		Base:          Base{ID: "db-1", Region: execRegion, State: cloud.StateAvailable, Tags: core.Tags{}},
		InstanceClass: "db.r5.2xlarge", Engine: "postgres", StorageGiB: 100, StorageType: "gp3",
	}
	resource := execResource(cloud.KindRDSInstance, "db-1")
	exec, _ := NewExecutor(optimize.ActionResizeRDS)

	plan, before, after := runLifecycle(t, exec, e, resource, map[string]any{"instance_class": "db.r5.large"})
	assert.True(t, after.LessThan(before))
	assert.Equal(t, "db.r5.large", e.RDSInstances["db-1"].InstanceClass)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, "db.r5.2xlarge", e.RDSInstances["db-1"].InstanceClass)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_ResizeNodeGroup(t *testing.T) {
	e := newExecEstate(t)
	e.EKSNodeGroups["ng-1"] = &EKSNodeGroup{
		Base: Base{ID: "ng-1", Region: execRegion, Tags: core.Tags{}}, ClusterID: "cluster-1",
		InstanceType: "m5.xlarge", DesiredSize: 20, PackedFraction: 0.35,
	}
	resource := execResource(cloud.KindEKSNodeGroup, "ng-1")
	exec, _ := NewExecutor(optimize.ActionResizeNodeGroup)

	plan, before, after := runLifecycle(t, exec, e, resource, map[string]any{"desired_size": 10})
	assert.True(t, after.LessThan(before))
	assert.Equal(t, 10, e.EKSNodeGroups["ng-1"].DesiredSize)
	assert.InDelta(t, 0.70, e.EKSNodeGroups["ng-1"].PackedFraction, 0.001)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, 20, e.EKSNodeGroups["ng-1"].DesiredSize)
	assert.InDelta(t, 0.35, e.EKSNodeGroups["ng-1"].PackedFraction, 0.001)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_DeleteVolume(t *testing.T) {
	e := newExecEstate(t)
	e.EBSVolumes["vol-1"] = &EBSVolume{
		Base: Base{ID: "vol-1", Region: execRegion, AZ: "us-east-1a", Tags: core.Tags{}}, VolumeType: "gp2", SizeGiB: 500,
	}
	resource := execResource(cloud.KindEBSVolume, "vol-1")
	exec, _ := NewExecutor(optimize.ActionDeleteVolume)

	plan, before, after := runLifecycle(t, exec, e, resource, nil)
	assert.True(t, after.LessThan(before))
	_, exists := e.EBSVolumes["vol-1"]
	assert.False(t, exists)

	session := execSession(t, e)
	assert.False(t, plan.Rollback.Feasible)
	err := exec.Rollback(context.Background(), session, plan, execute.Step{Target: "vol-1"})
	assert.Error(t, err, "deleting a volume must be irreversible")
}

func TestExecutors_DeleteVolume_AttachedIsRejected(t *testing.T) {
	e := newExecEstate(t)
	e.EBSVolumes["vol-attached"] = &EBSVolume{
		Base: Base{ID: "vol-attached", Region: execRegion, Tags: core.Tags{}}, VolumeType: "gp2", SizeGiB: 100, AttachedTo: "i-something",
	}
	resource := execResource(cloud.KindEBSVolume, "vol-attached")
	exec, _ := NewExecutor(optimize.ActionDeleteVolume)
	session := execSession(t, e)

	plan, err := exec.Plan(context.Background(), ports.ExecutionPlanInput{
		TenantID: testTenant, Resource: resource, Account: cloud.AWSAccount{AccountID: e.AccountID}, Session: session,
		Recommendation: optimize.Recommendation{Action: exec.Action()},
	})
	require.NoError(t, err)

	assert.Error(t, exec.Preflight(context.Background(), session, plan), "an attached volume must fail preflight")

	mutateStep, _ := firstStepOfKind(plan, execute.StepMutate)
	_, err = exec.Apply(context.Background(), session, plan, mutateStep)
	assert.Error(t, err, "an attached volume must not be deleted")
	_, stillExists := e.EBSVolumes["vol-attached"]
	assert.True(t, stillExists)
}

func TestExecutors_ModifyVolumeType(t *testing.T) {
	e := newExecEstate(t)
	e.EBSVolumes["vol-2"] = &EBSVolume{
		Base: Base{ID: "vol-2", Region: execRegion, Tags: core.Tags{}}, VolumeType: "gp2", SizeGiB: 1000,
	}
	resource := execResource(cloud.KindEBSVolume, "vol-2")
	exec, _ := NewExecutor(optimize.ActionModifyVolumeType)

	plan, before, after := runLifecycle(t, exec, e, resource, map[string]any{"volume_type": "gp3"})
	assert.True(t, after.LessThan(before), "gp2 to gp3 should reduce cost: before=%s after=%s", before, after)
	assert.Equal(t, "gp3", e.EBSVolumes["vol-2"].VolumeType)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, "gp2", e.EBSVolumes["vol-2"].VolumeType)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_ResizeVolume_GrowsAndRejectsShrink(t *testing.T) {
	e := newExecEstate(t)
	e.EBSVolumes["vol-3"] = &EBSVolume{
		Base: Base{ID: "vol-3", Region: execRegion, Tags: core.Tags{}}, VolumeType: "gp3", SizeGiB: 100,
	}
	resource := execResource(cloud.KindEBSVolume, "vol-3")
	exec, _ := NewExecutor(optimize.ActionResizeVolume)

	plan, before, after := runLifecycle(t, exec, e, resource, map[string]any{"size_gib": float64(200)})
	assert.True(t, after.GreaterThan(before), "growing a volume increases cost")
	assert.Equal(t, 200.0, e.EBSVolumes["vol-3"].SizeGiB)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, 100.0, e.EBSVolumes["vol-3"].SizeGiB)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())

	// A second, independent plan targeting a shrink must be rejected by mutate.
	plan2, err := exec.Plan(context.Background(), ports.ExecutionPlanInput{
		TenantID: testTenant, Resource: resource, Account: cloud.AWSAccount{AccountID: e.AccountID}, Session: session,
		Recommendation: optimize.Recommendation{Action: exec.Action(), Parameters: map[string]any{"size_gib": float64(50)}},
	})
	require.NoError(t, err)
	mutateStep, _ := firstStepOfKind(plan2, execute.StepMutate)
	_, err = exec.Apply(context.Background(), session, plan2, mutateStep)
	assert.Error(t, err, "shrinking an EBS volume must be rejected")
}

func TestExecutors_DeleteSnapshot(t *testing.T) {
	e := newExecEstate(t)
	e.EBSSnapshots["snap-1"] = &EBSSnapshot{Base: Base{ID: "snap-1", Region: execRegion, Tags: core.Tags{}}, VolumeID: "vol-x", SizeGiB: 200}
	resource := execResource(cloud.KindEBSSnapshot, "snap-1")
	exec, _ := NewExecutor(optimize.ActionDeleteSnapshot)

	plan, before, after := runLifecycle(t, exec, e, resource, nil)
	assert.True(t, after.LessThan(before))
	_, exists := e.EBSSnapshots["snap-1"]
	assert.False(t, exists)

	session := execSession(t, e)
	assert.Error(t, exec.Rollback(context.Background(), session, plan, execute.Step{Target: "snap-1"}))
}

func TestExecutors_ReleaseElasticIP(t *testing.T) {
	e := newExecEstate(t)
	e.ElasticIPs["eip-1"] = &ElasticIP{Base: Base{ID: "eip-1", Region: execRegion, Tags: core.Tags{}}, PublicIP: "203.0.113.5"}
	resource := execResource(cloud.KindElasticIP, "eip-1")
	exec, _ := NewExecutor(optimize.ActionReleaseElasticIP)

	plan, before, after := runLifecycle(t, exec, e, resource, nil)
	assert.True(t, after.LessThan(before))
	_, exists := e.ElasticIPs["eip-1"]
	assert.False(t, exists)

	session := execSession(t, e)
	assert.Error(t, exec.Rollback(context.Background(), session, plan, execute.Step{Target: "eip-1"}))
}

// TestExecutors_ApplyS3Lifecycle covers the non-current-version expiry
// clause. The parameters are no longer nil: apply_s3_lifecycle applies the
// clauses a recommendation names, and a recommendation that names none is
// refused rather than quietly performing some default change — see
// TestExecutors_ApplyS3Lifecycle_RefusesEmptyClauseSet.
func TestExecutors_ApplyS3Lifecycle(t *testing.T) {
	e := newExecEstate(t)
	e.S3Buckets["bucket-1"] = &S3Bucket{
		Base: Base{ID: "bucket-1", Region: execRegion, Tags: core.Tags{}}, StorageGiB: map[string]float64{"standard": 500},
		NonCurrentVersionGiB: 300, HasLifecyclePolicy: false,
	}
	resource := execResource(cloud.KindS3Bucket, "bucket-1")
	exec, _ := NewExecutor(optimize.ActionApplyS3Lifecycle)

	params := map[string]any{
		"rule_id":                    "cloudoptix-s3-noncurrent-versions",
		"noncurrent_expiration_days": 30,
	}
	plan, before, after := runLifecycle(t, exec, e, resource, params)
	assert.True(t, after.LessThan(before))
	assert.True(t, e.S3Buckets["bucket-1"].HasLifecyclePolicy)
	assert.Zero(t, e.S3Buckets["bucket-1"].NonCurrentVersionGiB)
	// The current objects are untouched: a non-current-version expiry and a
	// storage-class transition are different clauses on different bytes.
	assert.Equal(t, 500.0, e.S3Buckets["bucket-1"].StorageGiB["standard"])

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.False(t, e.S3Buckets["bucket-1"].HasLifecyclePolicy)
	assert.Equal(t, 300.0, e.S3Buckets["bucket-1"].NonCurrentVersionGiB)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

// TestExecutors_ApplyS3Lifecycle_TransitionMovesStandardBytes proves the
// transition clause is actually read. The s3-no-lifecycle,
// s3-wrong-storage-class and s3-intelligent-tiering rules all propose a
// transition and nothing else; before the parameter shapes were aligned they
// emitted target_class, which this executor never read, so the change they
// proposed was not the change that ran.
func TestExecutors_ApplyS3Lifecycle_TransitionMovesStandardBytes(t *testing.T) {
	e := newExecEstate(t)
	e.S3Buckets["bucket-3"] = &S3Bucket{
		Base:       Base{ID: "bucket-3", Region: execRegion, Tags: core.Tags{}},
		StorageGiB: map[string]float64{"standard": 1000},
	}
	resource := execResource(cloud.KindS3Bucket, "bucket-3")
	exec, _ := NewExecutor(optimize.ActionApplyS3Lifecycle)

	params := map[string]any{
		"rule_id":                  "cloudoptix-s3-no-lifecycle-policy",
		"transition_days":          30,
		"transition_storage_class": "STANDARD_IA",
	}
	plan, before, after := runLifecycle(t, exec, e, resource, params)
	assert.True(t, after.LessThan(before), "moving Standard bytes to Standard-IA must reduce cost")

	b := e.S3Buckets["bucket-3"]
	moved := 1000 * simLifecycleColdShare
	assert.InDelta(t, 1000-moved, b.StorageGiB["standard"], 1e-6)
	assert.InDelta(t, moved, b.StorageGiB["standard_ia"], 1e-6)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.InDelta(t, 1000.0, e.S3Buckets["bucket-3"].StorageGiB["standard"], 1e-6)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

// TestExecutors_ApplyS3Lifecycle_ComposesTwoRules proves two lifecycle
// recommendations against one bucket compose instead of overwriting each
// other. They are kept apart by rule_id, exactly as the live executor keeps
// them apart inside a real bucket's lifecycle configuration; with a single
// shared id the second plan's isApplied would report "already applied" and
// the second clause would never run.
func TestExecutors_ApplyS3Lifecycle_ComposesTwoRules(t *testing.T) {
	e := newExecEstate(t)
	e.S3Buckets["bucket-4"] = &S3Bucket{
		Base:       Base{ID: "bucket-4", Region: execRegion, Tags: core.Tags{}},
		StorageGiB: map[string]float64{"standard": 400}, NonCurrentVersionGiB: 100,
	}
	resource := execResource(cloud.KindS3Bucket, "bucket-4")
	exec, _ := NewExecutor(optimize.ActionApplyS3Lifecycle)

	runLifecycle(t, exec, e, resource, map[string]any{
		"rule_id": "cloudoptix-s3-no-lifecycle-policy", "transition_days": 30,
		"transition_storage_class": "STANDARD_IA",
	})
	runLifecycle(t, exec, e, resource, map[string]any{
		"rule_id": "cloudoptix-s3-noncurrent-versions", "noncurrent_expiration_days": 30,
	})

	b := e.S3Buckets["bucket-4"]
	assert.Zero(t, b.NonCurrentVersionGiB, "the second rule's clause must have run")
	assert.InDelta(t, 400*simLifecycleColdShare, b.StorageGiB["standard_ia"], 1e-6,
		"the first rule's clause must have survived the second")
	assert.Len(t, b.LifecycleRuleIDs, 2)
}

// TestExecutors_ApplyS3Lifecycle_RefusesEmptyClauseSet is the guard the
// parameter-shape defect walked straight past: a recommendation whose
// parameters name no clause this action can apply used to produce an enabled
// lifecycle rule that did nothing, reported as a success with a saving
// attached. Refusing is the honest outcome.
func TestExecutors_ApplyS3Lifecycle_RefusesEmptyClauseSet(t *testing.T) {
	e := newExecEstate(t)
	e.S3Buckets["bucket-5"] = &S3Bucket{
		Base:       Base{ID: "bucket-5", Region: execRegion, Tags: core.Tags{}},
		StorageGiB: map[string]float64{"standard": 100},
	}
	resource := execResource(cloud.KindS3Bucket, "bucket-5")
	exec, _ := NewExecutor(optimize.ActionApplyS3Lifecycle)
	session := execSession(t, e)

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"no parameters at all", nil},
		{"the pre-fix spelling no executor reads", map[string]any{"target_class": "standard_ia"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := optimize.Recommendation{ID: core.NewID("rec"), Action: exec.Action(), Parameters: tc.params}
			plan, err := exec.Plan(context.Background(), ports.ExecutionPlanInput{
				TenantID: testTenant, Recommendation: rec, Resource: resource,
				Account: cloud.AWSAccount{AccountID: e.AccountID}, Session: session, RequestedBy: "test-suite",
			})
			require.NoError(t, err)
			mutate, ok := firstStepOfKind(plan, execute.StepMutate)
			require.True(t, ok)
			_, err = exec.Apply(context.Background(), session, plan, mutate)
			require.Error(t, err)
			assert.False(t, e.S3Buckets["bucket-5"].HasLifecyclePolicy)
		})
	}
}

func TestExecutors_AbortMultipartUploads(t *testing.T) {
	e := newExecEstate(t)
	e.S3Buckets["bucket-2"] = &S3Bucket{
		Base: Base{ID: "bucket-2", Region: execRegion, Tags: core.Tags{}}, StorageGiB: map[string]float64{"standard": 10},
		IncompleteMultipartCount: 40, IncompleteMultipartGiB: 800,
	}
	resource := execResource(cloud.KindS3Bucket, "bucket-2")
	exec, _ := NewExecutor(optimize.ActionAbortMultipartUploads)

	plan, before, after := runLifecycle(t, exec, e, resource, nil)
	assert.True(t, after.LessThan(before))
	assert.Zero(t, e.S3Buckets["bucket-2"].IncompleteMultipartCount)
	assert.Zero(t, e.S3Buckets["bucket-2"].IncompleteMultipartGiB)

	session := execSession(t, e)
	assert.Error(t, exec.Rollback(context.Background(), session, plan, execute.Step{Target: "bucket-2"}))
}

func TestExecutors_SetLogRetention(t *testing.T) {
	e := newExecEstate(t)
	e.LogGroups["lg-1"] = &LogGroup{
		Base: Base{ID: "lg-1", Region: execRegion, Tags: core.Tags{}}, RetentionDays: 0,
		IngestGBPerMonth: 5, StoredGiB: 900,
	}
	resource := execResource(cloud.KindLogGroup, "lg-1")
	exec, _ := NewExecutor(optimize.ActionSetLogRetention)

	plan, before, after := runLifecycle(t, exec, e, resource, map[string]any{"retention_days": 30})
	assert.True(t, after.LessThan(before))
	assert.Equal(t, 30, e.LogGroups["lg-1"].RetentionDays)
	assert.Less(t, e.LogGroups["lg-1"].StoredGiB, 900.0)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, 0, e.LogGroups["lg-1"].RetentionDays)
	assert.Equal(t, 900.0, e.LogGroups["lg-1"].StoredGiB)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_ResizeLambdaMemory(t *testing.T) {
	e := newExecEstate(t)
	e.LambdaFunctions["fn-1"] = &LambdaFunction{
		Base: Base{ID: "fn-1", Region: execRegion, Tags: core.Tags{}}, MemoryMB: 3008, AvgDurationMS: 400,
		InvocationsPerMonth: 5_000_000, Architecture: "x86_64",
	}
	resource := execResource(cloud.KindLambdaFunction, "fn-1")
	exec, _ := NewExecutor(optimize.ActionResizeLambdaMemory)

	plan, before, after := runLifecycle(t, exec, e, resource, map[string]any{"memory_mb": 512})
	assert.True(t, after.LessThan(before))
	assert.Equal(t, 512, e.LambdaFunctions["fn-1"].MemoryMB)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, 3008, e.LambdaFunctions["fn-1"].MemoryMB)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_RemoveProvisionedConcurrency(t *testing.T) {
	e := newExecEstate(t)
	e.LambdaFunctions["fn-2"] = &LambdaFunction{
		Base: Base{ID: "fn-2", Region: execRegion, Tags: core.Tags{}}, MemoryMB: 1024, AvgDurationMS: 100,
		InvocationsPerMonth: 100_000, Architecture: "x86_64", ProvisionedConcurrency: 20,
	}
	resource := execResource(cloud.KindLambdaFunction, "fn-2")
	exec, _ := NewExecutor(optimize.ActionRemoveProvisionedConcurrency)

	plan, before, after := runLifecycle(t, exec, e, resource, nil)
	assert.True(t, after.LessThan(before))
	assert.Zero(t, e.LambdaFunctions["fn-2"].ProvisionedConcurrency)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.Equal(t, 20, e.LambdaFunctions["fn-2"].ProvisionedConcurrency)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

func TestExecutors_CreateVPCEndpoint(t *testing.T) {
	e := newExecEstate(t)
	e.VPCs["vpc-1"] = &VPC{Base: Base{ID: "vpc-1", Region: execRegion, Tags: core.Tags{}}, CIDR: "10.0.0.0/16"}
	e.Subnets["subnet-1"] = &Subnet{Base: Base{ID: "subnet-1", Region: execRegion, Tags: core.Tags{}}, VPCID: "vpc-1", CIDR: "10.0.1.0/24"}
	e.NATGateways["nat-1"] = &NATGateway{
		Base: Base{ID: "nat-1", Region: execRegion, AZ: "us-east-1a", Tags: core.Tags{}}, SubnetID: "subnet-1", GBProcessedPerMonth: 100_000,
	}
	resource := execResource(cloud.KindNATGateway, "nat-1")
	exec, _ := NewExecutor(optimize.ActionCreateVPCEndpoint)

	plan, before, after := runLifecycle(t, exec, e, resource, nil)
	assert.True(t, after.LessThan(before), "offloading 80%% of NAT traffic onto a cheap endpoint should reduce total cost")
	assert.InDelta(t, 20_000, e.NATGateways["nat-1"].GBProcessedPerMonth, 0.01)
	ep, exists := e.VPCEndpoints["vpce-nat-1"]
	require.True(t, exists)
	assert.Equal(t, "vpc-1", ep.VPCID)

	session := execSession(t, e)
	require.NoError(t, exec.Rollback(context.Background(), session, plan, plan.Rollback.Steps[0]))
	assert.InDelta(t, 100_000, e.NATGateways["nat-1"].GBProcessedPerMonth, 0.01)
	_, stillExists := e.VPCEndpoints["vpce-nat-1"]
	assert.False(t, stillExists)
	assert.Equal(t, before.Micros(), e.TotalMonthlyCost().Micros())
}

// TestExecutors_AllRegistered checks NewExecutors produces exactly the 16
// mutating actions this package implements, each wired to the resource
// kind its RequiredActions and Plan/Apply logic assume.
func TestExecutors_AllRegistered(t *testing.T) {
	want := []optimize.ActionType{
		optimize.ActionResizeInstance, optimize.ActionStopInstance, optimize.ActionScheduleShutdown,
		optimize.ActionResizeRDS, optimize.ActionResizeNodeGroup,
		optimize.ActionDeleteVolume, optimize.ActionModifyVolumeType, optimize.ActionResizeVolume,
		optimize.ActionDeleteSnapshot, optimize.ActionReleaseElasticIP,
		optimize.ActionApplyS3Lifecycle, optimize.ActionAbortMultipartUploads, optimize.ActionSetLogRetention,
		optimize.ActionResizeLambdaMemory, optimize.ActionRemoveProvisionedConcurrency,
		optimize.ActionCreateVPCEndpoint,
	}
	execs := NewExecutors()
	require.Len(t, execs, len(want))
	seen := map[optimize.ActionType]bool{}
	for _, ex := range execs {
		seen[ex.Action()] = true
		assert.NotEmpty(t, ex.RequiredActions())
	}
	for _, a := range want {
		assert.True(t, seen[a], "missing executor for %s", a)
	}
}

func TestExecutors_Plan_NotFoundResource(t *testing.T) {
	e := newExecEstate(t)
	exec, _ := NewExecutor(optimize.ActionStopInstance)
	session := execSession(t, e)
	_, err := exec.Plan(context.Background(), ports.ExecutionPlanInput{
		TenantID: testTenant, Resource: execResource(cloud.KindEC2Instance, "i-does-not-exist"),
		Account: cloud.AWSAccount{AccountID: e.AccountID}, Session: session,
		Recommendation: optimize.Recommendation{Action: exec.Action()},
	})
	assert.Error(t, err)
}
