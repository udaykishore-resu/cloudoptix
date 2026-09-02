// This file implements ports.CostIngestor against the Estate.
//
// The design decision: a real cost ingestor turns AWS's billing export into
// cost.Record lines; this one turns the estate's own priced resources into
// the same shape, with day-by-day seasonality, jitter and one injected
// anomaly layered on top — then rescales every generated amount by a single
// factor so the period's summed records reconcile exactly to the estate's
// TotalMonthlyCost (pro-rated to the period length). That rescaling is what
// makes "realistic-looking daily variation" and "reconciles with billing"
// simultaneously true: the variation is real (two different days are never
// equal), but it never drifts the total away from the one number the rest
// of the platform already trusts.
package awssim

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// CostIngestor implements ports.CostIngestor over an Estate.
type CostIngestor struct{}

var _ ports.CostIngestor = (*CostIngestor)(nil)

// NewCostIngestor builds the simulated cost ingestor.
func NewCostIngestor() *CostIngestor { return &CostIngestor{} }

// Source names this ingestor as the port's "simulator" source.
func (c *CostIngestor) Source() string { return "simulator" }

// Available is always true for a simulated account — there is no real CUR
// or Cost Explorer availability to probe.
func (c *CostIngestor) Available(ctx context.Context, session ports.AWSSession, account cloud.AWSAccount) bool {
	return true
}

// costLineItem is one resource's contribution to the daily cost series
// before day-by-day variation and reconciliation are applied.
type costLineItem struct {
	key                           string // stable sort key (native resource ID); see buildLineItems
	region                        core.Region
	az                            string
	service, usageType, operation string
	usageUnit                     string
	tags                          core.Tags
	env                           core.Environment
	monthlyAmount                 float64 // USD, from the resource's own MonthlyCost
	variable                      bool    // traffic-driven (weekly seasonality applies) vs flat-rate
	isNATProcessed                bool    // the anomaly injection target
}

// seededSourceFor derives a per-fetch PRNG seed from the fixture's base
// seed and the requested period, so the same period always reproduces the
// same jitter and the same day chosen for the injected anomaly, while a
// different period gets different (but still reproducible) variation.
func seededSourceFor(period core.Period) int64 {
	return period.Start.Unix()*31 + period.End.Unix()
}

// Fetch generates daily cost.Record line items for the estate over the
// requested period.
func (c *CostIngestor) Fetch(ctx context.Context, in ports.CostIngestInput) ([]cost.Record, error) {
	estate, err := FromSession(in.Session, "")
	if err != nil {
		return nil, err
	}
	estate.mu.RLock()
	defer estate.mu.RUnlock()

	items := buildLineItems(estate)
	// Go map iteration order is randomized per-process, so buildLineItems'
	// output order is not itself stable across calls. The day/jitter loop
	// below consumes rng draws in items order, so an unstable order would
	// make Fetch non-deterministic even with an identical seed. Sorting by
	// the stable per-resource key first fixes that.
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
	basis := in.Basis
	if basis == "" {
		basis = cost.BasisAmortized
	}

	days := dailyBuckets(in.Period)
	if len(days) == 0 {
		return nil, nil
	}
	rng := rand.New(rand.NewSource(seededSourceFor(in.Period)))
	anomalyDayIdx := -1
	if len(days) >= 2 {
		anomalyDayIdx = int(float64(len(days)) * 0.6)
		if anomalyDayIdx >= len(days) {
			anomalyDayIdx = len(days) - 1
		}
	}

	var records []cost.Record
	var rawTotal float64
	now := time.Now().UTC()

	for dayIdx, day := range days {
		weekday := day.Weekday()
		for _, li := range items {
			base := li.monthlyAmount / core.AverageDaysPerMonth
			factor := dayFactor(li.variable, weekday, rng)
			if li.isNATProcessed && dayIdx == anomalyDayIdx {
				// The injected anomaly: a genuine NAT data-processing
				// spike, isolated to one day and one dimension, so the
				// robust z-score detector has exactly one true positive to
				// find against 29+ quiet days of the same dimension.
				factor *= natAnomalyMultiplier
			}
			amount := base * factor
			rawTotal += amount

			records = append(records, cost.Record{
				ID: core.NewID("costrec"), TenantID: in.TenantID, AccountID: in.Account.AccountID,
				Region: li.region, AZ: li.az,
				Period: core.NewPeriod(day, day.AddDate(0, 0, 1)), Granularity: cost.GranularityDaily,
				Service: li.service, UsageType: li.usageType, Operation: li.operation,
				ChargeType: cost.ChargeUsage, Basis: basis,
				Amount: core.USDollars(amount), UsageQty: amount, UsageUnit: li.usageUnit,
				Tags: li.tags, Environment: li.env, Source: "simulator", IngestedAt: now,
			})
		}
	}

	// Reconcile: scale every generated amount so the period's total matches
	// the estate's own TotalMonthlyCost pro-rated to the period length.
	target := estate.TotalMonthlyCost().Units() * (float64(len(days)) / core.AverageDaysPerMonth)
	if rawTotal > 0 {
		scale := target / rawTotal
		for i := range records {
			records[i].Amount = records[i].Amount.Scale(scale)
			records[i].UsageQty *= scale
		}
	}
	return records, nil
}

