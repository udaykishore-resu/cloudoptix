package pricing

import (
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// commitmentSafetyMargin is the fraction of steady-state usage left
// uncommitted. A commitment is a bet that today's usage persists; the
// analysis is deliberately conservative about that bet rather than
// maximising the headline discount, because a customer whose usage drops
// after committing pays for capacity they no longer run, and that outcome
// (a savings recommendation that turns into a stranded cost) is worse for
// trust than a slightly smaller discount. 20% held back on-demand absorbs
// normal month-to-month variance (seasonality, a team's workload shrinking,
// a service being decommissioned) without requiring CloudOptix to forecast
// that variance precisely — it is a fixed margin, not a prediction.
const commitmentSafetyMargin = 0.20

// InstanceUsage is one line of steady-state on-demand usage the commitment
// analysis reasons about: how many hours per month a given instance
// type/region/platform combination has been running. It is the input the
// analysis needs and nothing more — CloudOptix's usage aggregation is
// responsible for turning raw CloudWatch/discovery data into this steady-
// state shape (e.g. the trailing-30-day minimum concurrent count times 730
// hours) before calling in here.
type InstanceUsage struct {
	Region        core.Region
	InstanceType  string
	Platform      string
	HoursPerMonth float64
}

// CommitmentItem is the recommended commitment for one usage line.
type CommitmentItem struct {
	Region       core.Region
	InstanceType string
	// Term and Payment name the winning commitment: "1yr"/"3yr" and
	// "reserved_no_upfront" / "reserved_all_upfront" /
	// "savings_plan_no_upfront" / "savings_plan_all_upfront".
	Term    string
	Payment string

	CoveredHours   float64 // hours/month committed
	UncoveredHours float64 // hours/month left on-demand as safety margin

	OnDemandMonthly  core.Money // full usage priced at on-demand
	CommittedMonthly core.Money // covered hours at the commitment rate + uncovered hours at on-demand
	MonthlySaving    core.Money
}

// CommitmentRecommendation is the aggregated result of CommitmentAnalysis.
type CommitmentRecommendation struct {
	Items                   []CommitmentItem
	TotalOnDemandMonthly    core.Money
	TotalCommittedMonthly   core.Money
	TotalMonthlySaving      core.Money
	SafetyMarginCoveragePct float64 // fraction of usage deliberately left on-demand
}

// candidateCommitments are the term/payment pairs CommitmentAnalysis compares
// for each usage line, in no particular order — the analysis picks whichever
// prices lowest for that specific instance type and region.
var candidateCommitments = []struct{ term, payment string }{
	{"1yr", "reserved_no_upfront"},
	{"1yr", "reserved_all_upfront"},
	{"1yr", "savings_plan_no_upfront"},
	{"1yr", "savings_plan_all_upfront"},
	{"3yr", "reserved_no_upfront"},
	{"3yr", "reserved_all_upfront"},
	{"3yr", "savings_plan_no_upfront"},
	{"3yr", "savings_plan_all_upfront"},
}

// CommitmentAnalysis computes, per usage line, the commitment (term and
// payment option) that maximises monthly saving on the committed portion of
// usage while holding back commitmentSafetyMargin of the usage on-demand.
//
// A usage line whose instance type or region cannot be priced is skipped
// rather than guessed at; the caller can detect this by comparing the
// returned item count against len(usage).
func (c *Catalog) CommitmentAnalysis(usage []InstanceUsage) CommitmentRecommendation {
	rec := CommitmentRecommendation{
		TotalOnDemandMonthly:  core.ZeroUSD(),
		TotalCommittedMonthly: core.ZeroUSD(),
	}
	var totalHours, uncommittedHours float64

	for _, u := range usage {
		odRate, ok := c.InstancePrice(u.Region, u.InstanceType, u.Platform)
		if !ok || u.HoursPerMonth <= 0 {
			continue
		}
		onDemandMonthly := odRate.Scale(u.HoursPerMonth)

		coveredHours := u.HoursPerMonth * (1 - commitmentSafetyMargin)
		uncoveredHours := u.HoursPerMonth - coveredHours

		bestTerm, bestPayment := "", ""
		bestRate := core.Money{}
		haveBest := false
		for _, cand := range candidateCommitments {
			rate, ok := c.CommitmentPrice(u.Region, u.InstanceType, cand.term, cand.payment)
			if !ok {
				continue
			}
			if !haveBest || rate.LessThan(bestRate) {
				bestRate, bestTerm, bestPayment, haveBest = rate, cand.term, cand.payment, true
			}
		}
		if !haveBest {
			// No commitment pricing known for this type/region (e.g. spot
			// or an unpriced region); nothing safe to recommend.
			continue
		}

		committedMonthly := bestRate.Scale(coveredHours).MustAdd(odRate.Scale(uncoveredHours))
		saving := onDemandMonthly.MustSub(committedMonthly)

		rec.Items = append(rec.Items, CommitmentItem{
			Region: u.Region, InstanceType: u.InstanceType,
			Term: bestTerm, Payment: bestPayment,
			CoveredHours: coveredHours, UncoveredHours: uncoveredHours,
			OnDemandMonthly: onDemandMonthly, CommittedMonthly: committedMonthly,
			MonthlySaving: saving,
		})
		rec.TotalOnDemandMonthly = rec.TotalOnDemandMonthly.MustAdd(onDemandMonthly)
		rec.TotalCommittedMonthly = rec.TotalCommittedMonthly.MustAdd(committedMonthly)
		totalHours += u.HoursPerMonth
		uncommittedHours += uncoveredHours
	}

	rec.TotalMonthlySaving = rec.TotalOnDemandMonthly.MustSub(rec.TotalCommittedMonthly)
	if totalHours > 0 {
		rec.SafetyMarginCoveragePct = uncommittedHours / totalHours
	}
	sort.Slice(rec.Items, func(i, j int) bool {
		return rec.Items[i].MonthlySaving.GreaterThan(rec.Items[j].MonthlySaving)
	})
	return rec
}
