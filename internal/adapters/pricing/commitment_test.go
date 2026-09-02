package pricing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestCommitmentAnalysis_BasicMath(t *testing.T) {
	c := New()
	usage := []InstanceUsage{
		{Region: "us-east-1", InstanceType: "m5.large", Platform: "linux", HoursPerMonth: 730},
	}
	rec := c.CommitmentAnalysis(usage)
	require.Len(t, rec.Items, 1)
	item := rec.Items[0]

	odRate, ok := c.InstancePrice("us-east-1", "m5.large", "linux")
	require.True(t, ok)
	wantOnDemand := odRate.Scale(730)
	assert.Equal(t, wantOnDemand.Micros(), item.OnDemandMonthly.Micros())

	// 80% of hours committed, 20% held back as safety margin.
	assert.InDelta(t, 730*0.8, item.CoveredHours, 0.01)
	assert.InDelta(t, 730*0.2, item.UncoveredHours, 0.01)

	// The committed price must beat on-demand.
	assert.True(t, item.CommittedMonthly.LessThan(item.OnDemandMonthly))
	assert.True(t, item.MonthlySaving.GreaterThan(core.ZeroUSD()))

	// Saving = on-demand - committed, reconciled exactly.
	assert.Equal(t, item.OnDemandMonthly.MustSub(item.CommittedMonthly).Micros(), item.MonthlySaving.Micros())

	assert.InDelta(t, 0.20, rec.SafetyMarginCoveragePct, 0.001)
}

func TestCommitmentAnalysis_PicksCheapestOption(t *testing.T) {
	c := New()
	usage := []InstanceUsage{
		{Region: "us-east-1", InstanceType: "r5.xlarge", Platform: "linux", HoursPerMonth: 730},
	}
	rec := c.CommitmentAnalysis(usage)
	require.Len(t, rec.Items, 1)
	item := rec.Items[0]

	// The deepest discount in the candidate set is 3yr all-upfront (reserved
	// or savings plan); verify no cheaper committed rate exists among the
	// candidates than the one chosen.
	best, ok := c.CommitmentPrice("us-east-1", "r5.xlarge", item.Term, item.Payment)
	require.True(t, ok)
	for _, cand := range candidateCommitments {
		rate, ok := c.CommitmentPrice("us-east-1", "r5.xlarge", cand.term, cand.payment)
		require.True(t, ok)
		assert.False(t, rate.LessThan(best), "found a cheaper commitment (%s/%s) than the one chosen (%s/%s)",
			cand.term, cand.payment, item.Term, item.Payment)
	}
}

func TestCommitmentAnalysis_SkipsUnpricedLines(t *testing.T) {
	c := New()
	usage := []InstanceUsage{
		{Region: "us-east-1", InstanceType: "m5.large", Platform: "linux", HoursPerMonth: 730},
		{Region: "mars-central-1", InstanceType: "m5.large", Platform: "linux", HoursPerMonth: 730},
		{Region: "us-east-1", InstanceType: "does.not.exist", Platform: "linux", HoursPerMonth: 730},
		{Region: "us-east-1", InstanceType: "c5.large", Platform: "linux", HoursPerMonth: 0}, // zero usage
	}
	rec := c.CommitmentAnalysis(usage)
	assert.Len(t, rec.Items, 1, "only the one priceable, positive-usage line should produce a recommendation")
}

func TestCommitmentAnalysis_MultipleLinesAggregate(t *testing.T) {
	c := New()
	usage := []InstanceUsage{
		{Region: "us-east-1", InstanceType: "m5.large", Platform: "linux", HoursPerMonth: 730},
		{Region: "us-east-1", InstanceType: "c5.xlarge", Platform: "linux", HoursPerMonth: 500},
	}
	rec := c.CommitmentAnalysis(usage)
	require.Len(t, rec.Items, 2)

	var sumOD, sumCommitted core.Money = core.ZeroUSD(), core.ZeroUSD()
	for _, it := range rec.Items {
		sumOD = sumOD.MustAdd(it.OnDemandMonthly)
		sumCommitted = sumCommitted.MustAdd(it.CommittedMonthly)
	}
	assert.Equal(t, sumOD.Micros(), rec.TotalOnDemandMonthly.Micros())
	assert.Equal(t, sumCommitted.Micros(), rec.TotalCommittedMonthly.Micros())
	assert.Equal(t, sumOD.MustSub(sumCommitted).Micros(), rec.TotalMonthlySaving.Micros())

	// Items sorted descending by saving.
	for i := 1; i < len(rec.Items); i++ {
		assert.False(t, rec.Items[i].MonthlySaving.GreaterThan(rec.Items[i-1].MonthlySaving))
	}
}

func TestCommitmentAnalysis_EmptyUsage(t *testing.T) {
	c := New()
	rec := c.CommitmentAnalysis(nil)
	assert.Empty(t, rec.Items)
	assert.True(t, rec.TotalMonthlySaving.IsZero())
	assert.Equal(t, 0.0, rec.SafetyMarginCoveragePct)
}
