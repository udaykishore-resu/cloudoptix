// This file implements resize_lambda_memory and remove_provisioned_concurrency.
//
// remove_provisioned_concurrency targets one (function, qualifier) pair —
// provisioned concurrency is configured per alias or published version, not
// per function — and discovery has no place to carry a qualifier separately
// from NativeID, so this executor adopts the explicit convention
// "functionName:qualifier" for NativeID on this one action, documented here
// rather than silently assumed; splitFunctionQualifier is the one place
// that convention is parsed.
package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

type lambdaAPI interface {
	GetFunctionConfiguration(ctx context.Context, in *lambda.GetFunctionConfigurationInput, optFns ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error)
	UpdateFunctionConfiguration(ctx context.Context, in *lambda.UpdateFunctionConfigurationInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionConfigurationOutput, error)
	GetProvisionedConcurrencyConfig(ctx context.Context, in *lambda.GetProvisionedConcurrencyConfigInput, optFns ...func(*lambda.Options)) (*lambda.GetProvisionedConcurrencyConfigOutput, error)
	PutProvisionedConcurrencyConfig(ctx context.Context, in *lambda.PutProvisionedConcurrencyConfigInput, optFns ...func(*lambda.Options)) (*lambda.PutProvisionedConcurrencyConfigOutput, error)
	DeleteProvisionedConcurrencyConfig(ctx context.Context, in *lambda.DeleteProvisionedConcurrencyConfigInput, optFns ...func(*lambda.Options)) (*lambda.DeleteProvisionedConcurrencyConfigOutput, error)
}

func newLambdaClient(cfg any) lambdaAPI { return lambda.NewFromConfig(cfg.(aws.Config)) }

func isLambdaNotFound(err error) bool {
	apiErr, ok := awserr.APIErrorOf(err)
	return ok && apiErr.ErrorCode() == "ResourceNotFoundException"
}

const lambdaWaitTimeout = 3 * time.Minute

// ---- resize_lambda_memory -----------------------------------------------

func captureFunction(ctx context.Context, c lambdaAPI, nativeID string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
	out, err := c.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(nativeID)})
	if err != nil {
		if isLambdaNotFound(err) {
			return nil, false, nil
		}
		return nil, false, awserr.Translate(err, "lambda", "GetFunctionConfiguration", "lambda:GetFunctionConfiguration")
	}
	return map[string]any{"memory_mb": int(aws.ToInt32(out.MemorySize)), "state": string(out.State)}, true, nil
}

var resizeLambdaMemorySpec = spec[lambdaAPI]{
	action: optimize.ActionResizeLambdaMemory, kind: cloud.KindLambdaFunction,
	awsAction: "lambda:UpdateFunctionConfiguration", titleFmt: "resize %s to the recommended memory allocation",
	requiredActions:  []string{"lambda:GetFunctionConfiguration", "lambda:UpdateFunctionConfiguration"},
	rollbackFeasible: true, dataLossRisk: core.RiskLow,
	captureState: captureFunction,
	extraPrecondition: func(current map[string]any) error {
		if current["state"] == string(lambdatypes.StateFailed) {
			return core.Invalid("resize_lambda_memory: function is in a failed state")
		}
		return nil
	},
	isApplied: func(current, params map[string]any) bool {
		want, ok := paramInt(params, "memory_mb")
		return ok && want == current["memory_mb"]
	},
	mutate: func(ctx context.Context, c lambdaAPI, nativeID string, params map[string]any, _ core.Region) (map[string]any, error) {
		target, ok := paramInt(params, "memory_mb")
		if !ok || target <= 0 {
			return nil, core.Invalid("resize_lambda_memory: missing memory_mb parameter")
		}
		return lambdaSetMemory(ctx, c, nativeID, target)
	},
	restore: func(ctx context.Context, c lambdaAPI, nativeID string, before map[string]any, _ core.Region) error {
		want, _ := before["memory_mb"].(int)
		if want <= 0 {
			return core.Invalid("resize_lambda_memory: rollback snapshot has no memory_mb")
		}
		_, err := lambdaSetMemory(ctx, c, nativeID, want)
		return err
	},
}