// natAnomalyMultiplier is how much larger the injected NAT-processing spike
// day is than an ordinary day for the same dimension — large enough that a
// robust z-score over a trailing window flags it without also being so
// large it dwarfs the rest of the estate's spend and defeats the point of
// "one line moved, not the whole bill."
const natAnomalyMultiplier = 4.5

// dayFactor returns the multiplier applied to one resource's daily average
// cost. Flat-rate charges (an always-on instance-hour, provisioned storage)
// get only a small jitter — real AWS billing for these is almost perfectly
// flat day to day. Traffic-driven charges get a weekend-heavier weekly cycle
// on top, which is what an e-commerce estate's request-driven spend (CDN,
// NAT processing, Lambda invocations, load balancer LCUs) actually looks
// like, plus the same jitter.
func dayFactor(variable bool, weekday time.Weekday, rng *rand.Rand) float64 {
	jitter := 1 + (rng.Float64()-0.5)*0.06 // +-3%
	if !variable {
		return jitter
	}
	seasonal := 1.0
	switch weekday {
	case time.Saturday, time.Sunday:
		seasonal = 1.22
	case time.Friday:
		seasonal = 1.08
	default:
		seasonal = 0.95
	}
	return seasonal * (1 + (rng.Float64()-0.5)*0.16) // +-8%
}

// dailyBuckets returns the UTC midnight of every day the half-open period
// covers. Both endpoints are truncated to midnight before bucketing: Start
// and End typically carry the same time-of-day (e.g. "N days before now" to
// "now"), and truncating only one of them would count a stray extra day
// whenever that shared time-of-day is not itself midnight.
func dailyBuckets(p core.Period) []time.Time {
	if p.IsZero() || !p.Start.Before(p.End) {
		return nil
	}
	start := time.Date(p.Start.Year(), p.Start.Month(), p.Start.Day(), 0, 0, 0, 0, time.UTC)
	end := time.Date(p.End.Year(), p.End.Month(), p.End.Day(), 0, 0, 0, 0, time.UTC)
	if !end.After(start) {
		end = start.AddDate(0, 0, 1)
	}
	var days []time.Time
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	return days
}

