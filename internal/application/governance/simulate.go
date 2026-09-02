package governance

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// maxSimulationRecommendations bounds how many of the tenant's open
// recommendations one Simulate call evaluates. Simulate is read-only — it
// never plans or executes anything — but an unbounded scan is still the
// platform's blast-radius discipline applied to a read path: a tenant with a
// very large open recommendation backlog gets an honest, capped answer with
// a warning rather than a call that degrades the whole service while it
// walks every row.
const maxSimulationRecommendations = 5000

// Simulate answers "what would this proposed policy do to my current
// recommendations" by evaluating both the tenant's currently active policy
// and the proposed one against every open recommendation, and reporting
// exactly which ones would change governance outcome. This is what makes a
// policy edit reviewable before ActivatePolicy makes it real: a reviewer
// sees the list of specific recommendations that would newly auto-execute,
// or newly require approval, or become prohibited — not just a rule diff.
func (s *Service) Simulate(ctx context.Context, tenant core.TenantID, p govern.Policy) (ports.PolicySimulation, error) {
	sim := ports.PolicySimulation{PolicyName: p.Name, AutoExecutableSaving: core.ZeroUSD()}

	if v := p.Validate(); v.HasBlocking() {
		for _, issue := range v.Issues {
			if issue.Severity.Order() >= core.SeverityHigh.Order() {
				sim.Warnings = append(sim.Warnings, fmt.Sprintf("%s: %s", issue.Path, issue.Message))
			}
		}
	}

	activePolicy, _, err := s.loadActivePolicyOrFallback(ctx, tenant)
	if err != nil {
		return ports.PolicySimulation{}, fmt.Errorf("governance: loading active policy for comparison: %w", err)
	}
	sp, err := s.loadActiveSpec(ctx, tenant)
	if err != nil {
		return ports.PolicySimulation{}, fmt.Errorf("governance: loading active specification: %w", err)
	}
	now := s.d.Clock.Now()

	recs, truncated, err := s.loadOpenRecommendations(ctx, tenant)
	if err != nil {
		return ports.PolicySimulation{}, fmt.Errorf("governance: loading open recommendations: %w", err)
	}
	if truncated {
		sim.Warnings = append(sim.Warnings, fmt.Sprintf(
			"only the first %d open recommendations were evaluated; the tenant has more open recommendations than this simulation scans", maxSimulationRecommendations))
	}

	resourceCache := map[core.ID]cloud.Resource{}
	for _, rec := range recs {
		res, err := s.resourceFor(ctx, tenant, rec.Finding.ResourceID, resourceCache)
		if err != nil {
			sim.Warnings = append(sim.Warnings, fmt.Sprintf(
				"recommendation %s references a resource that could not be loaded (%v); skipped", rec.ID, err))
			continue
		}
		freeze, requireApproval := s.matchBudgets(ctx, tenant, res)
		in, err := buildInput(tenant, rec, res, sp, freeze, requireApproval, now)
		if err != nil {
			sim.Warnings = append(sim.Warnings, fmt.Sprintf(
				"recommendation %s could not be evaluated: %v; skipped", rec.ID, err))
			continue
		}

		fromDecision := govern.Evaluate(activePolicy, in)
		applySpecificationGuards(&fromDecision, sp, res, rec, now)

		toDecision := govern.Evaluate(p, in)
		applySpecificationGuards(&toDecision, sp, res, rec, now)

		sim.Evaluated++
		switch toDecision.Effect {
		case govern.EffectAutoExecute:
			sim.AutoExecute++
			// The count includes every recommendation the candidate policy
			// would clear; the money counts primaries only, because a policy
			// that clears three mutually exclusive fixes for one node group
			// has cleared one saving, not three. See optimize/conflict.go.
			if rec.CountsTowardTotal() {
				sim.AutoExecutableSaving = sim.AutoExecutableSaving.MustAdd(rec.EstimatedMonthlySaving)
			}
		case govern.EffectRequireApproval:
			sim.RequireApproval++
		case govern.EffectProhibit:
			sim.Prohibited++
		case govern.EffectAdvisory:
			sim.Advisory++
		}

		if fromDecision.Effect != toDecision.Effect {
			sim.Changes = append(sim.Changes, ports.PolicySimulationChange{
				RecommendationID: rec.ID, Title: rec.Title,
				From: fromDecision.Effect, To: toDecision.Effect,
				MonthlySaving: rec.EstimatedMonthlySaving,
			})
		}
	}

	return sim, nil
}

// loadOpenRecommendations pages through the tenant's open and under-review
// recommendations up to maxSimulationRecommendations, reporting whether the
// cap truncated the result.
func (s *Service) loadOpenRecommendations(ctx context.Context, tenant core.TenantID) ([]optimize.Recommendation, bool, error) {
	filter := ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen, optimize.StatusUnderReview}}
	var out []optimize.Recommendation
	cursor := ""
	for {
		page, err := s.d.Recommendations.List(ctx, tenant, filter, ports.ListOptions{Limit: 500, Cursor: cursor})
		if err != nil {
			return nil, false, err
		}
		out = append(out, page.Items...)
		if len(out) >= maxSimulationRecommendations {
			return out[:maxSimulationRecommendations], true, nil
		}
		if page.NextCursor == "" || len(page.Items) == 0 {
			return out, false, nil
		}
		cursor = page.NextCursor
	}
}

func (s *Service) resourceFor(ctx context.Context, tenant core.TenantID, id core.ID, cache map[core.ID]cloud.Resource) (cloud.Resource, error) {
	if res, ok := cache[id]; ok {
		return res, nil
	}
	res, err := s.d.Resources.Get(ctx, tenant, id)
	if err != nil {
		return cloud.Resource{}, err
	}
	cache[id] = res
	return res, nil
}
