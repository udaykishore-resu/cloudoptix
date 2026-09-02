package awssim

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestMetricCollector_BuildDemoEstate_ShapesMatchDeclaredProfile(t *testing.T) {
	e := BuildDemoEstate()
	out := discoverAll(t, e)
	collector := NewMetricCollector()
	session := execSession(t, e)
	window := core.NewPeriod(demoNow.AddDate(0, 0, -14), demoNow)

	byNative := map[string]cloud.Resource{}
	for _, r := range out.Resources {
		byNative[r.NativeID] = r
	}

	// Every EC2 instance the demo estate declares ProfileIdle should
	// collect a CPU series that reads back as consistently idle: low
	// stability-defeating variance and a P99 well under saturation.
	idleChecked := 0
	for id, inst := range e.EC2Instances {
		if inst.Profile != ProfileIdle || inst.State != cloud.StateRunning {
			continue
		}
		r, ok := byNative[id]
		require.True(t, ok)
		metrics, err := collector.Collect(context.Background(), ports.MetricCollectInput{
			TenantID: testTenant, Session: session, Region: r.Region, Resources: []cloud.Resource{r}, Window: window,
		})
		require.NoError(t, err)
		require.Len(t, metrics, 1)
		require.NotNil(t, metrics[0].CPU)
		assert.Less(t, metrics[0].CPU.P99, 25.0, "idle instance %s should never show a high P99 CPU", id)
		assert.Equal(t, "consistently idle", metrics[0].CPU.Label(), "instance %s", id)
		idleChecked++
	}
	assert.Greater(t, idleChecked, 0, "the demo estate should contain at least one running idle instance to check")

	steadyChecked := 0
	for id, inst := range e.EC2Instances {
		if inst.Profile != ProfileSteady || inst.State != cloud.StateRunning {
			continue
		}
		r, ok := byNative[id]
		require.True(t, ok)
		metrics, err := collector.Collect(context.Background(), ports.MetricCollectInput{
			TenantID: testTenant, Session: session, Region: r.Region, Resources: []cloud.Resource{r}, Window: window,
		})
		require.NoError(t, err)
		require.Len(t, metrics, 1)
		assert.Equal(t, "steady", metrics[0].CPU.Label(), "instance %s", id)
		steadyChecked++
	}
	assert.Greater(t, steadyChecked, 0)

	spikyChecked := 0
	for id, inst := range e.EC2Instances {
		if inst.Profile != ProfileSpiky || inst.State != cloud.StateRunning {
			continue
		}
		r, ok := byNative[id]
		require.True(t, ok)
		metrics, err := collector.Collect(context.Background(), ports.MetricCollectInput{
			TenantID: testTenant, Session: session, Region: r.Region, Resources: []cloud.Resource{r}, Window: window,
		})
		require.NoError(t, err)
		require.Len(t, metrics, 1)
		cpu := metrics[0].CPU
		assert.Greater(t, cpu.P99, 4*cpu.P50, "instance %s: a spiky series' P99 should dwarf its P50 (P50=%.1f P99=%.1f)", id, cpu.P50, cpu.P99)
		spikyChecked++
	}
	assert.Greater(t, spikyChecked, 0)

	cyclicalChecked := 0
	for id, inst := range e.EC2Instances {
		if inst.Profile != ProfileCyclical || inst.State != cloud.StateRunning {
			continue
		}
		r, ok := byNative[id]
		require.True(t, ok)
		metrics, err := collector.Collect(context.Background(), ports.MetricCollectInput{
			TenantID: testTenant, Session: session, Region: r.Region, Resources: []cloud.Resource{r}, Window: window,
		})
		require.NoError(t, err)
		require.Len(t, metrics, 1)
		assert.True(t, metrics[0].CPU.Seasonal, "instance %s: a cyclical series should be flagged seasonal", id)
		assert.NotEmpty(t, metrics[0].CPU.PeakHours, "instance %s: a cyclical series should report peak hours", id)
		cyclicalChecked++
	}
	assert.Greater(t, cyclicalChecked, 0)
}

