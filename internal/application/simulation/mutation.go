package simulation

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// patternResult is what one architecture-mutation pattern builder produces.
// mutation.go turns this into a full simulate.Candidate so every pattern
// function only has to state its changes, scores and reasoning — the
// mechanical roll-up (projected cost, delta, savings percentage) is computed
// once, identically, for every pattern.
type patternResult struct {
	Name, Summary, Pattern string
	// Applicable false means the pattern genuinely does not fit this
	// topology — a stateful workload proposed for Lambda, no database present
	// to migrate. Per the task's own instruction, an inapplicable pattern is
	// reported as a blocker, never scored as if it were a live option: no
	// Changes, Scores or cost figures are computed for it.
	Applicable bool
	Blocker    string

	Changes        []simulate.ComponentChange
	Scores         []simulate.DimensionScore
	Assumptions    []simulate.Assumption
	Risks          []string
	MigrationSteps []string
	Confidence     core.Confidence
}

// patternBuilder generates one candidate architecture from the scoped
// inventory and topology. Every builder is a pure function of its inputs —
// no I/O — which is what makes candidate generation unit-testable without a
// database.
type patternBuilder func(inv *cloud.Inventory, topo *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult

// patternBuilders is the catalog of architecture-mutation patterns. The keys
// are the pattern identifiers MutationRequest.Patterns and Candidate.Pattern
// use.
var patternBuilders = map[string]patternBuilder{
	"containerize_to_serverless": buildContainerizeToServerless,
	"consolidate_to_ecs_fargate": buildConsolidateToFargate,
	"managed_data_migration":     buildManagedDataMigration,
	"network_cost_elimination":   buildNetworkCostElimination,
	"commitment_and_spot":        buildCommitmentAndSpot,
	"graviton_migration":         buildGravitonMigration,
	"caching_layer_introduction": buildCachingLayerIntroduction,
	"storage_tiering":            buildStorageTiering,
}

// MutateArchitecture loads the current topology and cost for a scope,
// generates every applicable pattern candidate, prices each against the
// pricing catalog, scores all eight simulate.Dimensions, and ranks the
// result using weights derived from the tenant's risk tolerance.
func (s *Service) MutateArchitecture(ctx context.Context, tenant core.TenantID, in ports.MutationRequest) (simulate.Simulation, error) {
	filter := scopeFilter(in)
	inv, err := s.Resources.LoadInventory(ctx, tenant, filter)
	if err != nil {
		return simulate.Simulation{}, err
	}
	topo, err := s.Resources.LoadTopology(ctx, tenant, filter)
	if err != nil {
		return simulate.Simulation{}, err
	}

	baseline := inv.TotalMonthlyCost()
	region := dominantRegion(inv)
	simID := core.NewID("sim")

	wantPattern := map[string]bool{}
	for _, p := range in.Patterns {
		wantPattern[p] = true
	}

	var applicable, blocked []simulate.Candidate
	for name, build := range patternBuilders {
		if len(wantPattern) > 0 && !wantPattern[name] {
			continue
		}
		pr := build(inv, topo, s.Pricing, region)
		cand := simulate.Candidate{
			ID: core.NewID("cand"), TenantID: tenant, SimulationID: simID,
			Name: pr.Name, Summary: pr.Summary, Pattern: pr.Pattern,
			CurrentMonthlyCost: baseline,
			Assumptions:        pr.Assumptions,
			MigrationSteps:     pr.MigrationSteps,
			Confidence:         pr.Confidence,
		}
		if !pr.Applicable {
			cand.Blockers = []string{pr.Blocker}
			cand.ProjectedMonthlyCost = baseline
			blocked = append(blocked, cand)
			continue
		}
		delta := core.ZeroUSD()
		for _, ch := range pr.Changes {
			delta = delta.MustAdd(ch.MonthlyDelta)
		}
		cand.Changes = pr.Changes
		cand.ProjectedMonthlyCost = baseline.MustAdd(delta)
		cand.MonthlyDelta = delta
		cand.AnnualDelta = delta.Annualized()
		if !baseline.IsZero() {
			cand.SavingsPct = delta.Scale(-1).Ratio(baseline) * 100
		}
		cand.Scores = pr.Scores
		cand.Risks = pr.Risks
		applicable = append(applicable, cand)
	}

	weights := simulate.WeightsForRiskTolerance(in.RiskTolerance)
	applicable = simulate.RankCandidates(applicable, weights)
	if in.MaxCandidates > 0 && len(applicable) > in.MaxCandidates {
		applicable = applicable[:in.MaxCandidates]
	}
	candidates := append(applicable, blocked...)

	sim := simulate.Simulation{
		ID: simID, TenantID: tenant, Name: in.Name, Scope: in.Scope, ScopeID: in.ScopeID,
		Kind:         simulate.KindArchitectureMutation,
		BaselineCost: baseline,
		Candidates:   candidates,
		Weights:      weights,
		RequestedBy:  in.RequestedBy,
		CreatedAt:    s.now(),
		CompletedAt:  s.now(),
		Status:       "completed",
	}
	if err := s.Store.SaveSimulation(ctx, sim); err != nil {
		return simulate.Simulation{}, err
	}
	return sim, nil
}

// scopeFilter translates a MutationRequest's scope onto a ResourceFilter.
// "account" is interpreted as the literal 12-digit AWS account id rather
// than CloudOptix's internal AWSAccount.ID surrogate key, because
// ResourceFilter itself only ever filters by the AWS account id
// (AccountIDs []core.AccountID) — resolving a surrogate id would need an
// AWSAccountRepository this package does not otherwise depend on, for a
// single call site.
func scopeFilter(in ports.MutationRequest) ports.ResourceFilter {
	switch in.Scope {
	case "application":
		return ports.ResourceFilter{ApplicationID: in.ScopeID}
	case "workload":
		return ports.ResourceFilter{WorkloadID: in.ScopeID}
	case "account":
		return ports.ResourceFilter{AccountIDs: []core.AccountID{core.AccountID(in.ScopeID)}}
	default:
		return ports.ResourceFilter{}
	}
}

// dominantRegion picks the most common region among the scoped resources —
// the region every candidate's pricing-catalog lookups use — falling back to
// us-east-1 for an empty scope.
func dominantRegion(inv *cloud.Inventory) core.Region {
	counts := map[core.Region]int{}
	for _, r := range inv.All() {
		if r.Region != "" {
			counts[r.Region]++
		}
	}
	best := core.Region("us-east-1")
	bestCount := 0
	for region, c := range counts {
		if c > bestCount {
			best, bestCount = region, c
		}
	}
	return best
}

func dimScore(d simulate.Dimension, score, delta float64, rationale string, conf core.Confidence) simulate.DimensionScore {
	return simulate.DimensionScore{Dimension: d, Score: score, Delta: delta, Rationale: rationale, Confidence: conf}
}
