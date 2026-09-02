// This file discovers Lambda functions, including their provisioned
// concurrency (a per-alias/version setting Lambda does not return from
// ListFunctions, so it costs one more call per function) and tags (also a
// separate call, since Lambda's own list/get calls return neither).
package discovery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type lambdaAPI interface {
	ListFunctions(ctx context.Context, in *lambda.ListFunctionsInput, optFns ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error)
	ListTags(ctx context.Context, in *lambda.ListTagsInput, optFns ...func(*lambda.Options)) (*lambda.ListTagsOutput, error)
	ListProvisionedConcurrencyConfigs(ctx context.Context, in *lambda.ListProvisionedConcurrencyConfigsInput, optFns ...func(*lambda.Options)) (*lambda.ListProvisionedConcurrencyConfigsOutput, error)
}

type LambdaDiscoverer struct {
	newClient func(aws.Config) lambdaAPI
}

var _ ports.ResourceDiscoverer = (*LambdaDiscoverer)(nil)

func NewLambdaDiscoverer() *LambdaDiscoverer {
	return &LambdaDiscoverer{newClient: func(cfg aws.Config) lambdaAPI { return lambda.NewFromConfig(cfg) }}
}

func (d *LambdaDiscoverer) Service() string     { return "lambda" }
func (d *LambdaDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindLambdaFunction} }
func (d *LambdaDiscoverer) RequiredActions() []string {
	return []string{"lambda:ListFunctions", "lambda:ListTags", "lambda:ListProvisionedConcurrencyConfigs"}
}

func (d *LambdaDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := lambda.NewListFunctionsPaginator(client, &lambda.ListFunctionsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("lambda: not available in region %s: %v", in.Region, err)
				break
			}
			return b.out, b.wrap(err, "lambda", "ListFunctions", "lambda:ListFunctions")
		}
		for _, fn := range page.Functions {
			addFunction(ctx, b, client, fn)
		}
	}
	return b.out, nil
}

func addFunction(ctx context.Context, b *builder, client lambdaAPI, fn lambdatypes.FunctionConfiguration) {
	name := aws.ToString(fn.FunctionName)
	arn := aws.ToString(fn.FunctionArn)

	tags := core.Tags{}
	b.countCall()
	if tagOut, err := client.ListTags(ctx, &lambda.ListTagsInput{Resource: aws.String(arn)}); err == nil {
		tags = tagsFromKV(kvFromMap(tagOut.Tags))
	}

	var concurrency int
	b.countCall()
	if pc, err := client.ListProvisionedConcurrencyConfigs(ctx, &lambda.ListProvisionedConcurrencyConfigsInput{FunctionName: aws.String(name)}); err == nil {
		for _, c := range pc.ProvisionedConcurrencyConfigs {
			concurrency += int(aws.ToInt32(c.AllocatedProvisionedConcurrentExecutions))
		}
	}

	arch := "x86_64"
	if len(fn.Architectures) > 0 {
		arch = string(fn.Architectures[0])
	}

	b.add(resourceSpec{
		Kind: cloud.KindLambdaFunction, NativeID: name, ARN: core.ARN(arn),
		Name: name, State: mapState(string(fn.State)), Engine: string(fn.Runtime), EngineVer: string(fn.Runtime),
		Capacity: cloud.Capacity{
			MemoryMB: int(aws.ToInt32(fn.MemorySize)), TimeoutSeconds: int(aws.ToInt32(fn.Timeout)), Concurrency: concurrency,
		},
		Purchase: cloud.PurchaseServerless, Tags: tags,
		Attributes: attrs("architecture", arch, "package_type", string(fn.PackageType),
			"handler", aws.ToString(fn.Handler), "vpc_configured", boolStr(fn.VpcConfig != nil && aws.ToString(fn.VpcConfig.VpcId) != "")),
		DiscoveredBy: "aws.lambda",
	})
}

// kvFromMap converts Lambda's map[string]string tag shape to the (key,
// value) pair slice tagsFromKV expects.
func kvFromMap(m map[string]string) [][2]string {
	pairs := make([][2]string, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, [2]string{k, v})
	}
	return pairs
}
