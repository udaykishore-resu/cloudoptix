package executor

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

type fakeLambda struct {
	fn                 *lambdatypes.State
	memoryMB           int32
	provisioned        int32 // 0 means "not configured"
	provisionedMissing bool
	calls              map[string]int
}

func newFakeLambda() *fakeLambda {
	s := lambdatypes.StateActive
	return &fakeLambda{fn: &s, memoryMB: 512, calls: map[string]int{}}
}

func (f *fakeLambda) GetFunctionConfiguration(_ context.Context, in *lambda.GetFunctionConfigurationInput, _ ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error) {
	f.calls["GetFunctionConfiguration"]++
	return &lambda.GetFunctionConfigurationOutput{
		FunctionName: in.FunctionName, MemorySize: aws.Int32(f.memoryMB), State: *f.fn,
		LastUpdateStatus: lambdatypes.LastUpdateStatusSuccessful, // FunctionUpdatedWaiter polls this field, not State
	}, nil
}

func (f *fakeLambda) UpdateFunctionConfiguration(_ context.Context, in *lambda.UpdateFunctionConfigurationInput, _ ...func(*lambda.Options)) (*lambda.UpdateFunctionConfigurationOutput, error) {
	f.calls["UpdateFunctionConfiguration"]++
	if in.MemorySize != nil {
		f.memoryMB = *in.MemorySize
	}
	return &lambda.UpdateFunctionConfigurationOutput{}, nil
}

func (f *fakeLambda) GetProvisionedConcurrencyConfig(_ context.Context, in *lambda.GetProvisionedConcurrencyConfigInput, _ ...func(*lambda.Options)) (*lambda.GetProvisionedConcurrencyConfigOutput, error) {
	f.calls["GetProvisionedConcurrencyConfig"]++
	if f.provisionedMissing || f.provisioned == 0 {
		return nil, notFoundErr("ResourceNotFoundException")
	}
	return &lambda.GetProvisionedConcurrencyConfigOutput{RequestedProvisionedConcurrentExecutions: aws.Int32(f.provisioned)}, nil
}

func (f *fakeLambda) PutProvisionedConcurrencyConfig(_ context.Context, in *lambda.PutProvisionedConcurrencyConfigInput, _ ...func(*lambda.Options)) (*lambda.PutProvisionedConcurrencyConfigOutput, error) {
	f.calls["PutProvisionedConcurrencyConfig"]++
	f.provisioned = aws.ToInt32(in.ProvisionedConcurrentExecutions)
	return &lambda.PutProvisionedConcurrencyConfigOutput{}, nil
}

func (f *fakeLambda) DeleteProvisionedConcurrencyConfig(_ context.Context, in *lambda.DeleteProvisionedConcurrencyConfigInput, _ ...func(*lambda.Options)) (*lambda.DeleteProvisionedConcurrencyConfigOutput, error) {
	f.calls["DeleteProvisionedConcurrencyConfig"]++
	f.provisioned = 0
	return &lambda.DeleteProvisionedConcurrencyConfigOutput{}, nil
}

func lambdaExecutor(sp spec[lambdaAPI], f *fakeLambda) *genericExecutor[lambdaAPI] {
	return &genericExecutor[lambdaAPI]{spec: sp, newClient: func(any) lambdaAPI { return f }}
}

func TestResizeLambdaMemory_ChangesAndRollsBack(t *testing.T) {
	f := newFakeLambda()
	f.memoryMB = 512
	ex := lambdaExecutor(resizeLambdaMemorySpec, f)
	res := testResource(cloud.KindLambdaFunction, "my-fn")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionResizeLambdaMemory, res, map[string]any{"memory_mb": 1024}))
	require.NoError(t, err)

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, 1024, out["memory_mb"])
	assert.Equal(t, int32(1024), f.memoryMB)

	out, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, true, out["idempotent"])

	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	assert.Equal(t, int32(512), f.memoryMB)
}

func TestRemoveProvisionedConcurrency_DeletesAndRestores(t *testing.T) {
	f := newFakeLambda()
	f.provisioned = 5
	ex := lambdaExecutor(removeProvisionedConcurrencySpec, f)
	res := testResource(cloud.KindLambdaFunction, "my-fn:live")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionRemoveProvisionedConcurrency, res, nil))
	require.NoError(t, err)
	assert.True(t, plan.Rollback.Feasible)

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, true, out["removed"])
	assert.Equal(t, int32(0), f.provisioned)

	// Retried against an already-removed config is idempotent via the
	// deleteAction "gone means done" path.
	out, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, true, out["already_deleted"])

	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	assert.Equal(t, int32(5), f.provisioned)
}

func TestRemoveProvisionedConcurrency_RejectsNativeIDWithoutQualifier(t *testing.T) {
	f := newFakeLambda()
	ex := lambdaExecutor(removeProvisionedConcurrencySpec, f)
	res := testResource(cloud.KindLambdaFunction, "my-fn-no-qualifier")
	_, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionRemoveProvisionedConcurrency, res, nil))
	require.Error(t, err)
}
