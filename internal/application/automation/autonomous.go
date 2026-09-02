package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/application/governance"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// maxAutonomousChangesPerCycle is the loop's own blast-radius cap,
// independent of anything a tenant's specification declares. Every other
// limit in this file (concurrency, daily and monthly impact budgets,
// maintenance windows) is tenant-configurable and could in principle be
// misconfigured wide open; this one cannot be, because it exists precisely
// to bound the damage a misconfiguration — or a bug upstream that made every
// recommendation in a large estate look auto-executable — can do in one
// unattended pass. See doc.go.
const maxAutonomousChangesPerCycle = 20

// maxAutonomousCandidates bounds how many auto-executable recommendations
// one cycle even loads before applying the per-cycle cap, so a tenant with
// thousands of open recommendations does not make ProcessAutonomous itself
// slow merely to decide it will act on twenty of them.
const maxAutonomousCandidates = 500

// defaultMaxConcurrentChanges is used when a tenant's specification does not
// declare Automation.MaxConcurrentChanges. It is deliberately small: an
// unattended loop that can only ever have one or two changes in flight at
// once is easy to reason about even when something downstream goes wrong.
const defaultMaxConcurrentChanges = 2

// ProcessAutonomous is the unattended entry point: it finds recommendations
// the current policy actually authorises for auto-execution right now,
// filters them against the tenant's maintenance windows, concurrency limit
// and impact budgets, and plans and executes the survivors — one
// recommendation at a time, re-evaluating governance and re-checking the
// caps before each one, never as a single batch decision made once at the
// top of the loop.
//
// This doc comment used to claim that govern.Evaluate could never return
// EffectAutoExecute for a validated policy, so that every candidate here
// would be skipped as "not_policy_authorized". That claim is wrong, and it
// was worth removing rather than leaving as a caveat: Evaluate tracks the
// most restrictive *matching* rule separately from DefaultEffect and only
// falls back to the default when nothing matched (see its own comment on
// exactly this point), so a matching auto_execute rule does decide the
// outcome. A stale "this can never fire" note on the autonomous loop is the
// kind of thing that makes a genuine zero look expected, which is precisely
// how the shipped demo reported "0 auto-executable" for as long as it did.
//
// What does gate this loop, and did suppress it in practice: the tenant's
// automation switch, whether any recommendation is in an environment the
// policy pack is willing to act in unattended, and the maintenance windows
// the specification declares for that environment.
func (s *Service) ProcessAutonomous(ctx context.Context, tenant core.TenantID) (ports.AutonomousRunResult, error) {
	started := s.d.Clock.Now()
	result := ports.AutonomousRunResult{SkipReasons: map[string]int{}, MonthlySaving: core.ZeroUSD()}

	sp, err := s.loadActiveSpec(ctx, tenant)
	if err != nil {
		return result, err
	}
	if !sp.Automation.Enabled {
		return result, nil
	}

	page, err := s.d.Recommendations.List(ctx, tenant,
		ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen}, AutoExecutableOnly: true},
		ports.ListOptions{Limit: maxAutonomousCandidates})
	if err != nil {
		return result, fmt.Errorf("automation: listing auto-executable recommendations: %w", err)
	}
	result.Considered = len(page.Items)

	now := s.d.Clock.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	daySpent, err := s.autonomousSpendSince(ctx, tenant, core.NewPeriod(dayStart, now))
	if err != nil {
		s.d.Logger.Warn("automation: computing today's autonomous spend failed", "tenant", tenant, "error", err)
	}
	monthSpent, err := s.autonomousSpendSince(ctx, tenant, core.MonthOf(now))
	if err != nil {
		s.d.Logger.Warn("automation: computing this month's autonomous spend failed", "tenant", tenant, "error", err)
	}
	maxConcurrent := sp.Automation.MaxConcurrentChanges
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentChanges
	}
	// spec.Automation carries only a monthly impact cap; a daily slice is
	// derived from it (1/30th) rather than requiring tenants to declare a
	// second number for the same intent. This is a deliberate, documented
	// simplification — see doc.go's note on the autonomous loop's own
	// blast radius.
	monthlyBudget := sp.Automation.MaxMonthlyImpact
	dailyBudget := monthlyBudget / 30

	for idx, rec := range page.Items {
		if result.Planned >= maxAutonomousChangesPerCycle {
			remaining := len(page.Items) - idx
			result.Skipped += remaining
			result.SkipReasons["cycle_limit_reached"] += remaining
			break
		}

		if !rec.CountsTowardTotal() {
			// This recommendation is one of several mutually exclusive ways
			// to fix the same problem, and CloudOptix recommends a different
			// one. Choosing between alternatives is a judgement call — a team
			// may prefer fixing pod requests to shrinking a node group — and
			// it is exactly the judgement the platform promised to leave with
			// a person. An unattended run therefore never picks an
			// alternative; it acts only on the primary, whose saving is also
			// the only one that would count toward the impact caps below.
			result.Skipped++
			result.SkipReasons["mutually_exclusive_alternative"]++
			continue
		}

		decision, err := s.d.Governance.Evaluate(ctx, tenant, rec.ID)
		if err != nil {
			result.Skipped++
			result.SkipReasons["governance_evaluation_failed"]++
			s.d.Logger.Warn("automation: autonomous governance evaluation failed", "recommendation", rec.ID, "error", err)
			continue
		}
		if decision.Effect != govern.EffectAutoExecute {
			result.Skipped++
			result.SkipReasons["not_policy_authorized"]++
			continue
		}

		res, err := s.d.Resources.Get(ctx, tenant, rec.Finding.ResourceID)
		if err != nil {
			result.Skipped++
			result.SkipReasons["resource_unavailable"]++
			continue
		}

		if len(sp.Automation.MaintenanceWindows) > 0 {
			w, inWindow := governance.InMaintenanceWindow(sp, res.Environment, now)
			if !inWindow {
				result.Skipped++
				result.SkipReasons["outside_maintenance_window"]++
				continue
			}
			if len(decision.MaintenanceWindows) > 0 && !containsStr(decision.MaintenanceWindows, w.Name) {
				result.Skipped++
				result.SkipReasons["outside_maintenance_window"]++
				continue
			}
		}

		inFlight, err := s.countInFlightPlans(ctx, tenant)
		if err != nil {
			s.d.Logger.Warn("automation: counting in-flight plans failed", "tenant", tenant, "error", err)
		}
		if inFlight >= maxConcurrent {
			result.Skipped++
			result.SkipReasons["concurrency_cap_reached"]++
			continue
		}

		if monthlyBudget > 0 && monthSpent.Units()+rec.EstimatedMonthlySaving.Units() > monthlyBudget {
			result.Skipped++
			result.SkipReasons["monthly_impact_cap_reached"]++
			continue
		}
		if dailyBudget > 0 && daySpent.Units()+rec.EstimatedMonthlySaving.Units() > dailyBudget {
			result.Skipped++
			result.SkipReasons["daily_budget_cap_reached"]++
			continue
		}

		plan, err := s.PlanExecution(ctx, tenant, rec.ID, ports.PlanOptions{RequestedBy: systemActor})
		if err != nil {
			result.Failed++
			result.SkipReasons["plan_failed"]++
			s.d.Logger.Warn("automation: autonomous planning failed", "recommendation", rec.ID, "error", err)
			continue
		}
		result.Planned++
		if plan.State != execute.PlanApproved {
			// PlanExecution re-evaluates governance independently and can
			// legitimately disagree with the snapshot decision this loop
			// just read a moment earlier — treat that race conservatively:
			// the plan now sits awaiting a human approval rather than being
			// executed unattended.
			result.SkipReasons["awaiting_approval_after_replan"]++
			continue
		}

		executed, err := s.Execute(ctx, tenant, plan.ID, systemActor)
		if err != nil {
			result.Failed++
			if executed.State == execute.PlanRolledBack {
				result.RolledBack++
			}
			s.d.Logger.Warn("automation: autonomous execution failed", "plan", plan.ID, "error", err)
			continue
		}
		result.Executed++
		result.MonthlySaving = result.MonthlySaving.MustAdd(rec.EstimatedMonthlySaving)
		daySpent = daySpent.MustAdd(rec.EstimatedMonthlySaving)
		monthSpent = monthSpent.MustAdd(rec.EstimatedMonthlySaving)
		// Validation is not run here: it happens once the plan's declared
		// observation window has actually elapsed, which
		// ExecutionRepository.ClaimPlansAwaitingValidation exists to
		// schedule. The plan already sits at PlanExecuted, which is exactly
		// the state that claim query looks for.
	}

	result.DurationMS = s.d.Clock.Now().Sub(started).Milliseconds()
	return result, nil
}

