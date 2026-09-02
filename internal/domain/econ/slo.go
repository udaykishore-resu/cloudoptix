package econ

import (
	"fmt"
	"math"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func formatPct(format string, v float64) string { return fmt.Sprintf(format, v) }

// SLOKind names the unit a Cost SLO is expressed in. Keeping these separate
// matters because they behave differently: an absolute budget resets monthly,
// a per-unit target is scale-invariant, and an efficiency target is a ratio
// that can improve while absolute spend grows.
type SLOKind string

const (
	// SLOAbsoluteSpend caps total spend for a scope over a period.
	// "Production infrastructure <= $100K/month".
	SLOAbsoluteSpend SLOKind = "absolute_spend"
	// SLOCostPerTransaction caps unit economics.
	// "Checkout <= $0.02/transaction".
	SLOCostPerTransaction SLOKind = "cost_per_transaction"
	// SLOCostPerRequest caps API-level unit cost.
	SLOCostPerRequest SLOKind = "cost_per_request"
	// SLOCostPerCustomer caps cost-to-serve.
	SLOCostPerCustomer SLOKind = "cost_per_customer"
	// SLOWasteRatio caps the proportion of spend classified as waste.
	SLOWasteRatio SLOKind = "waste_ratio"
	// SLOEfficiencyScore floors the Cloud Efficiency Score.
	SLOEfficiencyScore SLOKind = "efficiency_score"
)

// Direction says whether the objective is an upper or lower bound. Most cost
// SLOs are ceilings; the efficiency-score SLO is a floor.
type Direction string

const (
	DirectionAtMost  Direction = "at_most"
	DirectionAtLeast Direction = "at_least"
)

// CostSLO is a service level objective expressed in money.
//
// The insight this encodes: reliability engineering made availability a
// managed, budgeted quantity instead of a hope. Cost deserves the same
// treatment. A Cost SLO plus an error budget turns "we should spend less" into
// a measurable commitment with a defined response when it is breached.
//
// Traceability: REQ-SLO-001..006, SPEC-ECON-003.
type CostSLO struct {
	ID          core.ID       `json:"id"`
	TenantID    core.TenantID `json:"tenant_id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Kind        SLOKind       `json:"kind"`
	Direction   Direction     `json:"direction"`

	Scope   Scope   `json:"scope"`
	ScopeID core.ID `json:"scope_id,omitempty"`
	// TransactionID is required for per-transaction objectives.
	TransactionID core.ID `json:"transaction_id,omitempty"`

	// Target is the objective value. For money-denominated kinds it is a
	// Money; for ratio kinds (waste, efficiency) TargetRatio is used instead.
	Target      core.Money `json:"target,omitempty"`
	TargetRatio float64    `json:"target_ratio,omitempty"`

	// Window is the evaluation period, normally a calendar month.
	Window SLOWindow `json:"window"`
	// ErrorBudgetPct is the tolerated variance above target, expressed as a
	// fraction of the target. 0.05 means a $100K target carries a $5K budget.
	ErrorBudgetPct float64 `json:"error_budget_pct"`

	// BreachActions declare what CloudOptix does when the budget is exhausted.
	// Declaring the response in advance is what makes the budget meaningful;
	// a budget with no consequence is a chart.
	BreachActions []BreachAction `json:"breach_actions,omitempty"`

	Owner     string    `json:"owner,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SLOWindow is the objective's evaluation window.
type SLOWindow string

const (
	WindowCalendarMonth   SLOWindow = "calendar_month"
	WindowRolling30d      SLOWindow = "rolling_30d"
	WindowRolling7d       SLOWindow = "rolling_7d"
	WindowCalendarQuarter SLOWindow = "calendar_quarter"
)

// Period resolves the window to a concrete interval.
func (w SLOWindow) Period(now time.Time) core.Period {
	switch w {
	case WindowRolling7d:
		return core.PeriodOfDays(now, 7)
	case WindowRolling30d:
		return core.PeriodOfDays(now, 30)
	case WindowCalendarQuarter:
		u := now.UTC()
		q := (int(u.Month()) - 1) / 3
		start := time.Date(u.Year(), time.Month(q*3+1), 1, 0, 0, 0, 0, time.UTC)
		return core.Period{Start: start, End: start.AddDate(0, 3, 0)}
	default:
		return core.MonthOf(now)
	}
}

// BreachAction is a declared response to budget exhaustion.
type BreachAction string

const (
	// ActionNotify raises an alert to the SLO owner and the tenant channel.
	ActionNotify BreachAction = "notify"
	// ActionRequireApproval escalates every cost-increasing change to human
	// approval, even ones the policy would normally auto-approve.
	ActionRequireApproval BreachAction = "require_approval"
	// ActionFreezeIncreases refuses cost-increasing changes outright until
	// the window resets or the budget is restored.
	ActionFreezeIncreases BreachAction = "freeze_cost_increases"
	// ActionGenerateRecommendations triggers an out-of-band optimization run
	// scoped to the breaching entity.
	ActionGenerateRecommendations BreachAction = "generate_recommendations"
	// ActionOpenInvestigation creates a tracked investigation record with the
	// anomaly decomposition attached.
	ActionOpenInvestigation BreachAction = "open_investigation"
)

// BudgetState is the health of an economic error budget.
type BudgetState string

const (
	BudgetHealthy   BudgetState = "healthy"   // < 50% consumed
	BudgetWatch     BudgetState = "watch"     // 50-75%
	BudgetAtRisk    BudgetState = "at_risk"   // 75-100%
	BudgetExhausted BudgetState = "exhausted" // >= 100%
	BudgetBreached  BudgetState = "breached"  // target itself exceeded
	BudgetUnknown   BudgetState = "unknown"   // insufficient data
)

// EconomicErrorBudget is the evaluated state of a Cost SLO for one window.
//
// The burn-rate projection is the part that makes this operationally useful:
// knowing that 40% of the budget is gone on day 6 of a 30-day month is a
// signal to act, and knowing the projected end-of-window position is what
// turns it into a decision.
//
// Traceability: REQ-SLO-004, SPEC-ECON-004.
type EconomicErrorBudget struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	SLOID    core.ID       `json:"slo_id"`
	SLOName  string        `json:"slo_name"`
	Kind     SLOKind       `json:"kind"`
	Period   core.Period   `json:"period"`

	Target        core.Money `json:"target"`
	BudgetAmount  core.Money `json:"budget_amount"`
	Actual        core.Money `json:"actual"`
	Consumed      core.Money `json:"consumed"`
	Remaining     core.Money `json:"remaining"`
	ConsumedRatio float64    `json:"consumed_ratio"`

	// BurnRate is consumption relative to elapsed time. 1.0 means the budget
	// will land exactly at zero at the end of the window; 2.0 means it burns
	// twice as fast as the window elapses.
	BurnRate float64 `json:"burn_rate"`
	// ProjectedEndOfWindow extrapolates actual spend to the window end using
	// the observed burn rate.
	ProjectedEndOfWindow core.Money `json:"projected_end_of_window"`
	ProjectedOverage     core.Money `json:"projected_overage"`
	ExhaustionDate       *time.Time `json:"exhaustion_date,omitempty"`

	State            BudgetState    `json:"state"`
	TriggeredActions []BreachAction `json:"triggered_actions,omitempty"`
	Explanation      string         `json:"explanation"`
	EvaluatedAt      time.Time      `json:"evaluated_at"`
}

// EvaluateBudget computes the error-budget position for an absolute-spend or
// per-unit objective. Elapsed is the fraction of the window that has passed.
func EvaluateBudget(slo CostSLO, actual core.Money, now time.Time) EconomicErrorBudget {
	period := slo.Window.Period(now)
	b := EconomicErrorBudget{
		ID:          core.NewID("eeb"),
		TenantID:    slo.TenantID,
		SLOID:       slo.ID,
		SLOName:     slo.Name,
		Kind:        slo.Kind,
		Period:      period,
		Target:      slo.Target,
		Actual:      actual,
		EvaluatedAt: now.UTC(),
	}
	budgetPct := slo.ErrorBudgetPct
	if budgetPct <= 0 {
		budgetPct = 0.05 // a 5% tolerance is the platform default
	}
	b.BudgetAmount = slo.Target.Scale(budgetPct)

	// Consumption is the amount by which actual exceeds a pro-rated target.
	// Pro-rating matters: comparing month-to-date spend against a full-month
	// target would report every SLO as healthy until the 28th.
	elapsed := elapsedFraction(period, now)
	if elapsed <= 0 {
		b.State = BudgetUnknown
		b.Explanation = "window has not started"
		return b
	}
	proRatedTarget := slo.Target.Scale(elapsed)
	overage := actual.MustSub(proRatedTarget)
	if overage.IsNegative() {
		overage = core.ZeroUSD()
	}
	b.Consumed = overage
	b.Remaining = b.BudgetAmount.MustSub(overage)
	if !b.BudgetAmount.IsZero() {
		b.ConsumedRatio = overage.Ratio(b.BudgetAmount)
	}

	// Burn rate compares budget consumption pace against window elapse pace.
	if elapsed > 0 && !b.BudgetAmount.IsZero() {
		b.BurnRate = b.ConsumedRatio / elapsed
	}

	if elapsed > 0 {
		b.ProjectedEndOfWindow = actual.Div(elapsed)
		over := b.ProjectedEndOfWindow.MustSub(slo.Target)
		if !over.IsNegative() {
			b.ProjectedOverage = over
		} else {
			b.ProjectedOverage = core.ZeroUSD()
		}
	}

	// Project the date on which the budget hits zero, if it will.
	if b.BurnRate > 1 && b.ConsumedRatio < 1 {
		remainingFraction := (1 - b.ConsumedRatio) / b.BurnRate
		windowLen := period.End.Sub(period.Start)
		exhaustAt := now.UTC().Add(time.Duration(remainingFraction * float64(windowLen)))
		if exhaustAt.Before(period.End) {
			b.ExhaustionDate = &exhaustAt
		}
	}

	switch {
	case actual.GreaterThan(slo.Target):
		b.State = BudgetBreached
	case b.ConsumedRatio >= 1:
		b.State = BudgetExhausted
	case b.ConsumedRatio >= 0.75:
		b.State = BudgetAtRisk
	case b.ConsumedRatio >= 0.5:
		b.State = BudgetWatch
	default:
		b.State = BudgetHealthy
	}

	if b.State == BudgetExhausted || b.State == BudgetBreached {
		b.TriggeredActions = slo.BreachActions
		if len(b.TriggeredActions) == 0 {
			b.TriggeredActions = []BreachAction{ActionNotify, ActionRequireApproval}
		}
	}
	b.Explanation = explainBudget(b, elapsed)
	return b
}

func elapsedFraction(p core.Period, now time.Time) float64 {
	total := p.End.Sub(p.Start)
	if total <= 0 {
		return 0
	}
	done := now.UTC().Sub(p.Start)
	if done <= 0 {
		return 0
	}
	if done >= total {
		return 1
	}
	return float64(done) / float64(total)
}

func explainBudget(b EconomicErrorBudget, elapsed float64) string {
	switch b.State {
	case BudgetHealthy:
		return fmt.Sprintf("%.0f%% of the window elapsed, %.0f%% of the economic error budget consumed.",
			elapsed*100, b.ConsumedRatio*100)
	case BudgetWatch, BudgetAtRisk:
		msg := fmt.Sprintf("%.0f%% of the economic error budget consumed at %.0f%% of the window (burn rate %.1fx).",
			b.ConsumedRatio*100, elapsed*100, b.BurnRate)
		if b.ExhaustionDate != nil {
			msg += " Projected exhaustion " + b.ExhaustionDate.Format("Jan 2") + "."
		}
		return msg
	case BudgetExhausted:
		return fmt.Sprintf("Economic error budget exhausted. Projected end-of-window spend %s against a %s target.",
			b.ProjectedEndOfWindow.Format(), b.Target.Format())
	case BudgetBreached:
		return fmt.Sprintf("Cost SLO breached: actual %s exceeds the %s target with %.0f%% of the window remaining.",
			b.Actual.Format(), b.Target.Format(), (1-elapsed)*100)
	default:
		return "Insufficient data to evaluate."
	}
}

// AllowsCostIncrease reports whether a cost-increasing change may proceed
// without escalation given the budget state. The policy engine consults every
// budget touching the affected scope; any freeze wins.
func (b EconomicErrorBudget) AllowsCostIncrease() (allowed bool, requiresApproval bool) {
	for _, a := range b.TriggeredActions {
		switch a {
		case ActionFreezeIncreases:
			return false, false
		case ActionRequireApproval:
			requiresApproval = true
		}
	}
	return true, requiresApproval
}

// EfficiencyFactor is one weighted input to the Cloud Efficiency Score.
type EfficiencyFactor struct {
	Name        string     `json:"name"`
	Score       float64    `json:"score"`  // 0..100
	Weight      float64    `json:"weight"` // weights across factors sum to 1
	Detail      string     `json:"detail"`
	Opportunity core.Money `json:"opportunity,omitempty"`
}

// EfficiencyScore is the 0-100 headline health metric.
//
// It is a weighted composite rather than a single ratio because a single ratio
// is gameable and uninformative: an estate can have perfect utilisation and
// still waste a fortune on unattached volumes, unamortised commitments and
// cross-AZ chatter. Each factor is reported alongside the composite so the
// score is explainable and each point of improvement maps to an action.
//
// Traceability: REQ-ECON-010, SPEC-ECON-005.
type EfficiencyScore struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	Scope    Scope         `json:"scope"`
	ScopeID  core.ID       `json:"scope_id,omitempty"`
	Label    string        `json:"label"`
	Period   core.Period   `json:"period"`

	Score      float64            `json:"score"`
	Grade      string             `json:"grade"`
	Factors    []EfficiencyFactor `json:"factors"`
	PriorScore float64            `json:"prior_score,omitempty"`
	Delta      float64            `json:"delta,omitempty"`
	// WasteRatio is the share of spend attributable to findings CloudOptix
	// classifies as waste. It is the single number executives ask for.
	WasteRatio      float64    `json:"waste_ratio"`
	TotalSpend      core.Money `json:"total_spend"`
	IdentifiedWaste core.Money `json:"identified_waste"`
	ComputedAt      time.Time  `json:"computed_at"`
}

