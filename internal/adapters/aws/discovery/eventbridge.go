// This file discovers EventBridge event buses. cloud.Kind has no dedicated
// kind for an EventBridge rule (only KindEventBus), so rules are not
// emitted as their own resources; instead each bus is enriched with
// rule_count/enabled_rule_count attributes from a paginated ListRules call
// scoped to that bus, giving a cost/security engine enough signal ("this
// bus has zero rules, so nothing is actually wired to it") without
// modeling a resource kind the domain doesn't have.
//
// Like apigatewayv2.go, ListEventBuses and ListRules have no generated
// paginator (their NextToken fields aren't codegen-recognized token
// fields), so pagination is hand-rolled here.
package discovery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type eventBridgeAPI interface {
	ListEventBuses(ctx context.Context, in *eventbridge.ListEventBusesInput, optFns ...func(*eventbridge.Options)) (*eventbridge.ListEventBusesOutput, error)
	ListRules(ctx context.Context, in *eventbridge.ListRulesInput, optFns ...func(*eventbridge.Options)) (*eventbridge.ListRulesOutput, error)
	ListTagsForResource(ctx context.Context, in *eventbridge.ListTagsForResourceInput, optFns ...func(*eventbridge.Options)) (*eventbridge.ListTagsForResourceOutput, error)
}

type EventBridgeDiscoverer struct {
	newClient func(aws.Config) eventBridgeAPI
}

var _ ports.ResourceDiscoverer = (*EventBridgeDiscoverer)(nil)

func NewEventBridgeDiscoverer() *EventBridgeDiscoverer {
	return &EventBridgeDiscoverer{newClient: func(cfg aws.Config) eventBridgeAPI { return eventbridge.NewFromConfig(cfg) }}
}

func (d *EventBridgeDiscoverer) Service() string     { return "events" }
func (d *EventBridgeDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindEventBus} }
func (d *EventBridgeDiscoverer) RequiredActions() []string {
	return []string{"events:ListEventBuses", "events:ListRules", "events:ListTagsForResource"}
}

func (d *EventBridgeDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
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
		page, err := client.ListEventBuses(ctx, &eventbridge.ListEventBusesInput{NextToken: nextToken})
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("eventbridge: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "events", "ListEventBuses", "events:ListEventBuses")
		}
		for _, bus := range page.EventBuses {
			d.addEventBus(ctx, b, client, in, bus)
		}
		if page.NextToken == nil || *page.NextToken == "" {
			break
		}
		nextToken = page.NextToken
	}
	return b.out, nil
}

func (d *EventBridgeDiscoverer) addEventBus(ctx context.Context, b *builder, client eventBridgeAPI, in ports.DiscoveryInput, bus ebtypes.EventBus) {
	nativeID := aws.ToString(bus.Name)
	arn := aws.ToString(bus.Arn)

	tags := core.Tags{}
	if arn != "" {
		b.countCall()
		if resp, err := client.ListTagsForResource(ctx, &eventbridge.ListTagsForResourceInput{ResourceARN: bus.Arn}); err == nil {
			pairs := make([][2]string, 0, len(resp.Tags))
			for _, t := range resp.Tags {
				pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
			}
			tags = tagsFromKV(pairs)
		}
	}

	ruleCount, enabledCount := d.countRules(ctx, b, client, nativeID)

	b.add(resourceSpec{
		Kind: cloud.KindEventBus, NativeID: nativeID, ARN: core.ARN(arn),
		Name: nativeID, Region: in.Region, State: cloud.StateAvailable,
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("rule_count", istr(int64(ruleCount)), "enabled_rule_count", istr(int64(enabledCount))),
		CreatedAt:  aws.ToTime(bus.CreationTime), DiscoveredBy: "aws.eventbridge",
	})
}

func (d *EventBridgeDiscoverer) countRules(ctx context.Context, b *builder, client eventBridgeAPI, busName string) (total, enabled int) {
	var nextToken *string
	for {
		b.countCall()
		page, err := client.ListRules(ctx, &eventbridge.ListRulesInput{EventBusName: aws.String(busName), NextToken: nextToken})
		if err != nil {
			b.warnf("eventbridge: could not list rules for bus %s: %v", busName, err)
			return total, enabled
		}
		for _, r := range page.Rules {
			total++
			if r.State == ebtypes.RuleStateEnabled {
				enabled++
			}
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return total, enabled
		}
		nextToken = page.NextToken
	}
}
