// This file discovers API Gateway v2 (HTTP and WebSocket) APIs and the
// backend each one's integrations route to. GetApis has no dedicated
// paginator in the generated SDK (its NextToken is a plain string on the
// input/output, not a token-typed field the codegen recognized), so
// pagination here is hand-rolled rather than via a NewXPaginator.
//
// An integration's target is resolved on a best-effort basis, matching
// awssim's single-target-per-API simplification but allowing for more than
// one when an API fronts more than one backend: a Lambda AWS_PROXY
// integration's IntegrationUri embeds the function's ARN, from which the
// function name is extracted and matched against a Lambda discoverer's
// native id; an HTTP_PROXY/VPC_LINK integration's IntegrationUri is an
// ELBv2 listener ARN, which shares its "loadbalancer/<type>/<name>/<id>"
// segment (renamed to "listener/...") with the load balancer's own ARN, so
// that shared prefix is what's matched against.
package discovery

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agwtypes "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type apiGatewayV2API interface {
	GetApis(ctx context.Context, in *apigatewayv2.GetApisInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.GetApisOutput, error)
	GetIntegrations(ctx context.Context, in *apigatewayv2.GetIntegrationsInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.GetIntegrationsOutput, error)
}

type APIGatewayV2Discoverer struct {
	newClient func(aws.Config) apiGatewayV2API
}

var _ ports.ResourceDiscoverer = (*APIGatewayV2Discoverer)(nil)

func NewAPIGatewayV2Discoverer() *APIGatewayV2Discoverer {
	return &APIGatewayV2Discoverer{newClient: func(cfg aws.Config) apiGatewayV2API { return apigatewayv2.NewFromConfig(cfg) }}
}

func (d *APIGatewayV2Discoverer) Service() string { return "apigateway" }
func (d *APIGatewayV2Discoverer) Kinds() []cloud.Kind {
	return []cloud.Kind{cloud.KindAPIGateway}
}
func (d *APIGatewayV2Discoverer) RequiredActions() []string {
	return []string{"apigateway:GET"}
}

func (d *APIGatewayV2Discoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	var nextToken *string
	for {
		b.countCall()
		page, err := client.GetApis(ctx, &apigatewayv2.GetApisInput{NextToken: nextToken})
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("apigatewayv2: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "apigatewayv2", "GetApis", "apigateway:GET")
		}
		for _, api := range page.Items {
			d.addAPI(ctx, b, client, in, api)
		}
		if page.NextToken == nil || *page.NextToken == "" {
			break
		}
		nextToken = page.NextToken
	}
	return b.out, nil
}

func (d *APIGatewayV2Discoverer) addAPI(ctx context.Context, b *builder, client apiGatewayV2API, in ports.DiscoveryInput, api agwtypes.Api) {
	nativeID := aws.ToString(api.ApiId)
	id := b.add(resourceSpec{
		Kind: cloud.KindAPIGateway, NativeID: nativeID, ARN: core.ARN(""),
		Name: aws.ToString(api.Name), Region: in.Region, State: cloud.StateAvailable,
		Purchase: cloud.PurchaseServerless, Tags: core.Tags(api.Tags),
		Attributes: attrs("protocol_type", string(api.ProtocolType), "api_endpoint", aws.ToString(api.ApiEndpoint),
			"route_selection_expression", aws.ToString(api.RouteSelectionExpression)),
		CreatedAt: aws.ToTime(api.CreatedDate), DiscoveredBy: "aws.apigatewayv2",
	})

	var nextToken *string
	for {
		b.countCall()
		page, err := client.GetIntegrations(ctx, &apigatewayv2.GetIntegrationsInput{ApiId: api.ApiId, NextToken: nextToken})
		if err != nil {
			b.warnf("apigatewayv2: could not list integrations for %s: %v", nativeID, err)
			return
		}
		for _, integ := range page.Items {
			if toID, ok := resolveIntegrationTarget(b, integ); ok {
				b.edge(cloud.RelRoutesTo, id, toID, 1, 1.0, core.ProvenanceConfirmed)
			}
		}
		if page.NextToken == nil || *page.NextToken == "" {
			break
		}
		nextToken = page.NextToken
	}
}

func resolveIntegrationTarget(b *builder, integ agwtypes.Integration) (core.ID, bool) {
	uri := aws.ToString(integ.IntegrationUri)
	if uri == "" {
		return "", false
	}
	if fn, ok := lambdaFunctionFromIntegrationURI(uri); ok {
		if id, ok := b.existingIDByNative(fn); ok {
			return id, true
		}
	}
	if b.in.Existing != nil {
		for _, lb := range b.in.Existing.OfKinds(cloud.KindALB, cloud.KindNLB) {
			if listenerPrefix, ok := listenerPrefixFromLBArn(string(lb.ARN)); ok && strings.HasPrefix(uri, listenerPrefix) {
				return lb.ID, true
			}
		}
	}
	return "", false
}

// lambdaFunctionFromIntegrationURI extracts the function name from a Lambda
// AWS_PROXY IntegrationUri, which embeds the full invoke ARN as
// ".../functions/arn:aws:lambda:<region>:<account>:function:<name>/invocations".
func lambdaFunctionFromIntegrationURI(uri string) (string, bool) {
	const marker = ":function:"
	idx := strings.Index(uri, marker)
	if idx < 0 {
		return "", false
	}
	rest := uri[idx+len(marker):]
	if slash := strings.Index(rest, "/"); slash >= 0 {
		rest = rest[:slash]
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

// listenerPrefixFromLBArn rewrites a load balancer ARN's "loadbalancer/"
// segment to "listener/", producing the common prefix every one of that
// load balancer's listener ARNs shares.
func listenerPrefixFromLBArn(lbArn string) (string, bool) {
	const marker = ":loadbalancer/"
	idx := strings.Index(lbArn, marker)
	if idx < 0 {
		return "", false
	}
	return lbArn[:idx] + ":listener/" + lbArn[idx+len(marker):], true
}
