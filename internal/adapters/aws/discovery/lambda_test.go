package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

type fakeLambda struct {
	pages [][]lambdatypes.FunctionConfiguration
	call  int
	err   error
}

func (f *fakeLambda) ListFunctions(context.Context, *lambda.ListFunctionsInput, ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.call >= len(f.pages) {
		return &lambda.ListFunctionsOutput{}, nil
	}
	page := f.pages[f.call]
	f.call++
	out := &lambda.ListFunctionsOutput{Functions: page}
	if f.call < len(f.pages) {
		out.NextMarker = aws.String("more")
	}
	return out, nil
}
func (f *fakeLambda) ListTags(context.Context, *lambda.ListTagsInput, ...func(*lambda.Options)) (*lambda.ListTagsOutput, error) {
	return &lambda.ListTagsOutput{Tags: map[string]string{"Team": "checkout"}}, nil
}
func (f *fakeLambda) ListProvisionedConcurrencyConfigs(context.Context, *lambda.ListProvisionedConcurrencyConfigsInput, ...func(*lambda.Options)) (*lambda.ListProvisionedConcurrencyConfigsOutput, error) {
	return &lambda.ListProvisionedConcurrencyConfigsOutput{
		ProvisionedConcurrencyConfigs: []lambdatypes.ProvisionedConcurrencyConfigListItem{{AllocatedProvisionedConcurrentExecutions: aws.Int32(5)}},
	}, nil
}

func TestLambdaDiscoverer_PaginatesAndEnriches(t *testing.T) {
	f := &fakeLambda{pages: [][]lambdatypes.FunctionConfiguration{
		{{FunctionName: aws.String("checkout-handler"), MemorySize: aws.Int32(512), Timeout: aws.Int32(30),
			Runtime: lambdatypes.RuntimeNodejs20x, State: lambdatypes.StateActive,
			Architectures: []lambdatypes.Architecture{lambdatypes.ArchitectureArm64}}},
		{{FunctionName: aws.String("worker"), MemorySize: aws.Int32(256), Runtime: lambdatypes.RuntimePython313, State: lambdatypes.StateActive}},
	}}
	d := &LambdaDiscoverer{newClient: func(aws.Config) lambdaAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 2)

	fn := mustFind(t, out.Resources, "checkout-handler")
	assert.Equal(t, 512, fn.Capacity.MemoryMB)
	assert.Equal(t, 5, fn.Capacity.Concurrency)
	assert.Equal(t, "arm64", fn.Attr("architecture", ""))
	assert.Equal(t, "checkout", fn.Tags["Team"])
}

func TestLambdaDiscoverer_ThrottleTranslates(t *testing.T) {
	d := &LambdaDiscoverer{newClient: func(aws.Config) lambdaAPI {
		return &fakeLambda{err: &smithy.GenericAPIError{Code: "TooManyRequestsException", Message: "slow down"}}
	}}
	_, err := d.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrThrottled)
}

func TestLambdaDiscoverer_RequiredActions(t *testing.T) {
	d := NewLambdaDiscoverer()
	assert.Equal(t, "lambda", d.Service())
	assert.Contains(t, d.RequiredActions(), "lambda:ListFunctions")
}
