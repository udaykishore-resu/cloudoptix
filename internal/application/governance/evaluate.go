package governance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// Evaluate assembles a complete govern.Input for a recommendation and runs it
// through the tenant's active policy, persisting the resulting Decision and
// writing an audit record. It is the single call site in the whole platform
// where a recommendation is turned into a governance verdict — the execution
// engine re-runs it immediately before touching AWS rather than trusting a
// verdict computed earlier (see the automation package's Execute), but every
// evaluation, first or repeated, goes through exactly this assembly.
func (s *Service) Evaluate(ctx context.Context, tenant core.TenantID, recommendationID core.ID) (govern.Decision, error) {
	rec, err := s.d.Recommendations.Get(ctx, tenant, recommendationID)
	if err != nil {
		return govern.Decision{}, fmt.Errorf("governance: loading recommendation: %w", err)
	}
	res, err := s.d.Resources.Get(ctx, tenant, rec.Finding.ResourceID)
	if err != nil {
		return govern.Decision{}, fmt.Errorf("governance: loading resource: %w", err)
	}
	sp, err := s.loadActiveSpec(ctx, tenant)
	if err != nil {
		return govern.Decision{}, fmt.Errorf("governance: loading active specification: %w", err)
	}
	policy, policyIsFallback, err := s.loadActivePolicyOrFallback(ctx, tenant)
	if err != nil {
		return govern.Decision{}, fmt.Errorf("governance: loading active policy: %w", err)
	}
	budgetFreeze, budgetRequiresApproval := s.matchBudgets(ctx, tenant, res)

	now := s.d.Clock.Now()
	in, err := buildInput(tenant, rec, res, sp, budgetFreeze, budgetRequiresApproval, now)
	if err != nil {
		return govern.Decision{}, fmt.Errorf("governance: assembling policy input: %w", err)
	}

	decision := govern.Evaluate(policy, in)
	if policyIsFallback {
		decision.Explanation = append(decision.Explanation,
			"no active governance policy is set for this tenant; defaulting to manual approval for every change")
	}
	applySpecificationGuards(&decision, sp, res, rec, now)

	if err := s.d.Policies.SaveDecision(ctx, decision); err != nil {
		return govern.Decision{}, fmt.Errorf("governance: persisting policy decision: %w", err)
	}

	rec.RequiresApproval = decision.RequiresApproval
	rec.PolicyDecisionID = decision.ID
	rec.AutoExecutable = decision.Effect == govern.EffectAutoExecute
	rec.UpdatedAt = now
	if err := s.d.Recommendations.Update(ctx, rec); err != nil {
		s.d.Logger.Warn("governance: updating recommendation with policy decision failed", "recommendation", rec.ID, "error", err)
	}

	s.writeAudit(ctx, tenant, audit.ActionPolicyEvaluated, audit.OutcomeSuccess, systemActor, "recommendation", rec.ID,
		fmt.Sprintf("policy decision %s for recommendation %s: %s (%s)", decision.ID, rec.ID, decision.Effect, decision.Reason),
		map[string]any{"policy_decision_id": string(decision.ID), "effect": string(decision.Effect)})

	return decision, nil
}

// requiredInputFields is checked by buildInput before it will return an
// Input at all. Every one of these is read by a govern.Match guard
// somewhere in the domain package — RiskLevel by MaxRiskLevel,
// Reversibility by MinReversibility, AccountID/Region/Environment by their
// own selector fields — so a zero value here is never "no opinion", it is a
// guard silently evaluating against the empty string or zero float and
// therefore either always or never matching depending on the operator. The
// domain package has no way to detect that from inside Match.Matches: it
// receives a struct and trusts every field was actually populated. This
// function is where that trust is earned, and where a violation fails
// closed — Evaluate returns an error and no Decision is produced or
// persisted, rather than one built from an Input that understates the
// change's own risk.
func requiredInputFieldsOK(in govern.Input) []string {
	var missing []string
	if in.TenantID.IsZero() {
		missing = append(missing, "tenant_id")
	}
	if in.RecommendationID.IsZero() {
		missing = append(missing, "recommendation_id")
	}
	if in.RuleID == "" {
		missing = append(missing, "rule_id")
	}
	if in.Action == "" {
		missing = append(missing, "action")
	}
	if in.ResourceID.IsZero() {
		missing = append(missing, "resource_id")
	}
	if in.AccountID == "" {
		missing = append(missing, "account_id")
	}
	if in.Environment == "" {
		missing = append(missing, "environment")
	}
	if in.RiskLevel == "" {
		missing = append(missing, "risk_level")
	}
	if in.Reversibility == "" {
		missing = append(missing, "reversibility")
	}
	if in.Now.IsZero() {
		missing = append(missing, "now")
	}
	return missing
}

