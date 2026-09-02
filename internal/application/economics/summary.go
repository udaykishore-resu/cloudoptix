package economics

import (
	"context"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// scopeEntity names one scope-level entity Compute prices in a single run.
type scopeEntity struct {
	scope econ.Scope
	id    core.ID
}

// Compute runs the full economics batch for a period: a footprint for every
// scope entity the tenant has (organization, every account, every
// environment in use, every application, every workload), unit economics for
// every declared business transaction, and the organization-level efficiency
// score. It is the job the nightly economics run and the "recompute now"
// button both call.
func (s *Service) Compute(ctx context.Context, tenant core.TenantID, period core.Period) (ports.EconomicsResult, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.EconomicsResult{}, err
	}
	if period.IsZero() {
		period = s.defaultPeriod()
	}
	began := time.Now()

	entities := []scopeEntity{{econ.ScopeOrganization, core.ID(tenant)}}

	accounts, _ := s.Repos.AWSAccounts.List(ctx, tenant)
	envSeen := map[core.Environment]bool{}
	for _, a := range accounts {
		entities = append(entities, scopeEntity{econ.ScopeAccount, core.ID(a.AccountID)})
		if a.Environment != "" && !envSeen[a.Environment] {
			envSeen[a.Environment] = true
			entities = append(entities, scopeEntity{econ.ScopeEnvironment, core.ID(a.Environment)})
		}
	}

	apps, _ := s.Repos.Applications.ListApplications(ctx, tenant)
	for _, app := range apps {
		entities = append(entities, scopeEntity{econ.ScopeApplication, app.ID})
		workloads, werr := s.Repos.Applications.ListWorkloads(ctx, tenant, app.ID)
		if werr != nil {
			continue
		}
		for _, w := range workloads {
			entities = append(entities, scopeEntity{econ.ScopeWorkload, w.ID})
		}
	}

	var footprints []econ.Footprint
	totalAttributed, totalUnattributed := core.ZeroUSD(), core.ZeroUSD()
	for _, e := range entities {
		fp, ferr := s.computeFootprintCore(ctx, tenant, e.scope, e.id, period, true)
		if ferr != nil {
			// A single scope entity failing (a deleted application still
			// referenced somewhere, an account with nothing discovered yet)
			// must not abort the whole tenant's economics run.
			continue
		}
		footprints = append(footprints, fp)
		if e.scope == econ.ScopeOrganization {
			totalAttributed = fp.Total
			totalUnattributed = fp.Unattributed
		}
	}
	if len(footprints) > 0 {
		if err := s.Repos.Economics.SaveFootprints(ctx, tenant, footprints); err != nil {
			return ports.EconomicsResult{}, err
		}
	}

	txs, _ := s.Repos.Economics.ListTransactions(ctx, tenant)
	priced := 0
	for _, tx := range txs {
		if _, uerr := s.UnitEconomics(ctx, tenant, tx.ID, period); uerr == nil {
			priced++
		}
	}

	// The efficiency score is computed for the organization here so that
	// ExecutiveSummary always has a fresh one to read; per-application and
	// per-workload scores are computed on demand by EfficiencyScore rather
	// than for every entity on every run, since nothing in ports requires a
	// full efficiency sweep on the same cadence as footprints.
	_, _ = s.EfficiencyScore(ctx, tenant, econ.ScopeOrganization, core.ID(tenant))

	coverage := 1.0
	relevant := totalAttributed.MustAdd(totalUnattributed)
	if !relevant.IsZero() {
		coverage = totalAttributed.Ratio(relevant)
	}

	return ports.EconomicsResult{
		Period: period, FootprintsComputed: len(footprints), TotalAttributed: totalAttributed,
		TotalUnattributed: totalUnattributed, Coverage: coverage, TransactionsPriced: priced,
		DurationMS: time.Since(began).Milliseconds(),
	}, nil
}

