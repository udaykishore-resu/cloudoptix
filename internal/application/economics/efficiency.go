package economics

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// EfficiencyScore computes the Cloud Efficiency Score for a scope from real
// evidence — utilisation telemetry, open findings, purchase models and the
// savings funnel — across econ.StandardFactorWeights' eight factors, and
// persists it so the prior-period Delta the UI shows is a real comparison
// rather than a re-derivation.
//
// Two of the eight factors (automation_maturity, governance_maturity) draw
// on ExecutionRepository/SavingsRepository and RecommendationRepository
// methods that report tenant-wide, not scope-filtered — neither
// SavingsRepository.Funnel nor RecommendationRepository.Summary takes a
// scope argument. For any scope narrower than the organization those two
// factors are therefore the tenant's own figure, not the scope's; this is
// noted in each factor's Detail text rather than silently presented as
// scope-specific.
func (s *Service) EfficiencyScore(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID) (econ.EfficiencyScore, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.EfficiencyScore{}, err
	}
	period := s.defaultPeriod()
	own, label, err := s.resourcesForScope(ctx, tenant, scope, scopeID)
	if err != nil {
		return econ.EfficiencyScore{}, err
	}

	byResource, _ := s.Repos.Costs.ByResource(ctx, tenant, ports.CostFilter{Period: period})
	periodDays := period.Days()
	if periodDays <= 0 {
		periodDays = core.AverageDaysPerMonth
	}
	costOf := func(r cloud.Resource) core.Money {
		if amt, ok := byResource[r.ID]; ok {
			return amt
		}
		return r.MonthlyCost.Scale(periodDays / core.AverageDaysPerMonth)
	}
	totalSpend := core.ZeroUSD()
	ownIDs := make([]core.ID, 0, len(own))
	ownSet := make(map[core.ID]bool, len(own))
	for _, r := range own {
		totalSpend = totalSpend.MustAdd(costOf(r))
		ownIDs = append(ownIDs, r.ID)
		ownSet[r.ID] = true
	}

	var openRecs []optimize.Recommendation
	if page, rerr := s.Repos.Recommendations.List(ctx, tenant, ports.RecommendationFilter{
		Statuses: []optimize.Status{optimize.StatusOpen, optimize.StatusUnderReview},
	}, ports.ListOptions{Limit: 500}); rerr == nil {
		openRecs = page.Items
	}
	// Alternatives are skipped: within a conflict group only one member can
	// be applied, so counting all of them would inflate identified waste and
	// therefore deflate the efficiency score — the platform would grade a
	// tenant down for waste that exists once but was counted three times.
	// See optimize/conflict.go.
	scopedSaving := func(cat optimize.Category) core.Money {
		total := core.ZeroUSD()
		for _, rec := range openRecs {
			if rec.Finding.Category != cat || !rec.CountsTowardTotal() {
				continue
			}
			if !ownSet[rec.Finding.ResourceID] {
				continue
			}
			total = total.MustAdd(rec.EstimatedMonthlySaving)
		}
		return total
	}

	var factors []econ.EfficiencyFactor
	factors = append(factors, resourceUtilizationFactor(ctx, s, tenant, own, ownIDs))
	factors = append(factors, wasteFactor("waste_elimination", econ.StandardFactorWeights["waste_elimination"],
		scopedSaving(optimize.CategoryWaste), totalSpend,
		"share of spend identified as waste by open findings"))
	factors = append(factors, commitmentCoverageFactor(own, costOf))
	factors = append(factors, wasteFactor("storage_efficiency", econ.StandardFactorWeights["storage_efficiency"],
		scopedSaving(optimize.CategoryStorage), categoryCost(own, cloud.CategoryStorage, costOf),
		"share of storage spend identified as reclaimable"))
	factors = append(factors, wasteFactor("network_efficiency", econ.StandardFactorWeights["network_efficiency"],
		scopedSaving(optimize.CategoryNetwork), categoryCost(own, cloud.CategoryNetwork, costOf),
		"share of network spend identified as avoidable (NAT, cross-AZ, idle endpoints)"))
	factors = append(factors, architectureEfficiencyFactor(own))
	factors = append(factors, s.automationMaturityFactor(ctx, tenant, period, scope, label))
	factors = append(factors, s.governanceMaturityFactor(ctx, tenant, scope, label))

	identifiedWaste := core.ZeroUSD()
	for _, f := range factors {
		identifiedWaste = identifiedWaste.MustAdd(f.Opportunity)
	}

	score := econ.ComputeEfficiencyScore(tenant, scope, scopeID, label, period, factors, totalSpend, identifiedWaste)
	if prior, perr := s.Repos.Economics.GetEfficiencyScore(ctx, tenant, scope, scopeID); perr == nil {
		score.PriorScore = prior.Score
		score.Delta = score.Score - prior.Score
	}
	_ = s.Repos.Economics.SaveEfficiencyScore(ctx, score)
	return score, nil
}

