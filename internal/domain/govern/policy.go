// Package govern holds policy-as-code and the approval workflow: the
// deterministic gate that stands between a recommendation and an AWS API call.
//
// The architectural rule this package exists to enforce is that no AI output
// reaches AWS without passing a policy evaluation whose inputs are all
// structured facts. The policy engine never sees prose, never calls a model,
// and produces a decision record that is reproducible from its inputs alone —
// so a decision can be replayed months later during an audit and yield the
// same answer.
//
// Traceability: REQ-GOV-001..011, SPEC-GOV-001, SPEC-AI-004.
package govern

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// Effect is a rule's verdict.
type Effect string

const (
	// EffectAutoExecute permits execution with no human in the loop.
	EffectAutoExecute Effect = "auto_execute"
	// EffectRequireApproval permits execution after named approvers consent.
	EffectRequireApproval Effect = "require_approval"
	// EffectProhibit forbids the action entirely.
	EffectProhibit Effect = "prohibit"
	// EffectAdvisory allows the recommendation to be shown but never planned.
	EffectAdvisory Effect = "advisory_only"
)

// Rank orders effects by restrictiveness. Evaluation is deny-biased: when
// several rules match, the most restrictive wins regardless of order, so a
// permissive rule added later can never quietly widen an existing prohibition.
func (e Effect) Rank() int {
	switch e {
	case EffectAutoExecute:
		return 0
	case EffectRequireApproval:
		return 1
	case EffectAdvisory:
		return 2
	case EffectProhibit:
		return 3
	}
	return 3
}

// Match is a rule's selector. Every populated field must match; empty fields
// are wildcards. A rule with no selector at all matches everything, which is
// how the mandatory default-deny baseline rule is expressed.
type Match struct {
	Actions        []optimize.ActionType `json:"actions,omitempty" yaml:"actions,omitempty"`
	Categories     []optimize.Category   `json:"categories,omitempty" yaml:"categories,omitempty"`
	RuleIDs        []optimize.RuleID     `json:"rule_ids,omitempty" yaml:"rule_ids,omitempty"`
	Environments   []core.Environment    `json:"environments,omitempty" yaml:"environments,omitempty"`
	AccountIDs     []core.AccountID      `json:"account_ids,omitempty" yaml:"account_ids,omitempty"`
	Regions        []core.Region         `json:"regions,omitempty" yaml:"regions,omitempty"`
	ResourceKinds  []string              `json:"resource_kinds,omitempty" yaml:"resource_kinds,omitempty"`
	ApplicationIDs []core.ID             `json:"application_ids,omitempty" yaml:"application_ids,omitempty"`
	TagSelector    map[string]string     `json:"tag_selector,omitempty" yaml:"tag_selector,omitempty"`

	// Guards are numeric conditions on the recommendation itself. They are the
	// difference between "EC2 rightsizing is allowed" and the rule an
	// operations team actually wants: "EC2 rightsizing is allowed when
	// confidence is at least 90%, the blast radius touches no critical
	// service, and the change is reversible".
	MinConfidence            float64                `json:"min_confidence,omitempty" yaml:"min_confidence,omitempty"`
	MaxRiskLevel             core.RiskLevel         `json:"max_risk_level,omitempty" yaml:"max_risk_level,omitempty"`
	MaxBlastScore            float64                `json:"max_blast_score,omitempty" yaml:"max_blast_score,omitempty"`
	MaxCriticalServices      int                    `json:"max_critical_services,omitempty" yaml:"max_critical_services,omitempty"`
	MaxMonthlySavingImpact   core.Money             `json:"max_monthly_saving_impact,omitempty" yaml:"max_monthly_saving_impact,omitempty"`
	MinReversibility         optimize.Reversibility `json:"min_reversibility,omitempty" yaml:"min_reversibility,omitempty"`
	RequireMaintenanceWindow bool                   `json:"require_maintenance_window,omitempty" yaml:"require_maintenance_window,omitempty"`
}

