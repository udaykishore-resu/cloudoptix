// Package costing implements ports.CostIngestor against real AWS billing
// sources, plus a Price List API wrapper used to refresh the embedded
// price book internal/adapters/pricing implements ports.PricingCatalog
// from (see pricing.go's doc comment for that refresh procedure — this
// package does not implement ports.PricingCatalog itself).
//
// Two CostIngestor implementations live here, ranked by fidelity: the Cost
// Explorer API (costexplorer.go), which is always available to the payer
// account and answers in seconds but only at the SERVICE/USAGE_TYPE
// dimension (Cost Explorer allows at most two GroupBy dimensions per call,
// so per-resource attribution is not possible through this API at all —
// AWS's own limitation, not a shortcut taken here); and the Cost & Usage
// Report (cur.go), a per-resource-hour CSV AWS drops in S3, which is the
// preferred source whenever CostIngestInput.ResourceLevel is requested and
// the account has one configured. pricing.go wraps the Price List API for
// the embedded price book's documented refresh procedure.
//
// Every ingestor here stores amortized cost as the default basis it
// requests (see cost.AmortizationBasis's own doc comment for why), and
// every GetCostAndUsage call is cached in-process per unique request shape:
// Cost Explorer bills per API call, so a caller that asks for the same
// account/period/granularity/basis twice in one process lifetime — the cost
// engine re-rendering a dashboard, a retry after a transient network error —
// must not pay for it twice.
//
// Traceability: REQ-COST-001..008, SPEC-COST-001.
package costing

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	awssts "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/sts"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// costExplorerRegion is where every Cost Explorer call is signed against.
// CE is a global service with a single endpoint in us-east-1 regardless of
// which regions the account's resources actually live in.
const costExplorerRegion = core.Region("us-east-1")

// dateLayout is the YYYY-MM-DD format Cost Explorer's DAILY/MONTHLY
// TimePeriod values use. HOURLY granularity instead requires a full
// timestamp, handled separately in ceDateLayout.
const dateLayout = "2006-01-02"

type costExplorerAPI interface {
	GetCostAndUsage(ctx context.Context, in *costexplorer.GetCostAndUsageInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error)
}

// ceCacheKey identifies one unique GetCostAndUsage request shape, used to
// dedupe repeat Fetch calls within this ingestor's lifetime. ResourceLevel
// is part of the key even though this ingestor cannot honor it (see the
// package doc comment) so that a caller inspecting the returned records'
// lack of ResourceID is not confused by a stale cache entry from a
// differently-scoped request.
type ceCacheKey struct {
	account       core.AccountID
	start, end    int64
	granularity   cost.Granularity
	basis         cost.AmortizationBasis
	resourceLevel bool
}

// CostExplorerIngestor implements ports.CostIngestor over the Cost Explorer
// API.
type CostExplorerIngestor struct {
	newClient func(aws.Config) costExplorerAPI
	mu        sync.Mutex
	cache     map[ceCacheKey][]cost.Record
}

var _ ports.CostIngestor = (*CostExplorerIngestor)(nil)

// NewCostExplorerIngestor builds the Cost Explorer ingestor.
func NewCostExplorerIngestor() *CostExplorerIngestor {
	return &CostExplorerIngestor{
		newClient: func(cfg aws.Config) costExplorerAPI { return costexplorer.NewFromConfig(cfg) },
		cache:     map[ceCacheKey][]cost.Record{},
	}
}

func (c *CostExplorerIngestor) Source() string { return "cost_explorer" }

// Available probes Cost Explorer with the cheapest possible real call — one
// day, one metric, no grouping — rather than assuming success, because Cost
// Explorer must be explicitly enabled in the Billing console before its API
// answers at all; an account that has never opened Cost Explorer returns a
// DataUnavailableException, not zero results.
func (c *CostExplorerIngestor) Available(ctx context.Context, session ports.AWSSession, account cloud.AWSAccount) bool {
	if session == nil {
		return false
	}
	cfg, err := awssts.FromSession(session, costExplorerRegion)
	if err != nil {
		return false
	}
	client := c.newClient(cfg)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	end := time.Now().UTC()
	start := end.AddDate(0, 0, -1)
	_, err = client.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
		Granularity: cetypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
		TimePeriod: &cetypes.DateInterval{
			Start: aws.String(start.Format(dateLayout)), End: aws.String(end.Format(dateLayout)),
		},
	})
	return err == nil
}

