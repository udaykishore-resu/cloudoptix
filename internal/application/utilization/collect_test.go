package utilization

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeSession is the minimal ports.AWSSession a test needs.
type fakeSession struct {
	account core.AccountID
	scope   cloud.RoleScope
}

func (f fakeSession) AccountID() core.AccountID { return f.account }
func (f fakeSession) Scope() cloud.RoleScope    { return f.scope }
func (f fakeSession) ExpiresAt() time.Time      { return time.Now().Add(time.Hour) }
func (f fakeSession) Config(core.Region) any    { return nil }

// fakeCollector fails every other batch (odd-numbered calls) to exercise
// per-batch failure isolation, and otherwise returns one summary per
// resource requested.
type fakeCollector struct {
	source    string
	available bool
	calls     int
	failEvery int // 0 disables failure injection
}

func (f *fakeCollector) Source() string { return f.source }
func (f *fakeCollector) Available(ctx context.Context, session ports.AWSSession) bool {
	return f.available
}
func (f *fakeCollector) Collect(ctx context.Context, in ports.MetricCollectInput) ([]ports.ResourceMetrics, error) {
	f.calls++
	if f.failEvery > 0 && f.calls%f.failEvery == 0 {
		return nil, core.NewError(core.ErrThrottled, "throttled", "cloudwatch throttled this batch")
	}
	out := make([]ports.ResourceMetrics, 0, len(in.Resources))
	for _, r := range in.Resources {
		cpu := core.SummarizeSamples([]float64{10, 20, 30}, 1)
		out = append(out, ports.ResourceMetrics{
			ResourceID: r.ID, TenantID: in.TenantID, Window: in.Window,
			CPU: &cpu, Coverage: 1, Source: f.source, CollectedAt: time.Now().UTC(),
		})
	}
	return out, nil
}

func TestCollector_BatchesAndPersists(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_test")

	resources := make([]cloud.Resource, 0, 120)
	for i := 0; i < 120; i++ {
		resources = append(resources, cloud.Resource{ID: core.NewID("res"), TenantID: tenant, Kind: cloud.KindEC2Instance})
	}

	collector := &fakeCollector{source: "cloudwatch", available: true}
	c := &Collector{Collectors: []ports.MetricCollector{collector}, Metrics: repos.Metrics, BatchSize: 50}

	ctx := core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
	window := core.PeriodOfDays(time.Now(), 7)
	res, err := c.Collect(ctx, CollectRequest{
		TenantID: tenant, Session: fakeSession{account: "111111111111"},
		Resources: resources, Window: window,
	})
	require.NoError(t, err)
	assert.Equal(t, 120, res.ResourcesRequested)
	assert.Equal(t, 120, res.ResourcesCollected)
	assert.Equal(t, 3, res.Batches) // 50 + 50 + 20
	assert.Equal(t, 0, res.BatchesFailed)

	summaries, err := repos.Metrics.LoadSummaries(ctx, tenant, nil)
	require.NoError(t, err)
	assert.Len(t, summaries, 120)
}

func TestCollector_IsolatesBatchFailures(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_test2")

	resources := make([]cloud.Resource, 0, 100)
	for i := 0; i < 100; i++ {
		resources = append(resources, cloud.Resource{ID: core.NewID("res"), TenantID: tenant, Kind: cloud.KindEC2Instance})
	}
	// Fails every other batch (calls #2 and #4 of the 4 batches).
	collector := &fakeCollector{source: "cloudwatch", available: true, failEvery: 2}
	c := &Collector{Collectors: []ports.MetricCollector{collector}, Metrics: repos.Metrics, BatchSize: 25}

	ctx := core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
	res, err := c.Collect(ctx, CollectRequest{
		TenantID: tenant, Session: fakeSession{account: "111111111111"},
		Resources: resources, Window: core.PeriodOfDays(time.Now(), 7),
	})
	require.NoError(t, err)
	assert.Equal(t, 4, res.Batches)
	assert.Equal(t, 2, res.BatchesFailed)
	assert.Equal(t, 50, res.ResourcesCollected) // two batches of 25 lost, not the whole run
	assert.Len(t, res.Errors, 2)
}

func TestCollector_NoAvailableCollectorErrors(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_test3")
	collector := &fakeCollector{source: "cloudwatch", available: false}
	c := NewCollector([]ports.MetricCollector{collector}, repos.Metrics)

	ctx := core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
	_, err := c.Collect(ctx, CollectRequest{
		TenantID: tenant, Session: fakeSession{},
		Resources: []cloud.Resource{{ID: core.NewID("res"), TenantID: tenant}},
	})
	require.Error(t, err)
}