// StandardFactorWeights is the default weighting. It is configurable per
// tenant, because a serverless-first startup and a lift-and-shift estate do
// not have the same levers.
var StandardFactorWeights = map[string]float64{
	"resource_utilization":    0.22,
	"waste_elimination":       0.20,
	"commitment_coverage":     0.15,
	"storage_efficiency":      0.10,
	"network_efficiency":      0.10,
	"architecture_efficiency": 0.10,
	"automation_maturity":     0.07,
	"governance_maturity":     0.06,
}

// ComputeEfficiencyScore combines the weighted factors.
func ComputeEfficiencyScore(tenant core.TenantID, scope Scope, scopeID core.ID, label string, period core.Period, factors []EfficiencyFactor, totalSpend, waste core.Money) EfficiencyScore {
	s := EfficiencyScore{
		ID:              core.NewID("ces"),
		TenantID:        tenant,
		Scope:           scope,
		ScopeID:         scopeID,
		Label:           label,
		Period:          period,
		Factors:         factors,
		TotalSpend:      totalSpend,
		IdentifiedWaste: waste,
		ComputedAt:      time.Now().UTC(),
	}
	var weighted, totalWeight float64
	for _, f := range factors {
		weighted += math.Max(0, math.Min(100, f.Score)) * f.Weight
		totalWeight += f.Weight
	}
	if totalWeight > 0 {
		s.Score = weighted / totalWeight
	}
	if !totalSpend.IsZero() {
		s.WasteRatio = waste.Ratio(totalSpend)
	}
	s.Grade = grade(s.Score)
	return s
}

func grade(score float64) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	default:
		return "F"
	}
}
