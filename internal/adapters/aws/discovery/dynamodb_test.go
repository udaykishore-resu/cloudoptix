package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

type fakeDynamoDB struct {
	pages        [][]string
	call         int
	tables       map[string]*ddbtypes.TableDescription
	listErr      error
	describeErrs map[string]error
}

func (f *fakeDynamoDB) ListTables(context.Context, *dynamodb.ListTablesInput, ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.call >= len(f.pages) {
		return &dynamodb.ListTablesOutput{}, nil
	}
	page := f.pages[f.call]
	f.call++
	out := &dynamodb.ListTablesOutput{TableNames: page}
	if f.call < len(f.pages) {
		out.LastEvaluatedTableName = aws.String("more")
	}
	return out, nil
}

func (f *fakeDynamoDB) DescribeTable(_ context.Context, in *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	name := aws.ToString(in.TableName)
	if err, ok := f.describeErrs[name]; ok {
		return nil, err
	}
	return &dynamodb.DescribeTableOutput{Table: f.tables[name]}, nil
}

func (f *fakeDynamoDB) ListTagsOfResource(context.Context, *dynamodb.ListTagsOfResourceInput, ...func(*dynamodb.Options)) (*dynamodb.ListTagsOfResourceOutput, error) {
	return &dynamodb.ListTagsOfResourceOutput{}, nil
}

func TestDynamoDBDiscoverer_PaginatesListThenDescribesEach(t *testing.T) {
	f := &fakeDynamoDB{
		pages: [][]string{{"orders"}, {"users"}},
		tables: map[string]*ddbtypes.TableDescription{
			"orders": {
				TableName: aws.String("orders"), TableArn: aws.String("arn:aws:dynamodb:us-east-1:222222222222:table/orders"),
				TableStatus: ddbtypes.TableStatusActive, ItemCount: aws.Int64(1000), TableSizeBytes: aws.Int64(1024 * 1024 * 1024),
				BillingModeSummary: &ddbtypes.BillingModeSummary{BillingMode: ddbtypes.BillingModePayPerRequest},
			},
			"users": {
				TableName: aws.String("users"), TableStatus: ddbtypes.TableStatusActive,
				BillingModeSummary:    &ddbtypes.BillingModeSummary{BillingMode: ddbtypes.BillingModeProvisioned},
				ProvisionedThroughput: &ddbtypes.ProvisionedThroughputDescription{ReadCapacityUnits: aws.Int64(5), WriteCapacityUnits: aws.Int64(5)},
			},
		},
	}
	d := &DynamoDBDiscoverer{newClient: func(aws.Config) dynamoDBAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 2)

	orders := mustFind(t, out.Resources, "orders")
	assert.Equal(t, "on_demand", orders.Attr("billing_mode", ""))
	assert.Equal(t, float64(1), orders.Capacity.StorageGiB)
	assert.Equal(t, int64(1000), orders.Capacity.ObjectCount)

	users := mustFind(t, out.Resources, "users")
	assert.Equal(t, "provisioned", users.Attr("billing_mode", ""))
	assert.Equal(t, cloud.PurchaseReserved, users.Purchase)
}

func TestDynamoDBDiscoverer_OneTableDescribeFailureIsAWarningNotFatal(t *testing.T) {
	f := &fakeDynamoDB{
		pages:  [][]string{{"orders", "broken"}},
		tables: map[string]*ddbtypes.TableDescription{"orders": {TableName: aws.String("orders"), TableStatus: ddbtypes.TableStatusActive}},
		describeErrs: map[string]error{
			"broken": &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "table deleted mid-scan"},
		},
	}
	d := &DynamoDBDiscoverer{newClient: func(aws.Config) dynamoDBAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)
	assert.NotEmpty(t, out.Warnings)
}

func TestDynamoDBDiscoverer_DescribeTableThrottleAbortsPass(t *testing.T) {
	f := &fakeDynamoDB{
		pages:        [][]string{{"orders"}},
		describeErrs: map[string]error{"orders": &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}},
	}
	d := &DynamoDBDiscoverer{newClient: func(aws.Config) dynamoDBAPI { return f }}
	_, err := d.Discover(context.Background(), discoveryInput())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrThrottled)
}

func TestDynamoDBDiscoverer_RequiredActions(t *testing.T) {
	d := NewDynamoDBDiscoverer()
	assert.Equal(t, "dynamodb", d.Service())
	assert.Contains(t, d.RequiredActions(), "dynamodb:DescribeTable")
}
