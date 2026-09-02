package utilization

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// DefaultBatchSize bounds how many resources go into one MetricCollector
// call. CloudWatch bills GetMetricData per metric per query and caps how many
// metrics a single request may ask for; a single unbounded call for a
// 10,000-instance estate would either be rejected outright or dominate the
// account's CloudWatch API quota for every other caller, so the collector
// always chunks the resource list before it ever reaches the adapter.
const DefaultBatchSize = 50

// Collector drives one or more ports.MetricCollector implementations,
// batching requests and persisting the resulting summaries. Batch failures
// are isolated: a throttled batch is recorded and skipped rather than
// aborting collection for every resource behind it in the list.
type Collector struct {
	// Collectors is tried in preference order; the first one whose
	// Available reports true for the session is used for the whole request.
	Collectors []ports.MetricCollector
	Metrics    ports.MetricRepository
	BatchSize  int
}

// NewCollector builds a Collector with the platform default batch size.
func NewCollector(collectors []ports.MetricCollector, metrics ports.MetricRepository) *Collector {
	return &Collector{Collectors: collectors, Metrics: metrics, BatchSize: DefaultBatchSize}
}

// CollectRequest asks for utilization telemetry over one resource set.
type CollectRequest struct {
	TenantID    core.TenantID
	Session     ports.AWSSession
	Region      core.Region
	Resources   []cloud.Resource
	Window      core.Period
	StepSeconds int
}

// CollectResult reports one collection pass.
type CollectResult struct {
	Source             string   `json:"source"`
	ResourcesRequested int      `json:"resources_requested"`
	ResourcesCollected int      `json:"resources_collected"`
	Batches            int      `json:"batches"`
	BatchesFailed      int      `json:"batches_failed"`
	Errors             []string `json:"errors,omitempty"`
	DurationMS         int64    `json:"duration_ms"`
}

// Collect gathers utilisation summaries for the requested resources and
// persists them. It never returns an error for a partial failure — only when
// no collector at all is usable for the session — because a partial
// utilization refresh is still useful and the caller (discovery, a scheduled
// refresh) should be able to report "collected 8,400 of 8,500 resources"
// rather than treat one bad batch as a hard failure of the whole run.
func (c *Collector) Collect(ctx context.Context, req CollectRequest) (CollectResult, error) {
	began := time.Now()
	res := CollectResult{ResourcesRequested: len(req.Resources)}
	if len(req.Resources) == 0 {
		return res, nil
	}

	var collector ports.MetricCollector
	for _, mc := range c.Collectors {
		if mc.Available(ctx, req.Session) {
			collector = mc
			break
		}
	}
	if collector == nil {
		return res, core.NewError(core.ErrUnavailable, "no_metric_source",
			"no metric collector is available for this session; utilisation-dependent rules will run without telemetry")
	}
	res.Source = collector.Source()

	batchSize := c.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	for start := 0; start < len(req.Resources); start += batchSize {
		if err := ctx.Err(); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("collection cancelled after %d/%d resources: %v", start, len(req.Resources), err))
			break
		}
		end := start + batchSize
		if end > len(req.Resources) {
			end = len(req.Resources)
		}
		batch := req.Resources[start:end]
		res.Batches++

		summaries, err := collector.Collect(ctx, ports.MetricCollectInput{
			TenantID: req.TenantID, Session: req.Session, Region: req.Region,
			Resources: batch, Window: req.Window, StepSeconds: req.StepSeconds,
		})
		if err != nil {
			res.BatchesFailed++
			res.Errors = append(res.Errors, fmt.Sprintf("resources %d-%d: %v", start, end, err))
			continue
		}
		if len(summaries) == 0 {
			continue
		}
		if err := c.Metrics.SaveSummaries(ctx, req.TenantID, summaries); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("persisting resources %d-%d: %v", start, end, err))
			continue
		}
		res.ResourcesCollected += len(summaries)
	}
	res.DurationMS = time.Since(began).Milliseconds()
	return res, nil
}