func lambdaSetMemory(ctx context.Context, c lambdaAPI, nativeID string, memoryMB int) (map[string]any, error) {
	_, err := c.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(nativeID), MemorySize: aws.Int32(int32(memoryMB)),
	})
	if err != nil {
		return nil, awserr.Translate(err, "lambda", "UpdateFunctionConfiguration", "lambda:UpdateFunctionConfiguration")
	}
	wctx, cancel := ctxWithTimeout(ctx, lambdaWaitTimeout)
	defer cancel()
	if err := lambda.NewFunctionUpdatedWaiter(c).Wait(wctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(nativeID)}, lambdaWaitTimeout); err != nil {
		return nil, fmt.Errorf("aws executor: waiting for %s's configuration update to finish: %w", nativeID, err)
	}
	return map[string]any{"memory_mb": memoryMB}, nil
}

// ---- remove_provisioned_concurrency --------------------------------------

// splitFunctionQualifier parses this action's "functionName:qualifier"
// NativeID convention (see this file's package doc comment).
func splitFunctionQualifier(nativeID string) (fn, qualifier string, ok bool) {
	i := strings.LastIndex(nativeID, ":")
	if i < 0 || i == len(nativeID)-1 {
		return "", "", false
	}
	return nativeID[:i], nativeID[i+1:], true
}

var removeProvisionedConcurrencySpec = spec[lambdaAPI]{
	action: optimize.ActionRemoveProvisionedConcurrency, kind: cloud.KindLambdaFunction,
	awsAction: "lambda:DeleteProvisionedConcurrencyConfig", titleFmt: "remove unused provisioned concurrency from %s",
	requiredActions:  []string{"lambda:GetProvisionedConcurrencyConfig", "lambda:PutProvisionedConcurrencyConfig", "lambda:DeleteProvisionedConcurrencyConfig"},
	rollbackFeasible: true, dataLossRisk: core.RiskLow, deleteAction: true,
	captureState: func(ctx context.Context, c lambdaAPI, nativeID string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
		fn, qualifier, ok := splitFunctionQualifier(nativeID)
		if !ok {
			return nil, false, core.Invalid("remove_provisioned_concurrency: %q is not in \"function:qualifier\" form", nativeID)
		}
		out, err := c.GetProvisionedConcurrencyConfig(ctx, &lambda.GetProvisionedConcurrencyConfigInput{FunctionName: aws.String(fn), Qualifier: aws.String(qualifier)})
		if err != nil {
			if isLambdaNotFound(err) {
				return nil, false, nil // no provisioned concurrency configured: already the desired end state
			}
			return nil, false, awserr.Translate(err, "lambda", "GetProvisionedConcurrencyConfig", "lambda:GetProvisionedConcurrencyConfig")
		}
		return map[string]any{"requested_concurrency": int(aws.ToInt32(out.RequestedProvisionedConcurrentExecutions))}, true, nil
	},
	isApplied: func(_, _ map[string]any) bool { return false },
	mutate: func(ctx context.Context, c lambdaAPI, nativeID string, _ map[string]any, _ core.Region) (map[string]any, error) {
		fn, qualifier, ok := splitFunctionQualifier(nativeID)
		if !ok {
			return nil, core.Invalid("remove_provisioned_concurrency: %q is not in \"function:qualifier\" form", nativeID)
		}
		if _, err := c.DeleteProvisionedConcurrencyConfig(ctx, &lambda.DeleteProvisionedConcurrencyConfigInput{FunctionName: aws.String(fn), Qualifier: aws.String(qualifier)}); err != nil {
			return nil, awserr.Translate(err, "lambda", "DeleteProvisionedConcurrencyConfig", "lambda:DeleteProvisionedConcurrencyConfig")
		}
		return map[string]any{"removed": true}, nil
	},
	restore: func(ctx context.Context, c lambdaAPI, nativeID string, before map[string]any, _ core.Region) error {
		fn, qualifier, ok := splitFunctionQualifier(nativeID)
		if !ok {
			return core.Invalid("remove_provisioned_concurrency: %q is not in \"function:qualifier\" form", nativeID)
		}
		want, _ := before["requested_concurrency"].(int)
		if want <= 0 {
			return nil // there was nothing configured before: nothing to restore
		}
		_, err := c.PutProvisionedConcurrencyConfig(ctx, &lambda.PutProvisionedConcurrencyConfigInput{
			FunctionName: aws.String(fn), Qualifier: aws.String(qualifier), ProvisionedConcurrentExecutions: aws.Int32(int32(want)),
		})
		if err != nil {
			return awserr.Translate(err, "lambda", "PutProvisionedConcurrencyConfig", "lambda:PutProvisionedConcurrencyConfig")
		}
		return nil
	},
}
