package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSNS struct {
	topics []snstypes.Topic
	attrs  map[string]map[string]string
}

func (f *fakeSNS) ListTopics(context.Context, *sns.ListTopicsInput, ...func(*sns.Options)) (*sns.ListTopicsOutput, error) {
	return &sns.ListTopicsOutput{Topics: f.topics}, nil
}
func (f *fakeSNS) GetTopicAttributes(_ context.Context, in *sns.GetTopicAttributesInput, _ ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	return &sns.GetTopicAttributesOutput{Attributes: f.attrs[aws.ToString(in.TopicArn)]}, nil
}
func (f *fakeSNS) ListTagsForResource(context.Context, *sns.ListTagsForResourceInput, ...func(*sns.Options)) (*sns.ListTagsForResourceOutput, error) {
	return &sns.ListTagsForResourceOutput{Tags: []snstypes.Tag{{Key: aws.String("Team"), Value: aws.String("payments")}}}, nil
}

func TestSNSDiscoverer_NormalizesTopicAttributes(t *testing.T) {
	arn := "arn:aws:sns:us-east-1:222222222222:order-events"
	f := &fakeSNS{
		topics: []snstypes.Topic{{TopicArn: aws.String(arn)}},
		attrs: map[string]map[string]string{
			arn: {"FifoTopic": "false", "DisplayName": "Order Events", "SubscriptionsConfirmed": "3", "SubscriptionsPending": "0"},
		},
	}
	d := &SNSDiscoverer{newClient: func(aws.Config) snsAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	topic := out.Resources[0]
	assert.Equal(t, "order-events", topic.NativeID)
	assert.Equal(t, "Order Events", topic.Attr("display_name", ""))
	assert.Equal(t, "3", topic.Attr("subscriptions_confirmed", ""))
	assert.Equal(t, "payments", topic.Tags["Team"])
}

func TestTopicNameFromARN(t *testing.T) {
	assert.Equal(t, "order-events", topicNameFromARN("arn:aws:sns:us-east-1:222222222222:order-events"))
}

func TestSNSDiscoverer_RequiredActions(t *testing.T) {
	d := NewSNSDiscoverer()
	assert.Equal(t, "sns", d.Service())
	assert.Contains(t, d.RequiredActions(), "sns:GetTopicAttributes")
}
