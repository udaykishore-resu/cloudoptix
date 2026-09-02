// This file discovers SQS queues. ListQueues returns queue URLs only — no
// name, ARN, or attributes — so every queue costs one GetQueueAttributes
// call (requesting AttributeNameAll to get everything in a single
// round-trip rather than naming each attribute) plus one ListQueueTags
// call, an N+1 pattern like dynamodb.go and eks.go, accepted for the same
// reason: SQS has no bulk describe.
package discovery

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type sqsAPI interface {
	GetQueueAttributes(ctx context.Context, in *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
	ListQueues(ctx context.Context, in *sqs.ListQueuesInput, optFns ...func(*sqs.Options)) (*sqs.ListQueuesOutput, error)
	ListQueueTags(ctx context.Context, in *sqs.ListQueueTagsInput, optFns ...func(*sqs.Options)) (*sqs.ListQueueTagsOutput, error)
}

type SQSDiscoverer struct {
	newClient func(aws.Config) sqsAPI
}

var _ ports.ResourceDiscoverer = (*SQSDiscoverer)(nil)

func NewSQSDiscoverer() *SQSDiscoverer {
	return &SQSDiscoverer{newClient: func(cfg aws.Config) sqsAPI { return sqs.NewFromConfig(cfg) }}
}

func (d *SQSDiscoverer) Service() string     { return "sqs" }
func (d *SQSDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindSQSQueue} }
func (d *SQSDiscoverer) RequiredActions() []string {
	return []string{"sqs:ListQueues", "sqs:GetQueueAttributes", "sqs:ListQueueTags"}
}

func (d *SQSDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := sqs.NewListQueuesPaginator(client, &sqs.ListQueuesInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("sqs: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "sqs", "ListQueues", "sqs:ListQueues")
		}
		for _, url := range page.QueueUrls {
			d.addQueue(ctx, b, client, in, url)
		}
	}
	return b.out, nil
}

func (d *SQSDiscoverer) addQueue(ctx context.Context, b *builder, client sqsAPI, in ports.DiscoveryInput, url string) {
	b.countCall()
	attrResp, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameAll},
	})
	if err != nil {
		if isThrottledOrDenied(err) {
			b.warnf("sqs: could not read attributes for %s: %v (systemic, skipping remaining queues this pass)", url, err)
			return
		}
		b.warnf("sqs: could not read attributes for %s: %v", url, err)
		return
	}
	a := attrResp.Attributes
	nativeID := queueNameFromURL(url)
	arn := a["QueueArn"]

	tags := core.Tags{}
	b.countCall()
	if tagResp, err := client.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: aws.String(url)}); err == nil {
		tags = core.Tags(tagResp.Tags)
	}

	createdAt := time.Time{}
	if secs, err := strconv.ParseInt(a["CreatedTimestamp"], 10, 64); err == nil {
		createdAt = time.Unix(secs, 0).UTC()
	}
	visibilityTimeout, _ := strconv.Atoi(a["VisibilityTimeout"])

	b.add(resourceSpec{
		Kind: cloud.KindSQSQueue, NativeID: nativeID, ARN: core.ARN(arn),
		Name: nativeID, Region: in.Region, State: cloud.StateAvailable,
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("fifo_queue", a["FifoQueue"], "approximate_number_of_messages", a["ApproximateNumberOfMessages"],
			"approximate_number_of_messages_not_visible", a["ApproximateNumberOfMessagesNotVisible"],
			"visibility_timeout_seconds", istr(int64(visibilityTimeout)),
			"has_redrive_policy", boolStr(a["RedrivePolicy"] != "")),
		CreatedAt: createdAt, DiscoveredBy: "aws.sqs",
	})
}

// queueNameFromURL extracts the queue name from an SQS queue URL
// (https://sqs.<region>.amazonaws.com/<account>/<name>), which is the
// closest thing SQS has to a stable native id short of the full ARN.
func queueNameFromURL(url string) string {
	if idx := strings.LastIndex(url, "/"); idx >= 0 {
		return url[idx+1:]
	}
	return url
}
