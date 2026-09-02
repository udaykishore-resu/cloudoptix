package costing

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Explain decomposes the movement between two periods into ranked
// per-service contributors and correlates it with CloudOptix's own executed
// changes and compiled infrastructure changes, so "why did cost change"
// gets an answer that names a cause rather than only reporting the number.
func (s *Service) Explain(ctx context.Context, tenant core.TenantID, current, baseline core.Period) (ports.CostExplanation, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.CostExplanation{}, err
	}
	currentTotal, err := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: current})
	if err != nil {
		return ports.CostExplanation{}, err
	}
	baselineTotal, err := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: baseline})
	if err != nil {
		return ports.CostExplanation{}, err
	}
	delta := currentTotal.MustSub(baselineTotal)
	deltaPct := 0.0
	if !baselineTotal.IsZero() {
		deltaPct = delta.Ratio(baselineTotal) * 100
	}

	curBD, err := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: current}, "service")
	if err != nil {
		return ports.CostExplanation{}, err
	}
	baseBD, err := s.Repos.Costs.Breakdown(ctx, tenant, ports.CostFilter{Period: baseline}, "service")
	if err != nil {
		return ports.CostExplanation{}, err
	}
	contributors := diffBreakdowns(curBD, baseBD)

	explanation := ports.CostExplanation{
		CurrentPeriod: current, BaselinePeriod: baseline, CurrentTotal: currentTotal, BaselineTotal: baselineTotal,
		Delta: delta, DeltaPct: deltaPct, Contributors: contributors,
		Narrative:     narrateExplanation(baselineTotal, currentTotal, delta, deltaPct, contributors),
		LinkedChanges: s.linkedChanges(ctx, tenant, baseline.Start, current.End, delta),
	}
	return explanation, nil
}

// diffBreakdowns turns two same-shaped breakdowns into ranked contributions:
// a service present in both periods contributes its own delta, a service
// that appeared or disappeared contributes its full amount with the
// appropriate sign.
func diffBreakdowns(cur, base cost.Breakdown) []cost.Contribution {
	baseAmt := map[string]core.Money{}
	for _, it := range base.Items {
		baseAmt[it.Key] = it.Amount
	}
	seen := map[string]bool{}
	var contribs []cost.Contribution
	magnitude := core.ZeroUSD()
	for _, it := range cur.Items {
		seen[it.Key] = true
		delta := it.Amount.MustSub(baseAmt[it.Key])
		if delta.IsZero() {
			continue
		}
		contribs = append(contribs, cost.Contribution{Dimension: "service", Key: it.Key, Delta: delta})
		magnitude = magnitude.MustAdd(delta.Abs())
	}
	for k, amt := range baseAmt {
		if seen[k] || amt.IsZero() {
			continue
		}
		delta := core.ZeroUSD().MustSub(amt)
		contribs = append(contribs, cost.Contribution{Dimension: "service", Key: k, Delta: delta})
		magnitude = magnitude.MustAdd(delta.Abs())
	}
	sort.Slice(contribs, func(i, j int) bool { return contribs[i].Delta.Abs().Micros() > contribs[j].Delta.Abs().Micros() })
	if !magnitude.IsZero() {
		for i := range contribs {
			contribs[i].Share = contribs[i].Delta.Abs().Ratio(magnitude)
		}
	}
	if len(contribs) > 10 {
		contribs = contribs[:10]
	}
	return contribs
}

func narrateExplanation(baseline, current, delta core.Money, deltaPct float64, contributors []cost.Contribution) string {
	direction := "increased"
	if delta.IsNegative() {
		direction = "decreased"
	}
	narrative := fmt.Sprintf("Spend %s from %s to %s, a change of %s (%.1f%%).",
		direction, baseline.Format(), current.Format(), delta.Format(), deltaPct)
	if len(contributors) == 0 {
		return narrative
	}
	top := contributors[0]
	topDirection := "up"
	if top.Delta.IsNegative() {
		topDirection = "down"
	}
	return narrative + fmt.Sprintf(" %s moved the most (%s %s, %.0f%% of the total movement).",
		top.Key, topDirection, top.Delta.Abs().Format(), top.Share*100)
}

// linkedChanges correlates the movement with CloudOptix's own executed
// optimizations and compiled infrastructure changes that landed inside the
// window, ranked by how much of the movement each one's own cost impact
// could plausibly explain.
func (s *Service) linkedChanges(ctx context.Context, tenant core.TenantID, from, to time.Time, totalDelta core.Money) []ports.LinkedChange {
	var out []ports.LinkedChange

	if s.Repos.Executions != nil {
		states := []execute.PlanState{execute.PlanExecuted, execute.PlanValidated, execute.PlanRolledBack}
		if page, err := s.Repos.Executions.ListPlans(ctx, tenant, states, ports.ListOptions{Limit: 200}); err == nil {
			for _, p := range page.Items {
				if p.FinishedAt == nil || p.FinishedAt.Before(from) || p.FinishedAt.After(to) {
					continue
				}
				// An executed optimization is a saving, so its cost impact is
				// negative regardless of the sign convention ExpectedMonthlySaving
				// happens to be stored in.
				impact := core.ZeroUSD().MustSub(p.ExpectedMonthlySaving.Abs())
				out = append(out, ports.LinkedChange{
					Kind: "execution", ID: p.ID, Label: p.Title, At: *p.FinishedAt,
					CostImpact: impact, Correlation: correlationScore(impact, totalDelta),
				})
			}
		}
	}

	if s.Repos.Simulations != nil {
		if page, err := s.Repos.Simulations.ListCompilations(ctx, tenant, ports.ListOptions{Limit: 200}); err == nil {
			for _, c := range page.Items {
				if c.CompiledAt.Before(from) || c.CompiledAt.After(to) {
					continue
				}
				out = append(out, ports.LinkedChange{
					Kind: "compilation", ID: c.ID, Label: c.Label, At: c.CompiledAt,
					CostImpact: c.MonthlyDelta, Correlation: correlationScore(c.MonthlyDelta, totalDelta),
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Correlation > out[j].Correlation })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// correlationScore is a simple same-direction, magnitude-bounded heuristic:
// a change whose own cost impact moves the same way as the overall delta and
// is close to it in size scores near 1; an opposite-direction or
// wildly-oversized change scores low or negative. It is not a statistical
// correlation coefficient — a single pairwise comparison has no distribution
// to compute one from — but it orders candidates the way a human skimming
// the list would.
func correlationScore(impact, totalDelta core.Money) float64 {
	if totalDelta.IsZero() {
		return 0
	}
	ratio := impact.Abs().Ratio(totalDelta.Abs())
	if ratio > 1 {
		ratio = 1 / ratio
	}
	sameSign := impact.IsZero() || impact.IsNegative() == totalDelta.IsNegative()
	if !sameSign {
		ratio = -ratio
	}
	return ratio
}