// buildInput assembles a govern.Input from a recommendation, its resource,
// the tenant's approved specification and the resource's matched economic
// error budget signal (see matchBudgets). See requiredInputFieldsOK for the
// fail-closed contract.
func buildInput(tenant core.TenantID, rec optimize.Recommendation, res cloud.Resource, sp spec.Spec, budgetFreeze, budgetRequiresApproval bool, now time.Time) (govern.Input, error) {
	in := govern.Input{
		TenantID:         tenant,
		RecommendationID: rec.ID,
		RuleID:           rec.Finding.RuleID,
		Category:         rec.Finding.Category,
		Action:           rec.Action,
		ResourceID:       rec.Finding.ResourceID,
		ResourceKind:     string(rec.Finding.ResourceKind),
		AccountID:        rec.Finding.AccountID,
		Region:           rec.Finding.Region,
		Environment:      rec.Finding.Environment,
		ApplicationID:    res.ApplicationID,
		Tags:             res.Tags,

		Confidence:       rec.Confidence,
		RiskLevel:        rec.Risk.Level,
		RiskScore:        rec.Risk.Score,
		BlastScore:       rec.BlastRadius.Score,
		CriticalServices: rec.BlastRadius.CriticalServices,
		Reversibility:    rec.Reversibility,
		Destructive:      rec.Action.Destructive(),
		MonthlySaving:    rec.EstimatedMonthlySaving,
		MonthlyCostDelta: monthlyCostDelta(rec),

		InMaintenanceWindow: windowMatched(sp, rec.Finding.Environment, now),

		BudgetFreeze:           budgetFreeze,
		BudgetRequiresApproval: budgetRequiresApproval,
		AutomationEnabled:      sp.Automation.Enabled,
		RequestedBy:            rec.Finding.RuleName,
		Now:                    now,
	}
	if missing := requiredInputFieldsOK(in); len(missing) > 0 {
		return govern.Input{}, core.Invalid("policy input is missing required field(s): %v; refusing to evaluate", missing).
			WithDetail("missing_fields", missing).
			WithDetail("recommendation_id", string(rec.ID))
	}
	return in, nil
}

func windowMatched(sp spec.Spec, env core.Environment, now time.Time) bool {
	_, ok := InMaintenanceWindow(sp, env, now)
	return ok
}

// monthlyCostDelta is the signed change in run-rate the change would cause:
// positive means the change costs more, negative means it saves money. A
// recommendation's own EstimatedMonthlySaving is a positive quantity by
// convention (SPEC-OPT-001), so the reduction case is its negation; a rule
// that has priced the proposed state explicitly (StateSnapshot.MonthlyCost
// on both sides) is preferred when available since it captures a
// cost-increasing change — extra redundancy, a bigger commitment — that a
// pure "saving" framing cannot represent at all.
func monthlyCostDelta(rec optimize.Recommendation) core.Money {
	if !rec.ProposedState.MonthlyCost.IsZero() || !rec.CurrentState.MonthlyCost.IsZero() {
		return rec.ProposedState.MonthlyCost.MustSub(rec.CurrentState.MonthlyCost)
	}
	return core.ZeroUSD().MustSub(rec.EstimatedMonthlySaving)
}

// matchBudgets scans the tenant's current economic error budget states and
// reports whether any budget touching this resource's scope is frozen or
// merely requiring approval. Any single matched budget's freeze wins
// outright, matching econ.EconomicErrorBudget.AllowsCostIncrease's own doc
// comment: "any freeze wins".
//
// Only organization-wide and application-scoped budgets are matched against
// the resource: an EconomicErrorBudget itself carries no scope, only the id
// of the CostSLO it was evaluated from, so the scope check requires one
// GetCostSLO lookup per budget. Account- and workload-scoped budgets would
// need to resolve the SLO's ScopeID against the AWS account and workload
// repositories this method does not otherwise load, so they are treated as
// unmatched here rather than guessed at — a deliberately honest limitation,
// not a silent one: extending matching to every econ.Scope is a matter of
// widening this method's dependencies, and until then a narrower-scoped
// budget still protects its own scope through the SLO's own breach actions
// (ActionGenerateRecommendations, notifications) even though it does not yet
// gate this specific evaluation call.
func (s *Service) matchBudgets(ctx context.Context, tenant core.TenantID, res cloud.Resource) (freeze bool, requireApproval bool) {
	if s.d.Economics == nil {
		return false, false
	}
	budgets, err := s.d.Economics.ListBudgetStates(ctx, tenant)
	if err != nil {
		s.d.Logger.Warn("governance: loading economic error budget states failed; evaluating without a budget signal", "error", err)
		return false, false
	}
	for _, b := range budgets {
		slo, err := s.d.Economics.GetCostSLO(ctx, tenant, b.SLOID)
		if err != nil {
			s.d.Logger.Warn("governance: loading cost SLO for budget scope resolution failed; skipping this budget", "slo_id", b.SLOID, "error", err)
			continue
		}
		matched := slo.Scope == econ.ScopeOrganization ||
			(slo.Scope == econ.ScopeApplication && !res.ApplicationID.IsZero() && slo.ScopeID == res.ApplicationID)
		if !matched {
			continue
		}
		allowed, needsApproval := b.AllowsCostIncrease()
		if !allowed {
			freeze = true
		}
		if needsApproval {
			requireApproval = true
		}
	}
	return freeze, requireApproval
}

