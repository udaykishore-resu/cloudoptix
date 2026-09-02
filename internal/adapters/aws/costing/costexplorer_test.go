package costing

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type fakeCostExplorer struct {
	pages    [][]cetypes.ResultByTime
	call     int
	err      error
	requests []*costexplorer.GetCostAndUsageInput
}

func (f *fakeCostExplorer) GetCostAndUsage(_ context.Context, in *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	f.requests = append(f.requests, in)
	if f.err != nil {
		return nil, f.err
	}
	if f.call >= len(f.pages) {
		return &costexplorer.GetCostAndUsageOutput{}, nil
	}
	page := f.pages[f.call]
	f.call++
	out := &costexplorer.GetCostAndUsageOutput{ResultsByTime: page}
	if f.call < len(f.pages) {
		out.NextPageToken = aws.String("more")
	}
	return out, nil
}

func testPeriod() core.Period {
	return core.NewPeriod(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
}

func TestCostExplorerIngestor_GroupsByServiceAndUsageType(t *testing.T) {
	f := &fakeCostExplorer{pages: [][]cetypes.ResultByTime{{
		{
			TimePeriod: &cetypes.DateInterval{Start: aws.String("2026-08-01"), End: aws.String("2026-08-02")},
			Groups: []cetypes.Group{
				{Keys: []string{"Amazon Elastic Compute Cloud - Compute", "BoxUsage:m5.large"},
					Metrics: map[string]cetypes.MetricValue{"AmortizedCost": {Amount: aws.String("12.50"), Unit: aws.String("USD")}}},
				{Keys: []string{"Amazon Virtual Private Cloud", "NatGateway-Hours"},
					Metrics: map[string]cetypes.MetricValue{"AmortizedCost": {Amount: aws.String("0.00"), Unit: aws.String("USD")}}},
			},
		},
	}}}
	ing := &CostExplorerIngestor{newClient: func(aws.Config) costExplorerAPI { return f }, cache: map[ceCacheKey][]cost.Record{}}

	out, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: "tenant-1", Session: testSession(), Account: testAccount(), Period: testPeriod(),
		Granularity: cost.GranularityDaily, Basis: cost.BasisAmortized,
	})
	require.NoError(t, err)
	require.Len(t, out, 1, "the zero-amount NAT line must be dropped")

	rec := out[0]
	assert.Equal(t, "Amazon Elastic Compute Cloud - Compute", rec.Service)
	assert.Equal(t, "BoxUsage:m5.large", rec.UsageType)
	assert.Equal(t, cost.BasisAmortized, rec.Basis)
	assert.Equal(t, "cost_explorer", rec.Source)
	assert.InDelta(t, 12.50, rec.Amount.Units(), 0.001)

	require.Len(t, f.requests, 1)
	require.Len(t, f.requests[0].GroupBy, 2)
	assert.Equal(t, "SERVICE", aws.ToString(f.requests[0].GroupBy[0].Key))
	assert.Equal(t, "USAGE_TYPE", aws.ToString(f.requests[0].GroupBy[1].Key))
}

func TestCostExplorerIngestor_PaginatesAndCaches(t *testing.T) {
	page := []cetypes.ResultByTime{{
		TimePeriod: &cetypes.DateInterval{Start: aws.String("2026-08-01"), End: aws.String("2026-08-02")},
		Groups: []cetypes.Group{{Keys: []string{"Amazon S3", "TimedStorage-ByteHrs"},
			Metrics: map[string]cetypes.MetricValue{"AmortizedCost": {Amount: aws.String("3.00")}}}},
	}}
	f := &fakeCostExplorer{pages: [][]cetypes.ResultByTime{page, page}}
	ing := &CostExplorerIngestor{newClient: func(aws.Config) costExplorerAPI { return f }, cache: map[ceCacheKey][]cost.Record{}}

	in := ports.CostIngestInput{
		TenantID: "tenant-1", Session: testSession(), Account: testAccount(), Period: testPeriod(),
		Granularity: cost.GranularityDaily, Basis: cost.BasisAmortized,
	}
	out1, err := ing.Fetch(context.Background(), in)
	require.NoError(t, err)
	assert.Len(t, out1, 2, "two pages of one record each")
	assert.Equal(t, 2, f.call, "paginator must have followed NextPageToken")

	callsBefore := len(f.requests)
	out2, err := ing.Fetch(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, out1, out2)
	assert.Len(t, f.requests, callsBefore, "an identical request must be served from cache, not re-fetched")
}

func TestCostExplorerIngestor_ThrottleTranslates(t *testing.T) {
	ing := &CostExplorerIngestor{
		newClient: func(aws.Config) costExplorerAPI {
			return &fakeCostExplorer{err: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}}
		},
		cache: map[ceCacheKey][]cost.Record{},
	}
	_, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: "tenant-1", Session: testSession(), Account: testAccount(), Period: testPeriod(),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrThrottled)
}

func TestCostExplorerIngestor_Available(t *testing.T) {
	f := &fakeCostExplorer{pages: [][]cetypes.ResultByTime{{}}}
	ing := &CostExplorerIngestor{newClient: func(aws.Config) costExplorerAPI { return f }, cache: map[ceCacheKey][]cost.Record{}}
	assert.True(t, ing.Available(context.Background(), testSession(), testAccount()))
}

func TestCostExplorerIngestor_Source(t *testing.T) {
	assert.Equal(t, "cost_explorer", NewCostExplorerIngestor().Source())
}