// Fetch returns cost.Record lines grouped by SERVICE and USAGE_TYPE (Cost
// Explorer's own two-dimension GroupBy ceiling — see the package doc
// comment), paginating via NextPageToken and caching the assembled result
// per unique request shape.
func (c *CostExplorerIngestor) Fetch(ctx context.Context, in ports.CostIngestInput) ([]cost.Record, error) {
	basis := in.Basis
	if basis == "" {
		basis = cost.BasisAmortized
	}
	granularity := in.Granularity
	if granularity == "" {
		granularity = cost.GranularityDaily
	}

	key := ceCacheKey{
		account: in.Account.AccountID, start: in.Period.Start.Unix(), end: in.Period.End.Unix(),
		granularity: granularity, basis: basis, resourceLevel: in.ResourceLevel,
	}
	c.mu.Lock()
	if cached, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	cfg, err := awssts.FromSession(in.Session, costExplorerRegion)
	if err != nil {
		return nil, err
	}
	client := c.newClient(cfg)

	ceGranularity, layout := ceGranularityAndLayout(granularity)
	metric := ceMetricName(basis)

	var records []cost.Record
	var nextToken *string
	for {
		resp, err := client.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
			Granularity: ceGranularity,
			Metrics:     []string{metric},
			TimePeriod: &cetypes.DateInterval{
				Start: aws.String(in.Period.Start.Format(layout)), End: aws.String(in.Period.End.Format(layout)),
			},
			GroupBy: []cetypes.GroupDefinition{
				{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("SERVICE")},
				{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("USAGE_TYPE")},
			},
			NextPageToken: nextToken,
		})
		if err != nil {
			return nil, awserr.Translate(err, "costexplorer", "GetCostAndUsage", "ce:GetCostAndUsage")
		}
		for _, rbt := range resp.ResultsByTime {
			period, ok := ceResultPeriod(rbt, layout)
			if !ok {
				continue
			}
			for _, g := range rbt.Groups {
				rec, ok := ceRecordFromGroup(in, period, granularity, basis, metric, g)
				if !ok {
					continue
				}
				records = append(records, rec)
			}
		}
		if resp.NextPageToken == nil || *resp.NextPageToken == "" {
			break
		}
		nextToken = resp.NextPageToken
	}

	c.mu.Lock()
	c.cache[key] = records
	c.mu.Unlock()
	return records, nil
}

func ceRecordFromGroup(in ports.CostIngestInput, period core.Period, granularity cost.Granularity, basis cost.AmortizationBasis, metric string, g cetypes.Group) (cost.Record, bool) {
	service, usageType := "", ""
	if len(g.Keys) > 0 {
		service = g.Keys[0]
	}
	if len(g.Keys) > 1 {
		usageType = g.Keys[1]
	}
	mv, ok := g.Metrics[metric]
	if !ok {
		return cost.Record{}, false
	}
	amount, err := strconv.ParseFloat(aws.ToString(mv.Amount), 64)
	if err != nil {
		return cost.Record{}, false
	}
	if amount == 0 {
		// A zero-amount line (free-tier usage, a $0 support plan) carries no
		// signal for the cost engine and would only inflate every downstream
		// aggregation's record count for nothing.
		return cost.Record{}, false
	}
	return cost.Record{
		ID: core.NewID("cost"), TenantID: in.TenantID, AccountID: in.Account.AccountID,
		Period: period, Granularity: granularity, Service: service, UsageType: usageType,
		ChargeType: cost.ChargeUsage, Basis: basis, Amount: core.USDollars(amount),
		Source: "cost_explorer", IngestedAt: time.Now().UTC(),
	}, true
}

func ceResultPeriod(rbt cetypes.ResultByTime, layout string) (core.Period, bool) {
	if rbt.TimePeriod == nil {
		return core.Period{}, false
	}
	start, err := time.Parse(layout, aws.ToString(rbt.TimePeriod.Start))
	if err != nil {
		return core.Period{}, false
	}
	end, err := time.Parse(layout, aws.ToString(rbt.TimePeriod.End))
	if err != nil {
		return core.Period{}, false
	}
	return core.NewPeriod(start, end), true
}

// ceGranularityAndLayout maps cost.Granularity onto Cost Explorer's own
// enum and the TimePeriod string format that granularity requires (HOURLY
// needs a full RFC3339 timestamp; DAILY/MONTHLY use a bare date).
func ceGranularityAndLayout(g cost.Granularity) (cetypes.Granularity, string) {
	switch g {
	case cost.GranularityHourly:
		return cetypes.GranularityHourly, time.RFC3339
	case cost.GranularityMonthly:
		return cetypes.GranularityMonthly, dateLayout
	default:
		return cetypes.GranularityDaily, dateLayout
	}
}

// ceMetricName maps cost.AmortizationBasis onto the Cost Explorer Metrics
// string that returns it.
func ceMetricName(b cost.AmortizationBasis) string {
	switch b {
	case cost.BasisUnblended:
		return "UnblendedCost"
	case cost.BasisNetAmortized:
		return "NetAmortizedCost"
	case cost.BasisBlended:
		return "BlendedCost"
	default:
		return "AmortizedCost"
	}
}