// Rule is one policy statement.
type Rule struct {
	ID          string `json:"id" yaml:"id"`
	Description string `json:"description" yaml:"description"`
	Match       Match  `json:"match" yaml:"match"`
	Effect      Effect `json:"effect" yaml:"effect"`
	// Approvers names the roles or principals that may approve when the
	// effect is require_approval. Empty means any principal holding
	// approval:decide.
	Approvers    []string `json:"approvers,omitempty" yaml:"approvers,omitempty"`
	MinApprovals int      `json:"min_approvals,omitempty" yaml:"min_approvals,omitempty"`
	// RequireDistinctApprover forbids the requester approving their own
	// change, which is the segregation-of-duties control auditors ask for.
	RequireDistinctApprover bool     `json:"require_distinct_approver,omitempty" yaml:"require_distinct_approver,omitempty"`
	MaintenanceWindows      []string `json:"maintenance_windows,omitempty" yaml:"maintenance_windows,omitempty"`
	Reason                  string   `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// Policy is a versioned, tenant-scoped rule set.
type Policy struct {
	ID          core.ID       `json:"id"`
	TenantID    core.TenantID `json:"tenant_id"`
	Name        string        `json:"name" yaml:"name"`
	Description string        `json:"description,omitempty" yaml:"description,omitempty"`
	Version     int           `json:"version" yaml:"version"`
	Rules       []Rule        `json:"rules" yaml:"rules"`
	// DefaultEffect applies when no rule matches. It is require_approval by
	// default and validation refuses to let a tenant set it to auto_execute:
	// an unmatched action is by definition one nobody has thought about.
	DefaultEffect Effect    `json:"default_effect" yaml:"default_effect"`
	Enabled       bool      `json:"enabled" yaml:"enabled"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	ActivatedAt   time.Time `json:"activated_at,omitempty"`
	Checksum      string    `json:"checksum"`
}

