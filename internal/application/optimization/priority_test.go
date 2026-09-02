package optimization

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// mkRec builds a minimal Recommendation with the axes PriorityFormula.Score
// weighs, for tests that only care about ranking order.
func mkRec(saving core.Money, confidence core.Confidence, reversibility optimize.Reversibility, complexity optimize.Complexity, riskScore, blastScore float64, production bool) optimize.Recommendation {
	env := core.EnvDevelopment
	if production {
		env = core.EnvProduction
	}
	return optimize.Recommendation{
		ID:                     core.NewID("rec"),
		Finding:                optimize.Finding{Environment: env},
		EstimatedMonthlySaving: saving,
		Confidence:             confidence,
		Reversibility:          reversibility,
		Complexity:             complexity,
		Risk:                   optimize.RiskAssessment{Score: riskScore},
		BlastRadius:            optimize.BlastRadius{Score: blastScore},
	}
}

// TestPriorityRank_OrdersHigherValueFirst checks the formula's stated
// stance — prefer real savings CloudOptix is confident about and that can be
// undone, penalise risk and effort — actually produces that ordering, not
// just a formula that happens to compile.
func TestPriorityRank_OrdersHigherValueFirst(t *testing.T) {
	formula := optimize.DefaultPriorityFormula()

	big := mkRec(core.USDollars(2000), 0.9, optimize.ReversibilityInstant, optimize.ComplexityTrivial, 0.1, 0.1, false)
	small := mkRec(core.USDollars(20), 0.9, optimize.ReversibilityInstant, optimize.ComplexityTrivial, 0.1, 0.1, false)
	lowConfidence := mkRec(core.USDollars(2000), 0.2, optimize.ReversibilityInstant, optimize.ComplexityTrivial, 0.1, 0.1, false)
	risky := mkRec(core.USDollars(2000), 0.9, optimize.ReversibilityNone, optimize.ComplexityProject, 0.9, 0.9, false)
	prodCritical := mkRec(core.USDollars(2000), 0.9, optimize.ReversibilityInstant, optimize.ComplexityTrivial, 0.1, 0.1, true)

	ranked := optimize.Rank([]optimize.Recommendation{small, risky, lowConfidence, big, prodCritical}, formula)
	require.Len(t, ranked, 5)

	byID := map[core.ID]int{}
	for _, r := range ranked {
		byID[r.ID] = r.Rank
	}

	assert.Less(t, byID[big.ID], byID[small.ID], "a larger saving must outrank a smaller one at equal confidence/risk")
	assert.Less(t, byID[big.ID], byID[lowConfidence.ID], "higher confidence must outrank lower confidence at equal saving")
	assert.Less(t, byID[big.ID], byID[risky.ID], "low risk/blast/complexity must outrank a risky, hard-to-undo, high-effort change at equal saving")
	assert.Less(t, byID[big.ID], byID[prodCritical.ID], "an otherwise identical change against production must rank behind the non-production one")

	assert.Equal(t, 1, byID[big.ID], "the strictly-best recommendation on every axis must rank first")
}

// TestPriorityRank_StableTieBreakOnSaving checks that recommendations tied
// on priority score break the tie in favour of the larger dollar amount,
// deterministically, rather than leaving order to map/slice iteration. A
// formula with SavingsWeight 0 is used to force an exact score tie across
// different dollar amounts (pow(x, 0) == 1 for any x > 0), isolating the
// tie-break path from the ordinary case where a bigger saving already wins
// on score alone.
func TestPriorityRank_StableTieBreakOnSaving(t *testing.T) {
	formula := optimize.PriorityFormula{
		ConfidenceWeight: 1, ReversibilityWeight: 1, ComplexityWeight: 1,
		SavingsNormalizer: core.USDollars(1),
	}
	a := mkRec(core.USDollars(100), 0.8, optimize.ReversibilityFast, optimize.ComplexityLow, 0.2, 0.2, false)
	b := mkRec(core.USDollars(100), 0.8, optimize.ReversibilityFast, optimize.ComplexityLow, 0.2, 0.2, false)
	bigger := mkRec(core.USDollars(150), 0.8, optimize.ReversibilityFast, optimize.ComplexityLow, 0.2, 0.2, false)

	ranked := optimize.Rank([]optimize.Recommendation{a, b}, formula)
	require.Equal(t, ranked[0].PriorityScore, ranked[1].PriorityScore, "identical inputs must score identically")

	ranked2 := optimize.Rank([]optimize.Recommendation{a, bigger}, formula)
	require.Equal(t, ranked2[0].PriorityScore, ranked2[1].PriorityScore, "SavingsWeight=0 must make the score itself indifferent to the saving amount")
	byID := map[core.ID]int{}
	for _, r := range ranked2 {
		byID[r.ID] = r.Rank
	}
	assert.Equal(t, 1, byID[bigger.ID], "a tie on score must break toward the larger saving")
}