// autonomousSpendSince sums the monthly-saving amount of every savings
// record that reached at least StageExecuted within the period and was not
// subsequently lost, which is this package's running tally against a
// tenant's impact budget: money already committed to an executed change,
// whether or not it has been validated yet.
func (s *Service) autonomousSpendSince(ctx context.Context, tenant core.TenantID, period core.Period) (core.Money, error) {
	records, err := s.d.Savings.List(ctx, tenant, period)
	if err != nil {
		return core.ZeroUSD(), err
	}
	total := core.ZeroUSD()
	for _, r := range records {
		if r.Lost || r.Stage.Order() < execute.StageExecuted.Order() {
			continue
		}
		amount := r.ExecutedMonthly
		if amount.IsZero() {
			amount = r.PotentialMonthly
		}
		total = total.MustAdd(amount)
	}
	return total, nil
}

// countInFlightPlans counts plans this tenant currently has mid-flight
// (preflighting, executing or validating), which is what MaxConcurrentChanges
// actually bounds — a plan that is only drafted or awaiting approval has not
// touched AWS yet and does not count against concurrency.
func (s *Service) countInFlightPlans(ctx context.Context, tenant core.TenantID) (int, error) {
	page, err := s.d.Executions.ListPlans(ctx, tenant,
		[]execute.PlanState{execute.PlanPreflight, execute.PlanExecuting, execute.PlanValidating},
		ports.ListOptions{Limit: 500})
	if err != nil {
		return 0, err
	}
	return len(page.Items), nil
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
