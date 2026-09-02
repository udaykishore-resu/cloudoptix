package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agwtypes "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

type fakeAPIGatewayV2 struct {
	apis         []agwtypes.Api
	integrations map[string][]agwtypes.Integration
}

func (f *fakeAPIGatewayV2) GetApis(context.Context, *apigatewayv2.GetApisInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.GetApisOutput, error) {
	return &apigatewayv2.GetApisOutput{Items: f.apis}, nil
}
func (f *fakeAPIGatewayV2) GetIntegrations(_ context.Context, in *apigatewayv2.GetIntegrationsInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.GetIntegrationsOutput, error) {
	return &apigatewayv2.GetIntegrationsOutput{Items: f.integrations[aws.ToString(in.ApiId)]}, nil
}

func TestAPIGatewayV2Discoverer_RoutesToLambdaTarget(t *testing.T) {
	f := &fakeAPIGatewayV2{
		apis: []agwtypes.Api{{
			ApiId: aws.String("abc123"), Name: aws.String("checkout-api"), ProtocolType: agwtypes.ProtocolTypeHttp,
			ApiEndpoint: aws.String("https://abc123.execute-api.us-east-1.amazonaws.com"),
			Tags:        map[string]string{"Environment": "prod"},
		}},
		integrations: map[string][]agwtypes.Integration{
			"abc123": {{
				IntegrationId: aws.String("int1"), IntegrationType: agwtypes.IntegrationTypeAwsProxy,
				IntegrationUri: aws.String("arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:222222222222:function:checkout-handler/invocations"),
			}},
		},
	}
	d := &APIGatewayV2Discoverer{newClient: func(aws.Config) apiGatewayV2API { return f }}

	in := discoveryInput()
	in.Existing = cloud.NewInventory([]cloud.Resource{
		{ID: "res-fn-1", NativeID: "checkout-handler", Kind: cloud.KindLambdaFunction},
	})

	out, err := d.Discover(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	api := out.Resources[0]
	assert.Equal(t, cloud.KindAPIGateway, api.Kind)
	assert.Equal(t, "prod", api.Tags["Environment"])

	assertHasEdge(t, out.Relationships, cloud.RelRoutesTo, api.ID, "res-fn-1")
}

func TestLambdaFunctionFromIntegrationURI(t *testing.T) {
	fn, ok := lambdaFunctionFromIntegrationURI("arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:222222222222:function:my-fn/invocations")
	assert.True(t, ok)
	assert.Equal(t, "my-fn", fn)

	_, ok = lambdaFunctionFromIntegrationURI("arn:aws:elasticloadbalancing:us-east-1:222222222222:listener/app/web/abc/def")
	assert.False(t, ok)
}

func TestListenerPrefixFromLBArn(t *testing.T) {
	p, ok := listenerPrefixFromLBArn("arn:aws:elasticloadbalancing:us-east-1:222222222222:loadbalancer/app/web/abc123")
	assert.True(t, ok)
	assert.Equal(t, "arn:aws:elasticloadbalancing:us-east-1:222222222222:listener/app/web/abc123", p)
}

func TestAPIGatewayV2Discoverer_RequiredActions(t *testing.T) {
	d := NewAPIGatewayV2Discoverer()
	assert.Equal(t, "apigateway", d.Service())
	assert.NotEmpty(t, d.RequiredActions())
}