// buildLineItems flattens the estate into one (or, for NAT, two) cost
// dimensions per resource, in the Service/UsageType/Operation vocabulary
// Cost Explorer itself uses, so a rule written against real billing data
// reads the simulator's output unchanged.
func buildLineItems(e *Estate) []costLineItem {
	var items []costLineItem
	add := func(li costLineItem) {
		if li.monthlyAmount > 0 {
			items = append(items, li)
		}
	}
	envOf := func(tags core.Tags) core.Environment {
		if v, ok := tags.Get("Environment"); ok {
			return core.NormalizeEnvironment(v)
		}
		return core.EnvUnknown
	}

	for _, i := range e.EC2Instances {
		add(costLineItem{key: i.ID, region: i.Region, az: i.AZ, service: "Amazon Elastic Compute Cloud - Compute",
			usageType: fmt.Sprintf("BoxUsage:%s", i.InstanceType), operation: "RunInstances", usageUnit: "Hrs",
			tags: i.Tags, env: envOf(i.Tags), monthlyAmount: e.InstanceMonthlyCost(i).Units()})
	}
	for _, v := range e.EBSVolumes {
		add(costLineItem{key: v.ID, region: v.Region, az: v.AZ, service: "Amazon Elastic Compute Cloud - Compute",
			usageType: fmt.Sprintf("EBS:VolumeUsage.%s", v.VolumeType), operation: "CreateVolume", usageUnit: "GB-Mo",
			tags: v.Tags, env: envOf(v.Tags), monthlyAmount: e.VolumeMonthlyCost(v).Units()})
	}
	for _, s := range e.EBSSnapshots {
		add(costLineItem{key: s.ID, region: s.Region, service: "Amazon Elastic Compute Cloud - Compute",
			usageType: "EBS:SnapshotUsage", operation: "CreateSnapshot", usageUnit: "GB-Mo",
			tags: s.Tags, env: envOf(s.Tags), monthlyAmount: e.SnapshotMonthlyCost(s).Units()})
	}
	for _, ip := range e.ElasticIPs {
		add(costLineItem{key: ip.ID, region: ip.Region, service: "Amazon Elastic Compute Cloud - Compute",
			usageType: "ElasticIP:IdleAddress", operation: "AllocateAddress", usageUnit: "Hrs",
			tags: ip.Tags, env: envOf(ip.Tags), monthlyAmount: e.ElasticIPMonthlyCost(ip).Units()})
	}
	for _, a := range e.AMIs {
		add(costLineItem{key: a.ID, region: a.Region, service: "Amazon Elastic Compute Cloud - Compute",
			usageType: "EBS:SnapshotUsage", operation: "RegisterImage", usageUnit: "GB-Mo",
			tags: a.Tags, env: envOf(a.Tags), monthlyAmount: e.AMIMonthlyCost(a).Units()})
	}
	for _, r := range e.RDSInstances {
		add(costLineItem{key: r.ID, region: r.Region, az: r.AZ, service: "Amazon Relational Database Service",
			usageType: fmt.Sprintf("InstanceUsage:%s", r.InstanceClass), operation: "CreateDBInstance", usageUnit: "Hrs",
			tags: r.Tags, env: envOf(r.Tags), monthlyAmount: e.RDSInstanceMonthlyCost(r).Units()})
	}
	for _, s := range e.RDSSnapshots {
		add(costLineItem{key: s.ID, region: s.Region, service: "Amazon Relational Database Service",
			usageType: "RDS:ChargedBackupUsage", operation: "CreateDBSnapshot", usageUnit: "GB-Mo",
			tags: s.Tags, env: envOf(s.Tags), monthlyAmount: e.RDSSnapshotMonthlyCost(s).Units()})
	}
	for _, t := range e.DynamoDBTables {
		usageType, variable := "ProvisionedThroughput", false
		if t.BillingMode == "on_demand" {
			usageType, variable = "PayPerRequestThroughput", true
		}
		add(costLineItem{key: t.ID, region: t.Region, service: "Amazon DynamoDB", usageType: usageType, operation: "Query",
			usageUnit: "RequestUnits", tags: t.Tags, env: envOf(t.Tags), variable: variable,
			monthlyAmount: e.DynamoDBMonthlyCost(t).Units()})
	}
	for _, s := range e.S3Buckets {
		add(costLineItem{key: s.ID, region: s.Region, service: "Amazon Simple Storage Service",
			usageType: "TimedStorage-ByteHrs", operation: "StandardStorage", usageUnit: "GB-Mo",
			tags: s.Tags, env: envOf(s.Tags), monthlyAmount: e.S3MonthlyCost(s).Units()})
	}
	for _, f := range e.LambdaFunctions {
		add(costLineItem{key: f.ID, region: f.Region, service: "AWS Lambda", usageType: "Lambda-GB-Second", operation: "Invoke",
			usageUnit: "Seconds", tags: f.Tags, env: envOf(f.Tags), variable: true,
			monthlyAmount: e.LambdaMonthlyCost(f).Units()})
	}
	for _, s := range e.ECSServices {
		add(costLineItem{key: s.ID, region: s.Region, service: "Amazon Elastic Container Service",
			usageType: "Fargate-vCPU-Hours:perCPU", operation: "FargateTask", usageUnit: "Hrs",
			tags: s.Tags, env: envOf(s.Tags), variable: true, monthlyAmount: e.ECSServiceMonthlyCost(s).Units()})
	}
	for _, c := range e.EKSClusters {
		add(costLineItem{key: c.ID, region: c.Region, service: "Amazon Elastic Kubernetes Service",
			usageType: "AmazonEKS-Hours:perCluster", operation: "CreateCluster", usageUnit: "Hrs",
			tags: c.Tags, env: envOf(c.Tags), monthlyAmount: e.EKSClusterMonthlyCost(c).Units()})
	}
	for _, ng := range e.EKSNodeGroups {
		add(costLineItem{key: ng.ID, region: ng.Region, service: "Amazon Elastic Compute Cloud - Compute",
			usageType: fmt.Sprintf("BoxUsage:%s", ng.InstanceType), operation: "EKSNodegroup", usageUnit: "Hrs",
			tags: ng.Tags, env: envOf(ng.Tags), monthlyAmount: e.NodeGroupMonthlyCost(ng).Units()})
	}
	for _, lb := range e.LoadBalancers {
		svc := "LoadBalancing-Application"
		if lb.Kind == "network" {
			svc = "LoadBalancing-Network"
		}
		add(costLineItem{key: lb.ID, region: lb.Region, service: "Amazon Elastic Load Balancing", usageType: svc,
			operation: "LoadBalancing", usageUnit: "Hrs", tags: lb.Tags, env: envOf(lb.Tags), variable: true,
			monthlyAmount: e.LoadBalancerMonthlyCost(lb).Units()})
	}
	for _, d := range e.CloudFront {
		add(costLineItem{key: d.ID, service: "Amazon CloudFront", usageType: "DataTransfer-Out-Bytes", operation: "GetObject",
			usageUnit: "GB", tags: d.Tags, env: envOf(d.Tags), variable: true,
			monthlyAmount: e.CloudFrontMonthlyCost(d).Units()})
	}
	for _, a := range e.APIGateways {
		add(costLineItem{key: a.ID, region: a.Region, service: "Amazon API Gateway", usageType: "ApiGatewayRequest",
			operation: a.Kind, usageUnit: "Requests", tags: a.Tags, env: envOf(a.Tags), variable: true,
			monthlyAmount: e.APIGatewayMonthlyCost(a).Units()})
	}
	for _, n := range e.NATGateways {
		hours := priceOr0(e.Catalog.ServicePrice(n.Region, "nat_gateway", "hours")).Scale(core.HoursPerMonth)
		processed := priceOr0(e.Catalog.ServicePrice(n.Region, "nat_gateway", "gb_processed")).Scale(n.GBProcessedPerMonth)
		add(costLineItem{key: n.ID + "-hours", region: n.Region, az: n.AZ, service: "Amazon Virtual Private Cloud",
			usageType: "NatGateway-Hours", operation: "NatGateway", usageUnit: "Hrs",
			tags: n.Tags, env: envOf(n.Tags), monthlyAmount: hours.Units()})
		add(costLineItem{key: n.ID + "-processed", region: n.Region, az: n.AZ, service: "Amazon Virtual Private Cloud",
			usageType: "NatGateway-Bytes", operation: "NatGateway", usageUnit: "GB",
			tags: n.Tags, env: envOf(n.Tags), variable: true, isNATProcessed: true, monthlyAmount: processed.Units()})
	}
	for _, ep := range e.VPCEndpoints {
		add(costLineItem{key: ep.ID, region: ep.Region, service: "Amazon Virtual Private Cloud", usageType: "VpcEndpoint-Hours",
			operation: "VpcEndpoint", usageUnit: "Hrs", tags: ep.Tags, env: envOf(ep.Tags),
			monthlyAmount: e.VPCEndpointMonthlyCost(ep).Units()})
	}
	for _, c := range e.ElastiCacheClusters {
		add(costLineItem{key: c.ID, region: c.Region, service: "Amazon ElastiCache", usageType: fmt.Sprintf("NodeUsage:%s", c.NodeType),
			operation: "CreateCacheCluster", usageUnit: "Hrs", tags: c.Tags, env: envOf(c.Tags),
			monthlyAmount: e.ElastiCacheMonthlyCost(c).Units()})
	}
	for _, q := range e.SQSQueues {
		add(costLineItem{key: q.ID, region: q.Region, service: "Amazon Simple Queue Service", usageType: "Requests-Tier1",
			operation: "SendMessage", usageUnit: "Requests", tags: q.Tags, env: envOf(q.Tags), variable: true,
			monthlyAmount: e.SQSMonthlyCost(q).Units()})
	}
	for _, t := range e.SNSTopics {
		add(costLineItem{key: t.ID, region: t.Region, service: "Amazon Simple Notification Service", usageType: "Requests-Tier1",
			operation: "Publish", usageUnit: "Requests", tags: t.Tags, env: envOf(t.Tags), variable: true,
			monthlyAmount: e.SNSMonthlyCost(t).Units()})
	}
	for _, g := range e.LogGroups {
		add(costLineItem{key: g.ID, region: g.Region, service: "AmazonCloudWatch", usageType: "DataProcessing-Bytes",
			operation: "PutLogEvents", usageUnit: "GB", tags: g.Tags, env: envOf(g.Tags),
			monthlyAmount: e.LogGroupMonthlyCost(g).Units()})
	}
	for _, k := range e.KMSKeys {
		add(costLineItem{key: k.ID, region: k.Region, service: "AWS Key Management Service", usageType: "KMS-Keys",
			operation: "KeyStorage", usageUnit: "Mo", tags: k.Tags, env: envOf(k.Tags),
			monthlyAmount: e.KMSKeyMonthlyCost(k).Units()})
	}
	for _, s := range e.Secrets {
		add(costLineItem{key: s.ID, region: s.Region, service: "AWS Secrets Manager", usageType: "SecretUsage",
			operation: "SecretStorage", usageUnit: "Mo", tags: s.Tags, env: envOf(s.Tags),
			monthlyAmount: e.SecretMonthlyCost(s).Units()})
	}
	return items
}