// applySpecificationGuards tightens a Decision already produced by
// govern.Evaluate against constraints that live in the tenant's approved
// specification rather than in the policy document: excluded actions,
// excluded resources, excluded tags, and change-freeze windows. See the
// package doc comment for why these live here instead of as govern.Match
// vocabulary. Every branch can only move the decision to a stricter Effect
// (higher govern.Effect.Rank()); it is never used to loosen one a policy
// rule already produced.
func applySpecificationGuards(d *govern.Decision, sp spec.Spec, res cloud.Resource, rec optimize.Recommendation, now time.Time) {
	if actionExcluded(sp.Optimization.ExcludedActions, string(rec.Action)) {
		tighten(d, govern.EffectProhibit, "__spec_excluded_action__",
			fmt.Sprintf("action %q is excluded by the tenant's approved specification", rec.Action))
	}
	if resourceExcluded(sp.Optimization.ExcludedResources, res.ID, res.NativeID) {
		tighten(d, govern.EffectProhibit, "__spec_excluded_resource__",
			"this resource is excluded by the tenant's approved specification")
	}
	if match, ok := tagsExcluded(sp.Optimization.ExcludedTags, res.Tags); ok {
		tighten(d, govern.EffectProhibit, "__spec_excluded_tag__",
			fmt.Sprintf("resource tag %q is excluded by the tenant's approved specification", match))
	}
	if window, ok := InChangeFreeze(sp, now); ok && d.Effect == govern.EffectAutoExecute {
		tighten(d, govern.EffectRequireApproval, "__spec_change_freeze__",
			fmt.Sprintf("tenant change-freeze window %q is active; automatic execution is disabled and this change requires human approval", window))
	}
}

// tighten moves a Decision to a stricter effect, never a looser one, and
// keeps the Decision's derived fields (RequiresApproval, the deciding rule
// id, the human-readable reason) consistent with whatever Effect it ends up
// at — mirroring exactly what govern.Evaluate itself does when a platform
// invariant overrides a rule's own effect.
func tighten(d *govern.Decision, effect govern.Effect, deciding, reason string) {
	if effect.Rank() <= d.Effect.Rank() {
		d.Explanation = append(d.Explanation, reason)
		return
	}
	d.Effect = effect
	d.DecidingRule = deciding
	d.Reason = reason
	d.RequiresApproval = effect == govern.EffectRequireApproval
	if d.RequiresApproval && d.MinApprovals == 0 {
		d.MinApprovals = 1
	}
	if effect == govern.EffectProhibit {
		d.Approvers = nil
		d.MinApprovals = 0
		d.RequireDistinctApprover = false
	}
	d.Explanation = append(d.Explanation, reason)
}

func (s *Service) loadActiveSpec(ctx context.Context, tenant core.TenantID) (spec.Spec, error) {
	v, err := s.d.Specs.GetActive(ctx, tenant)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			// No approved specification means automation cannot possibly be
			// authorised (Spec.Automation.Enabled defaults false on the zero
			// value) and no maintenance window exists — both of which are the
			// safe, restrictive defaults this evaluation needs when a tenant
			// has not yet completed onboarding.
			s.d.Logger.Warn("governance: tenant has no active specification; evaluating with a fully restrictive default", "tenant", tenant)
			return spec.Spec{}, nil
		}
		return spec.Spec{}, err
	}
	return v.Spec, nil
}

// loadActivePolicyOrFallback returns the tenant's active policy, or — when
// none has ever been activated — a hardcoded, maximally restrictive fallback
// policy: no rules, default effect require_approval. This is the fail-closed
// choice between two bad options: erroring out of every recommendation
// evaluation until a tenant activates a policy pack would make the platform
// unusable during the gap between onboarding and the first policy choice,
// while silently permitting anything would be the opposite of what a missing
// policy should mean. Requiring manual approval for every change is the one
// behaviour that is safe in the absence of an authored policy.
func (s *Service) loadActivePolicyOrFallback(ctx context.Context, tenant core.TenantID) (govern.Policy, bool, error) {
	p, err := s.d.Policies.GetActive(ctx, tenant)
	if err == nil {
		return p, false, nil
	}
	if !errors.Is(err, core.ErrNotFound) {
		return govern.Policy{}, false, err
	}
	return govern.Policy{
		Name:          "no-active-policy-fallback",
		Description:   "platform fallback used when no policy has been activated for this tenant",
		DefaultEffect: govern.EffectRequireApproval,
		Enabled:       true,
	}, true, nil
}
