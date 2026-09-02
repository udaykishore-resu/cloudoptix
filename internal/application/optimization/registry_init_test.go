package optimization

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestNewDefaultRegistry_LoadsAllShippedRules is the wiring smoke test: the
// YAML rule pack and the Go registration list in registry_init.go must name
// exactly the same set of rules, or Register panics at construction time.
func TestNewDefaultRegistry_LoadsAllShippedRules(t *testing.T) {
	reg, err := NewDefaultRegistry(slog.New(slog.NewTextHandler(discardWriter{}, nil)))
	require.NoError(t, err)
	rules := reg.Rules()
	assert.Len(t, rules, 48, "every YAML-declared rule must have a registered Go implementation, and vice versa")

	seen := map[core.ID]bool{}
	for _, r := range rules {
		id := core.ID(r.ID())
		assert.False(t, seen[id], "duplicate rule ID: %s", r.ID())
		seen[id] = true
		info := r.Info()
		assert.NotEmpty(t, info.Name, "rule %s must declare a name", r.ID())
		assert.NotEmpty(t, info.Kinds, "rule %s must declare at least one kind", r.ID())
	}
}

// TestRegistry_EvaluateEndToEnd exercises the full pipeline — real YAML
// thresholds, real pricing catalog, every registered rule — against a small,
// deliberately under-provisioned synthetic estate, and checks the run
// produces at least one finding without panicking or rejecting every
// finding for a validation defect.
func TestRegistry_EvaluateEndToEnd(t *testing.T) {
	reg, err := NewDefaultRegistry(slog.New(slog.NewTextHandler(discardWriter{}, nil)))
	require.NoError(t, err)

	idle := mkResource(cloud.KindEC2Instance, "m5.2xlarge")
	idle.Name = "idle-box"

	unattachedVol := mkResource(cloud.KindEBSVolume, "gp2")
	unattachedVol.State = cloud.StateAvailable
	unattachedVol.Capacity.StorageGiB = 200
	unattachedVol.FirstSeenAt = testNow.Add(-30 * 24 * time.Hour)
	unattachedVol.MonthlyCost = core.USDollars(20)

	inv := cloud.NewInventory([]cloud.Resource{idle, unattachedVol})
	topo := cloud.NewTopology(nil)

	metrics := map[core.ID]ports.ResourceMetrics{
		idle.ID: {
			ResourceID: idle.ID,
			CPU:        pct(3, 5, 6, 3),
			Memory:     pct(5, 8, 9, 5),
			Coverage:   1.0,
			Window:     core.Period{Start: testNow.Add(-21 * 24 * time.Hour), End: testNow},
		},
	}

	ctx := testEvalContext(inv, topo, metrics, testSpec())
	ctx.Thresholds = reg // exercise the real YAML-backed thresholds, not the empty test registry

	findings, diag := reg.Evaluate(context.Background(), ctx)
	assert.Equal(t, 0, diag.FindingsRejected, "every finding this run produces must satisfy Finding.Validate")
	assert.Greater(t, len(findings), 0, "an idle oversized instance and a long-unattached volume must produce at least one finding")

	for _, f := range findings {
		assert.NotEmpty(t, f.Evidence, "finding %s from rule %s has no evidence", f.ID, f.RuleID)
		assert.False(t, f.EstimatedMonthlySaving.GreaterThan(f.CurrentMonthlyCost), "finding %s: saving exceeds cost", f.ID)
	}
}
