package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

type fakeEventBridge struct {
	buses []ebtypes.EventBus
	rules map[string][]ebtypes.Rule
}

func (f *fakeEventBridge) ListEventBuses(context.Context, *eventbridge.ListEventBusesInput, ...func(*eventbridge.Options)) (*eventbridge.ListEventBusesOutput, error) {
	return &eventbridge.ListEventBusesOutput{EventBuses: f.buses}, nil
}
func (f *fakeEventBridge) ListRules(_ context.Context, in *eventbridge.ListRulesInput, _ ...func(*eventbridge.Options)) (*eventbridge.ListRulesOutput, error) {
	return &eventbridge.ListRulesOutput{Rules: f.rules[aws.ToString(in.EventBusName)]}, nil
}
func (f *fakeEventBridge) ListTagsForResource(context.Context, *eventbridge.ListTagsForResourceInput, ...func(*eventbridge.Options)) (*eventbridge.ListTagsForResourceOutput, error) {
	return &eventbridge.ListTagsForResourceOutput{Tags: []ebtypes.Tag{{Key: aws.String("Team"), Value: aws.String("platform")}}}, nil
}

func TestEventBridgeDiscoverer_CountsRulesPerBus(t *testing.T) {
	f := &fakeEventBridge{
		buses: []ebtypes.EventBus{{Name: aws.String("default"), Arn: aws.String("arn:aws:events:us-east-1:222222222222:event-bus/default")}},
		rules: map[string][]ebtypes.Rule{
			"default": {
				{Name: aws.String("r1"), State: ebtypes.RuleStateEnabled},
				{Name: aws.String("r2"), State: ebtypes.RuleStateDisabled},
				{Name: aws.String("r3"), State: ebtypes.RuleStateEnabled},
			},
		},
	}
	d := &EventBridgeDiscoverer{newClient: func(aws.Config) eventBridgeAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	bus := out.Resources[0]
	assert.Equal(t, cloud.KindEventBus, bus.Kind)
	assert.Equal(t, "3", bus.Attr("rule_count", ""))
	assert.Equal(t, "2", bus.Attr("enabled_rule_count", ""))
	assert.Equal(t, "platform", bus.Tags["Team"])
}

func TestEventBridgeDiscoverer_RequiredActions(t *testing.T) {
	d := NewEventBridgeDiscoverer()
	assert.Equal(t, "events", d.Service())
	assert.Contains(t, d.RequiredActions(), "events:ListRules")
}
