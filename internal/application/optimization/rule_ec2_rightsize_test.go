package optimization

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestEC2Rightsize_P95VsMean is the guard that catches the naive-tool failure
// mode this rule exists to avoid: a resource with a low mean but a high P95/
// P99 (a nightly batch job, a Monday-morning spike, a retry storm) must never
// be rightsized on its mean alone.
func TestEC2Rightsize_P95VsMean(t *testing.T) {
	cases := []struct {
		name       string
		cpu        *core.Percentiles
		wantOK     bool
		wantReason string
	}{
		{
			name:       "low mean but high P95/P99 must not downsize",
			cpu:        pct(10, 92, 97, 8), // mean=8% looks idle; P99=97% is anything but
			wantOK:     false,
			wantReason: "a rule that rightsized on the 8% mean here would downsize straight into the observed peak",
		},
		{
			name:       "low P95/P99 (and low mean) downsizes",
			cpu:        pct(10, 20, 25, 12),
			wantOK:     true,
			wantReason: "both percentile ceilings clear with headroom to spare",
		},
		{
			name:       "high mean but low P95/P99 still downsizes",
			cpu:        pct(35, 38, 40, 35), // mean=35% is well above the low-utilization case but still clears both ceilings
			wantOK:     true,
			wantReason: "the decision is driven by percentiles, not the mean, in both directions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := mkResource(cloud.KindEC2Instance, "m5.xlarge")
			inv := cloud.NewInventory([]cloud.Resource{r})
			metrics := map[core.ID]ports.ResourceMetrics{
				r.ID: {
					ResourceID: r.ID,
					CPU:        tc.cpu,
					Coverage:   1.0,
					Window:     core.Period{Start: testNow.Add(-14 * 24 * time.Hour), End: testNow},
				},
			}
			ctx := testEvalContext(inv, nil, metrics, testSpec())

			d := decideEC2Rightsize(ctx, r)
			assert.Equalf(t, tc.wantOK, d.ok, "%s: %s", tc.name, tc.wantReason)
			if tc.wantOK {
				require.NotEmpty(t, d.candidateType)
				assert.True(t, d.candidateHourly.LessThan(d.currentHourly))
				assert.False(t, d.monthlySaving.IsZero())
			}
		})
	}
}

// TestEC2Rightsize_InsufficientData proves the rule never treats a data gap
// as a confirmed idle signal.
func TestEC2Rightsize_InsufficientData(t *testing.T) {
	baseWindow := core.Period{Start: testNow.Add(-14 * 24 * time.Hour), End: testNow}
	shortWindow := core.Period{Start: testNow.Add(-2 * time.Hour), End: testNow}

	cases := []struct {
		name       string
		buildEntry func(id core.ID) map[core.ID]ports.ResourceMetrics
	}{
		{
			name:       "no telemetry entry at all",
			buildEntry: func(id core.ID) map[core.ID]ports.ResourceMetrics { return nil },
		},
		{
			name: "coverage below the guard",
			buildEntry: func(id core.ID) map[core.ID]ports.ResourceMetrics {
				return map[core.ID]ports.ResourceMetrics{
					id: {ResourceID: id, CPU: pct(10, 20, 25, 12), Coverage: 0.1, Window: baseWindow},
				}
			},
		},
		{
			name: "window shorter than the guard",
			buildEntry: func(id core.ID) map[core.ID]ports.ResourceMetrics {
				return map[core.ID]ports.ResourceMetrics{
					id: {ResourceID: id, CPU: pct(10, 20, 25, 12), Coverage: 1.0, Window: shortWindow},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := mkResource(cloud.KindEC2Instance, "m5.xlarge")
			inv := cloud.NewInventory([]cloud.Resource{r})
			ctx := testEvalContext(inv, nil, tc.buildEntry(r.ID), testSpec())
			d := decideEC2Rightsize(ctx, r)
			assert.False(t, d.ok, "a data gap must never read as a confirmed idle/well-behaved resource")
		})
	}
}