// Validate enforces the policy invariants that keep the platform safe
// regardless of what a tenant writes.
func (p Policy) Validate() core.ValidationResult {
	var v core.ValidationResult
	if p.Name == "" {
		v.Add("name", "required", core.SeverityHigh, "policy name is required")
	}
	if p.DefaultEffect == EffectAutoExecute {
		v.AddHint("default_effect", "unsafe_default", core.SeverityCritical,
			"set default_effect to require_approval or prohibit",
			"a default effect of auto_execute would silently permit every action nobody wrote a rule for")
	}
	if p.DefaultEffect == "" {
		v.Add("default_effect", "required", core.SeverityHigh, "default_effect is required")
	}
	seen := map[string]bool{}
	for i, r := range p.Rules {
		path := fmt.Sprintf("rules[%d]", i)
		if r.ID == "" {
			v.Add(path+".id", "required", core.SeverityHigh, "each rule needs a stable id")
		} else if seen[r.ID] {
			v.Add(path+".id", "duplicate", core.SeverityHigh, "duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true

		switch r.Effect {
		case EffectAutoExecute, EffectRequireApproval, EffectProhibit, EffectAdvisory:
		default:
			v.Add(path+".effect", "invalid", core.SeverityHigh, "unknown effect %q", r.Effect)
		}

		// The hard platform invariant: a destructive action can never be
		// auto-executed, however a tenant writes their policy.
		if r.Effect == EffectAutoExecute {
			for _, a := range r.Match.Actions {
				if a.Destructive() {
					v.AddHint(path+".effect", "destructive_auto_execute", core.SeverityCritical,
						"change the effect to require_approval",
						"action %q is destructive and can never be auto-executed", a)
				}
			}
			if len(r.Match.Actions) == 0 && len(r.Match.Categories) == 0 && len(r.Match.RuleIDs) == 0 {
				v.AddHint(path+".match", "overbroad_auto_execute", core.SeverityCritical,
					"narrow the match to specific actions or categories",
					"an auto_execute rule must name the actions it permits")
			}
			// Auto-execution in production without a confidence guard is the
			// single most common way an optimization platform causes an
			// incident.
			prod := false
			for _, e := range r.Match.Environments {
				if e.IsProduction() {
					prod = true
				}
			}
			if (prod || len(r.Match.Environments) == 0) && r.Match.MinConfidence < 0.85 {
				v.AddHint(path+".match.min_confidence", "weak_production_guard", core.SeverityHigh,
					"set min_confidence to at least 0.85 for production auto-execution",
					"auto_execute reaching production requires a confidence guard")
			}
		}
		if r.Effect == EffectRequireApproval && r.MinApprovals < 0 {
			v.Add(path+".min_approvals", "invalid", core.SeverityMedium, "min_approvals cannot be negative")
		}
	}
	return v
}

// Matches evaluates a rule's selector against a decision input.
func (m Match) Matches(in Input) bool {
	if len(m.Actions) > 0 && !containsAction(m.Actions, in.Action) {
		return false
	}
	if len(m.Categories) > 0 && !containsCategory(m.Categories, in.Category) {
		return false
	}
	if len(m.RuleIDs) > 0 && !containsRuleID(m.RuleIDs, in.RuleID) {
		return false
	}
	if len(m.Environments) > 0 && !containsEnv(m.Environments, in.Environment) {
		return false
	}
	if len(m.AccountIDs) > 0 && !containsAccount(m.AccountIDs, in.AccountID) {
		return false
	}
	if len(m.Regions) > 0 && !containsRegion(m.Regions, in.Region) {
		return false
	}
	if len(m.ResourceKinds) > 0 && !containsString(m.ResourceKinds, in.ResourceKind) {
		return false
	}
	if len(m.ApplicationIDs) > 0 && !containsID(m.ApplicationIDs, in.ApplicationID) {
		return false
	}
	for k, want := range m.TagSelector {
		got, ok := in.Tags.Get(k)
		if !ok || (want != "" && want != "*" && !strings.EqualFold(got, want)) {
			return false
		}
	}
	// Guard conditions. A guard that the input fails means the rule does not
	// apply, which for an auto_execute rule means the change falls through to
	// a more restrictive rule or to the default effect.
	if m.MinConfidence > 0 && float64(in.Confidence) < m.MinConfidence {
		return false
	}
	if m.MaxRiskLevel != "" && in.RiskLevel.Order() > m.MaxRiskLevel.Order() {
		return false
	}
	if m.MaxBlastScore > 0 && in.BlastScore > m.MaxBlastScore {
		return false
	}
	if m.MaxCriticalServices > 0 && in.CriticalServices > m.MaxCriticalServices {
		return false
	}
	if !m.MaxMonthlySavingImpact.IsZero() && in.MonthlySaving.GreaterThan(m.MaxMonthlySavingImpact) {
		return false
	}
	if m.MinReversibility != "" && in.Reversibility.Factor() < m.MinReversibility.Factor() {
		return false
	}
	if m.RequireMaintenanceWindow && !in.InMaintenanceWindow {
		return false
	}
	return true
}

// Input is the structured fact set a policy decision is made from. It contains
// no free text and no model output by construction.
type Input struct {
	TenantID         core.TenantID
	RecommendationID core.ID
	RuleID           optimize.RuleID
	Category         optimize.Category
	Action           optimize.ActionType
	ResourceID       core.ID
	ResourceKind     string
	AccountID        core.AccountID
	Region           core.Region
	Environment      core.Environment
	ApplicationID    core.ID
	Tags             core.Tags

	Confidence       core.Confidence
	RiskLevel        core.RiskLevel
	RiskScore        float64
	BlastScore       float64
	CriticalServices int
	Reversibility    optimize.Reversibility
	Destructive      bool
	MonthlySaving    core.Money
	MonthlyCostDelta core.Money

	InMaintenanceWindow bool
	// BudgetFreeze and BudgetRequiresApproval are injected by the economics
	// engine from the tenant's economic error budgets. A frozen budget
	// overrides any permissive rule.
	BudgetFreeze           bool
	BudgetRequiresApproval bool
	// AutomationEnabled is the tenant's master switch from the onboarding
	// spec. When false, nothing auto-executes regardless of policy.
	AutomationEnabled bool
	RequestedBy       string
	Now               time.Time
}

// Decision is the reproducible outcome of a policy evaluation.
type Decision struct {
	ID               core.ID       `json:"id"`
	TenantID         core.TenantID `json:"tenant_id"`
	RecommendationID core.ID       `json:"recommendation_id"`
	PolicyID         core.ID       `json:"policy_id"`
	PolicyVersion    int           `json:"policy_version"`
	PolicyChecksum   string        `json:"policy_checksum"`

	Effect       Effect   `json:"effect"`
	MatchedRules []string `json:"matched_rules"`
	DecidingRule string   `json:"deciding_rule"`
	Reason       string   `json:"reason"`
	Explanation  []string `json:"explanation"`

	RequiresApproval        bool     `json:"requires_approval"`
	Approvers               []string `json:"approvers,omitempty"`
	MinApprovals            int      `json:"min_approvals"`
	RequireDistinctApprover bool     `json:"require_distinct_approver"`
	MaintenanceWindows      []string `json:"maintenance_windows,omitempty"`

	InputDigest string    `json:"input_digest"`
	DecidedAt   time.Time `json:"decided_at"`
}

// Allowed reports whether the decision permits execution at all.
func (d Decision) Allowed() bool {
	return d.Effect == EffectAutoExecute || d.Effect == EffectRequireApproval
}

// Evaluate applies a policy to an input. It is a pure function: same policy
// plus same input always yields the same decision, which is what makes the
// audit trail meaningful.
func Evaluate(p Policy, in Input) Decision {
	d := Decision{
		ID:               core.NewID("pd"),
		TenantID:         in.TenantID,
		RecommendationID: in.RecommendationID,
		PolicyID:         p.ID,
		PolicyVersion:    p.Version,
		PolicyChecksum:   p.Checksum,
		DecidedAt:        in.Now,
	}
	if d.DecidedAt.IsZero() {
		d.DecidedAt = time.Now().UTC()
	}

	// Deny-bias operates *among the matching rules*, and the default applies
	// only when nothing matched. Folding the default into the same comparison
	// would be a subtle but total failure: EffectAutoExecute has the lowest
	// rank, and validation forbids a default of auto_execute, so a permissive
	// rule could never out-rank the seed and no policy could ever authorise
	// autonomous execution. Tracking the matched effect separately keeps
	// "most restrictive matching rule wins" and "unmatched means default"
	// as the two distinct rules they are meant to be.
	fallback := p.DefaultEffect
	if fallback == "" {
		fallback = EffectRequireApproval
	}
	deciding := "__default__"
	var decidingRule *Rule
	var matchedEffect Effect
	matched := false

	for i := range p.Rules {
		r := p.Rules[i]
		if !r.Match.Matches(in) {
			continue
		}
		d.MatchedRules = append(d.MatchedRules, r.ID)
		// The most restrictive matching rule wins; ties keep the earlier rule
		// so the policy file reads top-down.
		if !matched || r.Effect.Rank() > matchedEffect.Rank() {
			matched = true
			matchedEffect = r.Effect
			deciding = r.ID
			decidingRule = &p.Rules[i]
		}
	}

	effect := fallback
	if matched {
		effect = matchedEffect
	}

	d.Explanation = append(d.Explanation,
		fmt.Sprintf("policy %s v%d evaluated %d rule(s); %d matched", p.Name, p.Version, len(p.Rules), len(d.MatchedRules)))

	// Platform invariants applied after tenant rules. These cannot be
	// overridden by policy because they are the reason the platform can be
	// trusted with an execute role at all.
	if effect == EffectAutoExecute && in.Destructive {
		effect = EffectRequireApproval
		deciding = "__platform_destructive_guard__"
		d.Explanation = append(d.Explanation,
			"platform guard: destructive actions are never auto-executed")
	}
	if effect == EffectAutoExecute && !in.AutomationEnabled {
		effect = EffectRequireApproval
		deciding = "__tenant_automation_disabled__"
		d.Explanation = append(d.Explanation,
			"tenant automation is disabled in the approved specification")
	}
	// Both economic-error-budget guards apply to cost-*increasing* changes
	// only, and the direction check belongs on both of them.
	//
	// econ.EconomicErrorBudget.AllowsCostIncrease is the sole source of these
	// two flags, and its name states its scope: it answers whether spending
	// more may proceed. The freeze branch has always honoured that. The
	// escalation branch did not, and the asymmetry was not harmless — a
	// tenant over budget is exactly the tenant whose cost-reducing changes
	// should be least obstructed, and escalating those to a human made the
	// platform stop saving money at the precise moment the budget said it
	// needed to save some. On a real estate the effect is self-sustaining:
	// over budget means nothing auto-executes, nothing auto-executing means
	// still over budget.
	costIncreasing := in.MonthlyCostDelta.GreaterThan(core.ZeroUSD())
	if in.BudgetFreeze && costIncreasing {
		effect = EffectProhibit
		deciding = "__economic_error_budget_freeze__"
		d.Explanation = append(d.Explanation,
			"an exhausted economic error budget has frozen cost-increasing changes")
	} else if in.BudgetRequiresApproval && costIncreasing && effect == EffectAutoExecute {
		effect = EffectRequireApproval
		deciding = "__economic_error_budget_escalation__"
		d.Explanation = append(d.Explanation,
			"economic error budget consumption requires human approval for this cost-increasing change")
	}

	d.Effect = effect
	d.DecidingRule = deciding
	d.RequiresApproval = effect == EffectRequireApproval
	if decidingRule != nil && effect == decidingRule.Effect {
		d.Approvers = decidingRule.Approvers
		d.MinApprovals = decidingRule.MinApprovals
		d.RequireDistinctApprover = decidingRule.RequireDistinctApprover
		d.MaintenanceWindows = decidingRule.MaintenanceWindows
		if decidingRule.Reason != "" {
			d.Reason = decidingRule.Reason
		} else {
			d.Reason = decidingRule.Description
		}
	}
	if d.RequiresApproval && d.MinApprovals == 0 {
		d.MinApprovals = 1
	}
	if d.Reason == "" {
		d.Reason = defaultReason(effect)
	}
	d.InputDigest = digestInput(in)
	return d
}

func defaultReason(e Effect) string {
	switch e {
	case EffectAutoExecute:
		return "policy permits autonomous execution for this change class"
	case EffectRequireApproval:
		return "policy requires human approval for this change class"
	case EffectProhibit:
		return "policy prohibits this change class"
	default:
		return "policy allows this recommendation for guidance only"
	}
}

// digestInput produces a stable fingerprint of the decision inputs so a stored
// decision can be shown to have been made on exactly these facts.
func digestInput(in Input) string {
	parts := []string{
		string(in.RuleID), string(in.Action), string(in.Environment),
		string(in.AccountID), string(in.Region), string(in.ResourceKind),
		fmt.Sprintf("conf=%.4f", float64(in.Confidence)),
		fmt.Sprintf("risk=%s/%.4f", in.RiskLevel, in.RiskScore),
		fmt.Sprintf("blast=%.4f/%d", in.BlastScore, in.CriticalServices),
		fmt.Sprintf("rev=%s", in.Reversibility),
		fmt.Sprintf("save=%d", in.MonthlySaving.Micros()),
		fmt.Sprintf("delta=%d", in.MonthlyCostDelta.Micros()),
		fmt.Sprintf("auto=%t,freeze=%t,esc=%t,mw=%t",
			in.AutomationEnabled, in.BudgetFreeze, in.BudgetRequiresApproval, in.InMaintenanceWindow),
	}
	sort.Strings(parts[3:6])
	return Checksum(strings.Join(parts, "|"))
}

func containsAction(list []optimize.ActionType, v optimize.ActionType) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func containsCategory(list []optimize.Category, v optimize.Category) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func containsRuleID(list []optimize.RuleID, v optimize.RuleID) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func containsEnv(list []core.Environment, v core.Environment) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func containsAccount(list []core.AccountID, v core.AccountID) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func containsRegion(list []core.Region, v core.Region) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func containsID(list []core.ID, v core.ID) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func containsString(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