func categoryCost(resources []cloud.Resource, cat cloud.Category, costOf func(cloud.Resource) core.Money) core.Money {
	total := core.ZeroUSD()
	for _, r := range resources {
		if r.Kind.Category() == cat {
			total = total.MustAdd(costOf(r))
		}
	}
	return total
}

// wasteFactor scores a factor by how much of its relevant cost base a set of
// open findings identifies as reclaimable: no waste at all scores 100, and
// the score reaches zero once identified waste hits a third of the relevant
// spend — a deliberately steep curve, because unlike resource_utilization's
// smooth reward for headroom, waste is binary per-dollar: it either should
// not be there or it should.
func wasteFactor(name string, weight float64, identified, base core.Money, detail string) econ.EfficiencyFactor {
	ratio := 0.0
	if !base.IsZero() {
		ratio = identified.Ratio(base)
	}
	score := 100 * (1 - clamp01(ratio*3))
	return econ.EfficiencyFactor{
		Name: name, Score: score, Weight: weight, Opportunity: identified,
		Detail: fmt.Sprintf("%s: %s identified against %s relevant spend", detail, identified.Format(), base.Format()),
	}
}

func resourceUtilizationFactor(ctx context.Context, s *Service, tenant core.TenantID, resources []cloud.Resource, ids []core.ID) econ.EfficiencyFactor {
	weight := econ.StandardFactorWeights["resource_utilization"]
	metrics, err := s.Repos.Metrics.LoadSummaries(ctx, tenant, ids)
	if err != nil || len(metrics) == 0 {
		return econ.EfficiencyFactor{
			Name: "resource_utilization", Score: 60, Weight: weight,
			Detail: "no utilisation telemetry available; neutral score assigned pending discovery collecting it",
		}
	}
	var sum float64
	var n int
	for _, r := range resources {
		m, ok := metrics[r.ID]
		if !ok || m.CPU == nil || !m.HasSignal(0.5) {
			continue
		}
		n++
		// 70% P95 CPU is treated as fully utilised — the same headroom bar
		// the rightsizing rules use, so this factor and the recommendations
		// that drive waste_elimination never disagree about what "well
		// utilised" means.
		sum += clamp01(m.CPU.P95/70) * 100
	}
	if n == 0 {
		return econ.EfficiencyFactor{
			Name: "resource_utilization", Score: 60, Weight: weight,
			Detail: fmt.Sprintf("none of %d resources have CPU telemetry; neutral score assigned", len(resources)),
		}
	}
	score := sum / float64(n)
	return econ.EfficiencyFactor{
		Name: "resource_utilization", Score: score, Weight: weight,
		Detail: fmt.Sprintf("%d of %d resources have CPU telemetry; normalised utilisation score %.0f/100 against a 70%% P95 target", n, len(resources), score),
	}
}

