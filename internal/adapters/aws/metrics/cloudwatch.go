// Package metrics implements ports.MetricCollector over CloudWatch
// GetMetricData.
//
// One design decision governs the whole file: every resource kind's metric
// set is declared once, in specsFor, as a plain list of (namespace, metric
// name, statistic, target field) tuples — not scattered across per-kind
// methods — so the complete CloudWatch surface this package touches is
// visible by reading one function, the same reason discovery's discoverers
// each declare RequiredActions() as one flat list rather than deriving it
// from scattered call sites.
//
// GetMetricData, not the older GetMetricStatistics, is used throughout:
// it is the one CloudWatch API that answers many metrics (hundreds of
// resources times several metrics each) in a single call, batched here at
// 500 queries per call — CloudWatch's own ceiling on MetricDataQueries per
// request — with each batch's own NextToken pagination followed to
// completion before moving to the next batch.
//
// Reduction always goes through core.SummarizeSamples, the same function
// every rule engine and the awssim MetricCollector already route through,
// so a rule reading a real collector's P95 and a rule reading the
// simulator's P95 mean exactly the same thing.
//
// Traceability: REQ-UTL-001..004, SPEC-UTL-002.
package metrics

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	awssts "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/sts"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// maxQueriesPerCall is CloudWatch's own ceiling on MetricDataQueries per
// GetMetricData call.
const maxQueriesPerCall = 500

// availabilityProbeRegion is where Available's permission probe is signed.
// Available's own port signature (ports.MetricCollector.Available) carries
// no region — only Collect does — so this is a coarse "can this session
// call CloudWatch at all" check, not a per-region one; us-east-1 is used
// because every AWS account has it enabled and cannot disable it.
const availabilityProbeRegion = core.Region("us-east-1")

type cloudWatchAPI interface {
	GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// Collector implements ports.MetricCollector over CloudWatch.
type Collector struct {
	newClient func(aws.Config) cloudWatchAPI
}

var _ ports.MetricCollector = (*Collector)(nil)

// NewCollector builds the CloudWatch metric collector.
func NewCollector() *Collector {
	return &Collector{newClient: func(cfg aws.Config) cloudWatchAPI { return cloudwatch.NewFromConfig(cfg) }}
}

func (c *Collector) Source() string { return "cloudwatch" }

// Available probes with a harmless, zero-cost query for a metric every EC2
// account can be asked about (a well-formed but almost certainly
// nonexistent instance id): CloudWatch does not error on a metric with no
// data, it errors on a malformed request or a permission denial, so a nil
// error here means the session really can call GetMetricData.
func (c *Collector) Available(ctx context.Context, session ports.AWSSession) bool {
	if session == nil {
		return false
	}
	cfg, err := awssts.FromSession(session, availabilityProbeRegion)
	if err != nil {
		return false
	}
	client := c.newClient(cfg)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	end := time.Now().UTC()
	start := end.Add(-time.Hour)
	_, err = client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(start), EndTime: aws.Time(end),
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("probe"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace: aws.String("AWS/EC2"), MetricName: aws.String("CPUUtilization"),
					Dimensions: []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: aws.String("i-000000000000000ff")}},
				},
				Period: aws.Int32(3600), Stat: aws.String("Average"),
			},
		}},
	})
	return err == nil
}

