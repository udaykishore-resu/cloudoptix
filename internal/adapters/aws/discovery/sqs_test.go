package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSQS struct {
	urls  []string
	attrs map[string]map[string]string
}

func (f *fakeSQS) ListQueues(context.Context, *sqs.ListQueuesInput, ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error) {
	return &sqs.ListQueuesOutput{QueueUrls: f.urls}, nil
}
func (f *fakeSQS) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	return &sqs.GetQueueAttributesOutput{Attributes: f.attrs[aws.ToString(in.QueueUrl)]}, nil
}
func (f *fakeSQS) ListQueueTags(context.Context, *sqs.ListQueueTagsInput, ...func(*sqs.Options)) (*sqs.ListQueueTagsOutput, error) {
	return &sqs.ListQueueTagsOutput{Tags: map[string]string{"Environment": "prod"}}, nil
}

func TestSQSDiscoverer_NormalizesQueueAttributes(t *testing.T) {
	url := "https://sqs.us-east-1.amazonaws.com/222222222222/checkout-events"
	f := &fakeSQS{
		urls: []string{url},
		attrs: map[string]map[string]string{
			url: {
				"QueueArn": "arn:aws:sqs:us-east-1:222222222222:checkout-events", "FifoQueue": "false",
				"ApproximateNumberOfMessages": "42", "VisibilityTimeout": "30", "CreatedTimestamp": "1700000000",
				"RedrivePolicy": `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:222222222222:checkout-events-dlq","maxReceiveCount":5}`,
			},
		},
	}
	d := &SQSDiscoverer{newClient: func(aws.Config) sqsAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	q := out.Resources[0]
	assert.Equal(t, "checkout-events", q.NativeID)
	assert.Equal(t, "42", q.Attr("approximate_number_of_messages", ""))
	assert.Equal(t, "30", q.Attr("visibility_timeout_seconds", ""))
	assert.Equal(t, "true", q.Attr("has_redrive_policy", ""))
	assert.Equal(t, "prod", q.Tags["Environment"])
}

func TestQueueNameFromURL(t *testing.T) {
	assert.Equal(t, "my-queue", queueNameFromURL("https://sqs.us-east-1.amazonaws.com/222222222222/my-queue"))
}

func TestSQSDiscoverer_RequiredActions(t *testing.T) {
	d := NewSQSDiscoverer()
	assert.Equal(t, "sqs", d.Service())
	assert.Contains(t, d.RequiredActions(), "sqs:GetQueueAttributes")
}
