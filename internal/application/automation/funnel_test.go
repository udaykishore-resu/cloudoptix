package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestFunnel_ReflectsAdvancedSavingsRecord proves Funnel is genuinely wired
// to the savings ladder this package advances: a plan that only reached
// StagePlanned should not show up as executed.
func TestFunnel_ReflectsAdvancedSavingsRecord(t *testing.T) {
	h := newHarness(t)
	h.seedSpec(t, defaultSpec())
	res := h.seedEC2(t)
	rec := h.seedRecommendation(t, res)

	_, err := h.svc.PlanExecution(ctxFor(testTenant), testTenant, rec.ID, ports.PlanOptions{RequestedBy: "dev@example.com"})
	require.NoError(t, err)

	period := core.NewPeriod(testNow.Add(-time.Hour), testNow.Add(time.Hour))
	funnel, err := h.svc.Funnel(ctxFor(testTenant), testTenant, period)
	require.NoError(t, err)
	assert.Equal(t, 1, funnel.Counts[execute.StagePlanned])
	assert.Equal(t, 0, funnel.Counts[execute.StageExecuted])
}