// Collect gathers utilisation telemetry for every requested resource whose
// kind this package knows a CloudWatch metric set for (see specsFor);
// every other kind is silently skipped, the same contract
// awssim.MetricCollector documents for the same reason — CloudWatch itself
// has no utilisation metric for, say, a VPC.
func (c *Collector) Collect(ctx context.Context, in ports.MetricCollectInput) ([]ports.ResourceMetrics, error) {
	cfg, err := awssts.FromSession(in.Session, in.Region)
	if err != nil {
		return nil, err
	}
	client := c.newClient(cfg)

	period := periodFor(in.Window, in.StepSeconds)

	var queries []cwtypes.MetricDataQuery
	targets := map[string]queryTarget{}
	for ri, r := range in.Resources {
		for si, spec := range specsFor(r, period) {
			id := queryID(ri, si)
			queries = append(queries, cwtypes.MetricDataQuery{
				Id: aws.String(id), ReturnData: aws.Bool(true),
				MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{Namespace: aws.String(spec.namespace), MetricName: aws.String(spec.metricName), Dimensions: spec.dimensions},
					Period: aws.Int32(spec.period), Stat: aws.String(spec.stat),
				},
			})
			targets[id] = queryTarget{resourceIdx: ri, field: spec.target, customKey: spec.customKey, asRate: spec.asRate, scale: spec.scale, period: spec.period}
		}
	}
	if len(queries) == 0 {
		return nil, nil
	}

	raw := make(map[string][]float64, len(queries)) // query id -> converted values
	for _, batch := range chunkQueries(queries, maxQueriesPerCall) {
		if err := c.fetchBatch(ctx, client, in.Window, batch, targets, raw); err != nil {
			return nil, err
		}
	}

	return assemble(in, raw, targets), nil
}

func (c *Collector) fetchBatch(ctx context.Context, client cloudWatchAPI, window core.Period, batch []cwtypes.MetricDataQuery, targets map[string]queryTarget, raw map[string][]float64) error {
	p := cloudwatch.NewGetMetricDataPaginator(client, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(window.Start), EndTime: aws.Time(window.End), MetricDataQueries: batch,
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return awserr.Translate(err, "cloudwatch", "GetMetricData", "cloudwatch:GetMetricData")
		}
		for _, result := range page.MetricDataResults {
			id := aws.ToString(result.Id)
			target, ok := targets[id]
			if !ok {
				continue
			}
			for _, v := range result.Values {
				converted := v
				if target.asRate && target.period > 0 {
					converted = v / float64(target.period)
				}
				if target.scale != 0 {
					converted *= target.scale
				}
				raw[id] = append(raw[id], converted)
			}
		}
	}
	return nil
}

func chunkQueries(items []cwtypes.MetricDataQuery, size int) [][]cwtypes.MetricDataQuery {
	var out [][]cwtypes.MetricDataQuery
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[i:end])
	}
	return out
}

func queryID(resourceIdx, specIdx int) string {
	return "m" + strconv.Itoa(resourceIdx) + "_" + strconv.Itoa(specIdx)
}

// periodFor picks the GetMetricData granularity. CloudWatch only retains
// sub-5-minute data for 15 days and sub-1-hour data for 63 days (see
// MetricStat.Period's own doc comment), so a period finer than the window's
// age would silently return empty buckets rather than an error; coarsening
// by window length is what MetricCollectInput.StepSeconds's doc comment
// means by "coarsens the step for long windows". A caller-requested step is
// honored only when it is no finer than that floor.
func periodFor(window core.Period, requestedStep int) int32 {
	floor := minPeriodForWindow(window)
	if requestedStep > 0 && int32(requestedStep) > floor {
		return int32(requestedStep)
	}
	return floor
}

func minPeriodForWindow(window core.Period) int32 {
	age := time.Since(window.Start)
	switch {
	case age <= 15*24*time.Hour:
		return 60
	case age <= 63*24*time.Hour:
		return 300
	default:
		return 3600
	}
}

// s3MetricPeriod overrides periodFor for S3 storage metrics specifically:
// AWS/S3 storage metrics are published once daily regardless of how recent
// the window is, so any finer period just returns at most one datapoint
// with a lot of empty buckets around it.
const s3MetricPeriod int32 = 86400

func albDimensionValue(arn core.ARN) string {
	const marker = ":loadbalancer/"
	s := string(arn)
	if idx := strings.Index(s, marker); idx >= 0 {
		return s[idx+len(marker):]
	}
	return ""
}
