// This file discovers DynamoDB tables. Unlike most services, DynamoDB has no
// bulk describe: ListTables returns names only, so one DescribeTable call
// per table is unavoidable — this discoverer accepts that N+1 shape rather
// than working around it, and documents it so a reviewer does not mistake it
// for an oversight.
package discovery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type dynamoDBAPI interface {
	ListTables(ctx context.Context, in *dynamodb.ListTablesInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error)
	DescribeTable(ctx context.Context, in *dynamodb.DescribeTableInput, optFns ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	ListTagsOfResource(ctx context.Context, in *dynamodb.ListTagsOfResourceInput, optFns ...func(*dynamodb.Options)) (*dynamodb.ListTagsOfResourceOutput, error)
}

type DynamoDBDiscoverer struct {
	newClient func(aws.Config) dynamoDBAPI
}

var _ ports.ResourceDiscoverer = (*DynamoDBDiscoverer)(nil)

func NewDynamoDBDiscoverer() *DynamoDBDiscoverer {
	return &DynamoDBDiscoverer{newClient: func(cfg aws.Config) dynamoDBAPI { return dynamodb.NewFromConfig(cfg) }}
}

func (d *DynamoDBDiscoverer) Service() string     { return "dynamodb" }
func (d *DynamoDBDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindDynamoDBTable} }
func (d *DynamoDBDiscoverer) RequiredActions() []string {
	return []string{"dynamodb:ListTables", "dynamodb:DescribeTable", "dynamodb:ListTagsOfResource"}
}

func (d *DynamoDBDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := dynamodb.NewListTablesPaginator(client, &dynamodb.ListTablesInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("dynamodb: not available in region %s: %v", in.Region, err)
				break
			}
			return b.out, b.wrap(err, "dynamodb", "ListTables", "dynamodb:ListTables")
		}
		for _, name := range page.TableNames {
			b.countCall()
			desc, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
			if err != nil {
				if isThrottledOrDenied(err) {
					return b.out, b.wrap(err, "dynamodb", "DescribeTable", "dynamodb:DescribeTable")
				}
				b.warnf("dynamodb: could not describe table %s: %v", name, err)
				continue
			}
			addTable(b, in, client, ctx, desc.Table)
		}
	}
	return b.out, nil
}

func addTable(b *builder, in ports.DiscoveryInput, client dynamoDBAPI, ctx context.Context, t *ddbtypes.TableDescription) {
	if t == nil {
		return
	}
	nativeID := aws.ToString(t.TableName)
	tags := core.Tags{}
	// Best-effort: a missing dynamodb:ListTagsOfResource permission should
	// not fail discovery of the table itself, only leave it untagged.
	if tagOut, err := client.ListTagsOfResource(ctx, &dynamodb.ListTagsOfResourceInput{ResourceArn: t.TableArn}); err == nil {
		pairs := make([][2]string, 0, len(tagOut.Tags))
		for _, tg := range tagOut.Tags {
			pairs = append(pairs, [2]string{aws.ToString(tg.Key), aws.ToString(tg.Value)})
		}
		tags = tagsFromKV(pairs)
	}

	billingMode := "on_demand"
	purchase := cloud.PurchaseServerless
	var rcu, wcu float64
	if t.BillingModeSummary != nil && t.BillingModeSummary.BillingMode == ddbtypes.BillingModeProvisioned {
		billingMode = "provisioned"
		purchase = cloud.PurchaseReserved
	}
	if t.ProvisionedThroughput != nil {
		rcu = float64(aws.ToInt64(t.ProvisionedThroughput.ReadCapacityUnits))
		wcu = float64(aws.ToInt64(t.ProvisionedThroughput.WriteCapacityUnits))
	}
	b.add(resourceSpec{
		Kind: cloud.KindDynamoDBTable, NativeID: nativeID, ARN: core.ARN(aws.ToString(t.TableArn)),
		Name: nativeID, Region: in.Region, State: mapState(string(t.TableStatus)),
		Capacity: cloud.Capacity{
			StorageGiB:  float64(aws.ToInt64(t.TableSizeBytes)) / (1024 * 1024 * 1024),
			ObjectCount: aws.ToInt64(t.ItemCount), ReadCapacityWCU: rcu, WriteCapacityRCU: wcu,
		},
		Purchase: purchase, Tags: tags,
		Attributes: attrs("billing_mode", billingMode, "gsi_count", istr(int64(len(t.GlobalSecondaryIndexes))),
			"stream_enabled", boolStr(t.StreamSpecification != nil && aws.ToBool(t.StreamSpecification.StreamEnabled))),
		CreatedAt: aws.ToTime(t.CreationDateTime), DiscoveredBy: "aws.dynamodb",
	})
}
