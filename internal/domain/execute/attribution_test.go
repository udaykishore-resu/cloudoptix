package execute

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// The funnel invariant and the attribution cap exist for the same reason: the
// realized-savings figure is the one number CloudOptix puts in front of a CFO,
// and every plausible bug in the measurement path inflates it rather than
// deflating it. These tests pin both guards.
func TestAttributableSaving(t *testing.T) {
	tests := []struct {
		name             string
		predicted        core.Money
		measured         core.Money
		wantAttributed   core.Money
		wantUnattributed core.Money
		wantReason       bool
	}{
		{
			name:           "measured matches prediction",
			predicted:      core.USDollars(1000),
			measured:       core.USDollars(1000),
			wantAttributed: core.USDollars(1000), wantUnattributed: core.ZeroUSD(),
		},
		{
			name:           "measured below prediction is credited in full",
			predicted:      core.USDollars(1000),
			measured:       core.USDollars(640),
			wantAttributed: core.USDollars(640), wantUnattributed: core.ZeroUSD(),
		},
		{
			name:           "modest favourable variance is credited in full",
			predicted:      core.USDollars(1000),
			measured:       core.USDollars(1200),
			wantAttributed: core.USDollars(1200), wantUnattributed: core.ZeroUSD(),
		},
		{
			name:           "variance exactly at the tolerance is credited in full",
			predicted:      core.USDollars(1000),
			measured:       core.USDollars(1250),
			wantAttributed: core.USDollars(1250), wantUnattributed: core.ZeroUSD(),
		},
		{
			// The demo's real failure: a node-group change predicted to save
			// $10,652 measured against a $18,220 drop, because other things
			// moved in the same window. Crediting the whole drop would have
			// produced a funnel whose realized stage exceeded its executed one.
			name:             "large favourable variance is capped and reported",
			predicted:        core.USDollars(10652.16),
			measured:         core.USDollars(18220.80),
			wantAttributed:   core.USDollars(13315.20),
			wantUnattributed: core.USDollars(4905.60),
			wantReason:       true,
		},
		{
			name:           "no prediction credits the whole measurement",
			predicted:      core.ZeroUSD(),
			measured:       core.USDollars(500),
			wantAttributed: core.USDollars(500), wantUnattributed: core.ZeroUSD(),
		},
		{
			name:           "a cost increase attributes nothing",
			predicted:      core.USDollars(1000),
			measured:       core.USDollars(-300),
			wantAttributed: core.ZeroUSD(), wantUnattributed: core.ZeroUSD(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attributed, unattributed, reason := AttributableSaving(tt.predicted, tt.measured)
			assert.Equal(t, tt.wantAttributed.Micros(), attributed.Micros(), "attributed")
			assert.Equal(t, tt.wantUnattributed.Micros(), unattributed.Micros(), "unattributed")
			if tt.wantReason {
				assert.NotEmpty(t, reason, "an excess must be explained, not silently dropped")
			} else {
				assert.Empty(t, reason)
			}
			// Whatever the split, it must never manufacture money.
			if !tt.measured.IsNegative() {
				total := attributed.MustAdd(unattributed)
				assert.Equal(t, tt.measured.Micros(), total.Micros(),
					"attributed plus unattributed must equal what was measured")
			}
		})
	}
}

func TestBuildFunnelIsMonotonic(t *testing.T) {
	now := time.Now().UTC()
	period := core.PeriodOfDays(now, 30)

	// A record whose validated and realized stages claim more than it
	// executed — exactly what a mis-measured validation produces.
	rec := SavingsRecord{
		ID: core.NewID("sav"), TenantID: "t1", RecommendationID: core.NewID("rec"),
		Stage:            StageRealized,
		PotentialMonthly: core.USDollars(10652.16),
		ApprovedMonthly:  core.USDollars(10652.16),
		ExecutedMonthly:  core.USDollars(10652.16),
		ValidatedMonthly: core.USDollars(18220.80),
		RealizedMonthly:  core.USDollars(18220.80),
		CreatedAt:        now,
	}

	f := BuildFunnel("t1", period, []SavingsRecord{rec})

	stages := []core.Money{f.Potential, f.Approved, f.Planned, f.Executed, f.Validated, f.Realized}
	for i := 1; i < len(stages); i++ {
		require.Falsef(t, stages[i].GreaterThan(stages[i-1]),
			"stage %d (%s) must not exceed stage %d (%s)", i, stages[i].Format(), i-1, stages[i-1].Format())
	}
	assert.Equal(t, core.USDollars(10652.16).Micros(), f.Realized.Micros(),
		"realized is capped at what was executed")
	assert.False(t, f.OverAttributed.IsZero(),
		"the capped excess must be surfaced, not absorbed")
	assert.NotEmpty(t, f.OverAttributionNotes,
		"an over-attribution must carry an explanation an operator can act on")
	assert.LessOrEqual(t, f.PredictionAccuracy, 1.0,
		"prediction accuracy cannot exceed 1 once the funnel is monotonic")
}

func TestBuildFunnelLeavesAWellFormedFunnelAlone(t *testing.T) {
	now := time.Now().UTC()
	rec := SavingsRecord{
		ID: core.NewID("sav"), TenantID: "t1", Stage: StageRealized,
		PotentialMonthly: core.USDollars(1000),
		ApprovedMonthly:  core.USDollars(900),
		ExecutedMonthly:  core.USDollars(850),
		ValidatedMonthly: core.USDollars(820),
		RealizedMonthly:  core.USDollars(790),
		CreatedAt:        now,
	}
	f := BuildFunnel("t1", core.PeriodOfDays(now, 30), []SavingsRecord{rec})

	assert.Equal(t, core.USDollars(1000).Micros(), f.Potential.Micros())
	assert.Equal(t, core.USDollars(790).Micros(), f.Realized.Micros())
	assert.True(t, f.OverAttributed.IsZero(), "a well-formed funnel must not be adjusted")
	assert.Empty(t, f.OverAttributionNotes)
}