func commitmentCoverageFactor(resources []cloud.Resource, costOf func(cloud.Resource) core.Money) econ.EfficiencyFactor {
	weight := econ.StandardFactorWeights["commitment_coverage"]
	eligible, committed := core.ZeroUSD(), core.ZeroUSD()
	for _, r := range resources {
		cat := r.Kind.Category()
		if cat != cloud.CategoryCompute && cat != cloud.CategoryDatabase {
			continue
		}
		switch r.Purchase {
		case cloud.PurchaseOnDemand:
			eligible = eligible.MustAdd(costOf(r))
		case cloud.PurchaseReserved, cloud.PurchaseSavingsPlan:
			c := costOf(r)
			eligible = eligible.MustAdd(c)
			committed = committed.MustAdd(c)
		}
	}
	score := 100.0
	detail := "no on-demand-eligible compute or database spend in scope"
	if !eligible.IsZero() {
		score = clamp01(committed.Ratio(eligible)) * 100
		detail = fmt.Sprintf("%s of %s eligible compute/database spend is committed (reserved or savings plan)", committed.Format(), eligible.Format())
	}
	return econ.EfficiencyFactor{Name: "commitment_coverage", Score: score, Weight: weight, Detail: detail}
}

func architectureEfficiencyFactor(resources []cloud.Resource) econ.EfficiencyFactor {
	weight := econ.StandardFactorWeights["architecture_efficiency"]
	if len(resources) == 0 {
		return econ.EfficiencyFactor{Name: "architecture_efficiency", Score: 60, Weight: weight, Detail: "no resources in scope"}
	}
	attributed := 0
	for _, r := range resources {
		if !r.ApplicationID.IsZero() && r.Environment != core.EnvUnknown {
			attributed++
		}
	}
	score := float64(attributed) / float64(len(resources)) * 100
	return econ.EfficiencyFactor{
		Name: "architecture_efficiency", Score: score, Weight: weight,
		Detail: fmt.Sprintf("%d of %d resources have a confirmed application and environment — a well-modelled estate is a precondition for every other figure this platform reports", attributed, len(resources)),
	}
}

func (s *Service) automationMaturityFactor(ctx context.Context, tenant core.TenantID, period core.Period, scope econ.Scope, label string) econ.EfficiencyFactor {
	weight := econ.StandardFactorWeights["automation_maturity"]
	funnel, err := s.Repos.Savings.Funnel(ctx, tenant, period)
	scopedNote := ""
	if scope != econ.ScopeOrganization {
		scopedNote = fmt.Sprintf(" (platform-wide, not yet scoped to %s)", label)
	}
	if err != nil || funnel.Approved.IsZero() {
		return econ.EfficiencyFactor{
			Name: "automation_maturity", Score: 60, Weight: weight,
			Detail: "no approved savings in the window to measure execution follow-through on" + scopedNote,
		}
	}
	score := clamp01(funnel.Executed.Ratio(funnel.Approved)) * 100
	return econ.EfficiencyFactor{
		Name: "automation_maturity", Score: score, Weight: weight,
		Detail: fmt.Sprintf("%s of %s approved savings were actually executed%s", funnel.Executed.Format(), funnel.Approved.Format(), scopedNote),
	}
}

func (s *Service) governanceMaturityFactor(ctx context.Context, tenant core.TenantID, scope econ.Scope, label string) econ.EfficiencyFactor {
	weight := econ.StandardFactorWeights["governance_maturity"]
	summary, err := s.Repos.Recommendations.Summary(ctx, tenant)
	scopedNote := ""
	if scope != econ.ScopeOrganization {
		scopedNote = fmt.Sprintf(" (platform-wide, not yet scoped to %s)", label)
	}
	if err != nil || summary.Open == 0 {
		return econ.EfficiencyFactor{
			Name: "governance_maturity", Score: 60, Weight: weight,
			Detail: "no open recommendations to measure policy coverage against" + scopedNote,
		}
	}
	score := clamp01(float64(summary.AutoExecutable)/float64(summary.Open)) * 100
	return econ.EfficiencyFactor{
		Name: "governance_maturity", Score: score, Weight: weight,
		Detail: fmt.Sprintf("%d of %d open recommendations are policy-cleared for automatic execution%s", summary.AutoExecutable, summary.Open, scopedNote),
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
