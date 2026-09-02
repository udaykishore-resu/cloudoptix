// This file discovers SNS topics. Like sqs.go, ListTopics returns ARNs
// only, so each topic costs one GetTopicAttributes call plus one
// ListTagsForResource call — the same N+1 pattern, for the same reason (no
// bulk describe).
package discovery

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type snsAPI interface {
	ListTopics(ctx context.Context, in *sns.ListTopicsInput, optFns ...func(*sns.Options)) (*sns.ListTopicsOutput, error)
	GetTopicAttributes(ctx context.Context, in *sns.GetTopicAttributesInput, optFns ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
	ListTagsForResource(ctx context.Context, in *sns.ListTagsForResourceInput, optFns ...func(*sns.Options)) (*sns.ListTagsForResourceOutput, error)
}

type SNSDiscoverer struct {
	newClient func(aws.Config) snsAPI
}

var _ ports.ResourceDiscoverer = (*SNSDiscoverer)(nil)

func NewSNSDiscoverer() *SNSDiscoverer {
	return &SNSDiscoverer{newClient: func(cfg aws.Config) snsAPI { return sns.NewFromConfig(cfg) }}
}

func (d *SNSDiscoverer) Service() string     { return "sns" }
func (d *SNSDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindSNSTopic} }
func (d *SNSDiscoverer) RequiredActions() []string {
	return []string{"sns:ListTopics", "sns:GetTopicAttributes", "sns:ListTagsForResource"}
}

func (d *SNSDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := sns.NewListTopicsPaginator(client, &sns.ListTopicsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("sns: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "sns", "ListTopics", "sns:ListTopics")
		}
		for _, t := range page.Topics {
			d.addTopic(ctx, b, client, in, aws.ToString(t.TopicArn))
		}
	}
	return b.out, nil
}

func (d *SNSDiscoverer) addTopic(ctx context.Context, b *builder, client snsAPI, in ports.DiscoveryInput, arn string) {
	nativeID := topicNameFromARN(arn)

	b.countCall()
	attrResp, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(arn)})
	if err != nil {
		b.warnf("sns: could not read attributes for %s: %v", nativeID, err)
		return
	}
	a := attrResp.Attributes

	tags := core.Tags{}
	b.countCall()
	if tagResp, err := client.ListTagsForResource(ctx, &sns.ListTagsForResourceInput{ResourceArn: aws.String(arn)}); err == nil {
		pairs := make([][2]string, 0, len(tagResp.Tags))
		for _, t := range tagResp.Tags {
			pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
		}
		tags = tagsFromKV(pairs)
	}

	b.add(resourceSpec{
		Kind: cloud.KindSNSTopic, NativeID: nativeID, ARN: core.ARN(arn),
		Name: nativeID, Region: in.Region, State: cloud.StateAvailable,
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("fifo_topic", a["FifoTopic"], "display_name", a["DisplayName"],
			"subscriptions_confirmed", a["SubscriptionsConfirmed"], "subscriptions_pending", a["SubscriptionsPending"]),
		DiscoveredBy: "aws.sns",
	})
}

func topicNameFromARN(arn string) string {
	if idx := strings.LastIndex(arn, ":"); idx >= 0 {
		return arn[idx+1:]
	}
	return arn
}