func TestMetricCollector_Saturated(t *testing.T) {
	e := newExecEstate(t)
	e.EC2Instances["i-hot"] = &EC2Instance{
		Base:         Base{ID: "i-hot", Region: execRegion, State: cloud.StateRunning, Tags: core.Tags{}},
		InstanceType: "m5.large", Platform: "linux", Profile: ProfileSaturated, CPUBaselineP50: 92,
	}
	session := execSession(t, e)
	collector := NewMetricCollector()
	window := core.NewPeriod(demoNow.AddDate(0, 0, -7), demoNow)

	metrics, err := collector.Collect(context.Background(), ports.MetricCollectInput{
		TenantID: testTenant, Session: session, Region: execRegion,
		Resources: []cloud.Resource{{ID: core.NewID("res"), Kind: cloud.KindEC2Instance, NativeID: "i-hot", Region: execRegion}},
		Window:    window,
	})
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	cpu := metrics[0].CPU
	assert.Greater(t, cpu.P50, 80.0, "a saturated resource's median should sit near the ceiling")
	assert.Greater(t, cpu.Stability, 0.7, "a saturated resource should still be a stable plateau, not noisy")
}

func TestMetricCollector_SkipsKindsWithoutADeclaredProfile(t *testing.T) {
	e := newExecEstate(t)
	e.VPCs["vpc-1"] = &VPC{Base: Base{ID: "vpc-1", Region: execRegion, Tags: core.Tags{}}, CIDR: "10.0.0.0/16"}
	session := execSession(t, e)
	collector := NewMetricCollector()

	metrics, err := collector.Collect(context.Background(), ports.MetricCollectInput{
		TenantID: testTenant, Session: session, Region: execRegion,
		Resources: []cloud.Resource{{ID: core.NewID("res"), Kind: cloud.KindVPC, NativeID: "vpc-1", Region: execRegion}},
		Window:    core.NewPeriod(demoNow.AddDate(0, 0, -1), demoNow),
	})
	require.NoError(t, err)
	assert.Empty(t, metrics, "a kind with no declared utilisation profile should produce no metrics")
}

func TestMetricCollector_Deterministic(t *testing.T) {
	e := newExecEstate(t)
	e.EC2Instances["i-det"] = &EC2Instance{
		Base:         Base{ID: "i-det", Region: execRegion, State: cloud.StateRunning, Tags: core.Tags{}},
		InstanceType: "m5.large", Platform: "linux", Profile: ProfileCyclical, CPUBaselineP50: 40,
	}
	session := execSession(t, e)
	collector := NewMetricCollector()
	window := core.NewPeriod(demoNow.AddDate(0, 0, -10), demoNow)
	in := ports.MetricCollectInput{
		TenantID: testTenant, Session: session, Region: execRegion,
		Resources: []cloud.Resource{{ID: core.NewID("res"), Kind: cloud.KindEC2Instance, NativeID: "i-det", Region: execRegion}},
		Window:    window,
	}

	m1, err := collector.Collect(context.Background(), in)
	require.NoError(t, err)
	m2, err := collector.Collect(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, m1, 1)
	require.Len(t, m2, 1)
	assert.Equal(t, m1[0].CPU.P50, m2[0].CPU.P50)
	assert.Equal(t, m1[0].CPU.P99, m2[0].CPU.P99)
	assert.Equal(t, m1[0].CPU.PeakHours, m2[0].CPU.PeakHours)
}

func TestMetricCollector_CoverageAndSourceAreSet(t *testing.T) {
	e := newExecEstate(t)
	e.LambdaFunctions["fn-metrics"] = &LambdaFunction{
		Base: Base{ID: "fn-metrics", Region: execRegion, Tags: core.Tags{}}, MemoryMB: 512, AvgDurationMS: 250,
		InvocationsPerMonth: 100000, Architecture: "x86_64", Profile: ProfileSteady,
	}
	session := execSession(t, e)
	collector := NewMetricCollector()
	metrics, err := collector.Collect(context.Background(), ports.MetricCollectInput{
		TenantID: testTenant, Session: session, Region: execRegion,
		Resources: []cloud.Resource{{ID: core.NewID("res"), Kind: cloud.KindLambdaFunction, NativeID: "fn-metrics", Region: execRegion}},
		Window:    core.NewPeriod(demoNow.AddDate(0, 0, -3), demoNow),
	})
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.Equal(t, "simulator", metrics[0].Source)
	assert.Equal(t, 1.0, metrics[0].Coverage)
	assert.False(t, metrics[0].CollectedAt.IsZero())
	require.NotNil(t, metrics[0].Concurrency)
	require.NotNil(t, metrics[0].Requests)
	require.NotNil(t, metrics[0].LatencyP99)
	require.NotNil(t, metrics[0].ErrorRate)
	assert.True(t, metrics[0].HasSignal(0.5))
}

func TestMetricCollector_Available(t *testing.T) {
	e := newExecEstate(t)
	session := execSession(t, e)
	collector := NewMetricCollector()
	assert.True(t, collector.Available(context.Background(), session))
	assert.Equal(t, "simulator", collector.Source())
}