// ExecutiveSummary assembles the board-level view from every engine this
// package composes: spend and its forecast, the savings funnel, Cost SLO
// health and the efficiency score.
func (s *Service) ExecutiveSummary(ctx context.Context, tenant core.TenantID) (ports.ExecutiveSummary, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.ExecutiveSummary{}, err
	}
	now := s.clock().Now()
	month := core.MonthOf(now)

	monthToDate, _ := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: core.Period{Start: month.Start, End: now}})
	priorMonth, _ := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{
		Period: core.Period{Start: month.Start.AddDate(0, -1, 0), End: month.Start},
	})
	changePct := 0.0
	if !priorMonth.IsZero() {
		changePct = monthToDate.MustSub(priorMonth).Ratio(priorMonth) * 100
	}
	// A simple run-rate projection of the month, kept local rather than
	// importing package costing's own (more sophisticated) Forecast: pulling
	// in a sibling application package here would couple two of the five
	// engines this task built independently, for one headline number that a
	// linear day-count projection already estimates reasonably.
	forecastMonthEnd := monthToDate
	if daysElapsed := now.Sub(month.Start).Hours() / 24; daysElapsed > 0 {
		forecastMonthEnd = monthToDate.Div(daysElapsed).Scale(month.Days())
	}

	trailing := core.PeriodOfDays(now, 30)
	trailingTotal, _ := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: trailing})

	recSummary, _ := s.Repos.Recommendations.Summary(ctx, tenant)
	funnel, _ := s.Repos.Savings.Funnel(ctx, tenant, trailing)

	wastePct := 0.0
	if !trailingTotal.IsZero() {
		wastePct = recSummary.TotalMonthlySaving.Ratio(trailingTotal) * 100
	}

	es, err := s.Repos.Economics.GetEfficiencyScore(ctx, tenant, econ.ScopeOrganization, core.ID(tenant))
	if err != nil {
		es, err = s.EfficiencyScore(ctx, tenant, econ.ScopeOrganization, core.ID(tenant))
		if err != nil {
			es = econ.EfficiencyScore{}
		}
	}

	budgets, _ := s.BudgetStates(ctx, tenant)
	var healthy, atRisk, breached int
	for _, b := range budgets {
		switch b.State {
		case econ.BudgetHealthy:
			healthy++
		case econ.BudgetWatch, econ.BudgetAtRisk:
			atRisk++
		case econ.BudgetExhausted, econ.BudgetBreached:
			breached++
		}
	}

	var topOpportunities []optimize.Recommendation
	if page, rerr := s.Repos.Recommendations.List(ctx, tenant, ports.RecommendationFilter{
		Statuses: []optimize.Status{optimize.StatusOpen},
	}, ports.ListOptions{Limit: 100}); rerr == nil {
		items := page.Items
		sort.Slice(items, func(i, j int) bool {
			return items[i].EstimatedMonthlySaving.Micros() > items[j].EstimatedMonthlySaving.Micros()
		})
		if len(items) > 5 {
			items = items[:5]
		}
		topOpportunities = items
	}

	txs, _ := s.Repos.Economics.ListTransactions(ctx, tenant)
	var topTransactions []econ.UnitEconomics
	for _, tx := range txs {
		ue, uerr := s.UnitEconomics(ctx, tenant, tx.ID, trailing)
		if uerr != nil {
			continue
		}
		topTransactions = append(topTransactions, ue)
	}
	sort.Slice(topTransactions, func(i, j int) bool {
		return topTransactions[i].TotalCost.Micros() > topTransactions[j].TotalCost.Micros()
	})
	if len(topTransactions) > 5 {
		topTransactions = topTransactions[:5]
	}

	return ports.ExecutiveSummary{
		Period: core.Period{Start: month.Start, End: now}, MonthlySpend: monthToDate,
		ForecastMonthEnd: forecastMonthEnd, PriorMonthSpend: priorMonth, SpendChangePct: changePct,
		PotentialSavings: recSummary.TotalMonthlySaving, RealizedSavings: funnel.Realized,
		RealizedAnnualized: funnel.RealizedAnnual, WastePct: wastePct,
		EfficiencyScore: es.Score, EfficiencyGrade: es.Grade,
		CostSLOsHealthy: healthy, CostSLOsAtRisk: atRisk, CostSLOsBreached: breached,
		BudgetStates: budgets, TopOpportunities: topOpportunities, TopTransactions: topTransactions,
		SavingsFunnel: funnel, GeneratedAt: now,
	}, nil
}
