// Package optimize models findings, recommendations and the scoring that
// decides which of them a human should look at first.
//
// The separation that matters here is between a Finding (a deterministic rule
// fired on evidence) and a Recommendation (a proposed change with a predicted
// effect, a risk assessment, a blast radius and an executable action). Rules
// produce findings; the recommendation builder enriches findings into
// recommendations. An LLM may explain a recommendation, rank alternatives, or
// draft its narrative — it may never create one, because a recommendation is
// the object the execution engine acts on.
//
// Traceability: REQ-OPT-001..014, SPEC-OPT-001.
package optimize

import (
	"fmt"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// RuleID identifies an optimization rule. Rule identifiers are stable and
// versioned because the learning loop keys historical accuracy on them; a rule
// whose logic changes materially gets a new identifier rather than silently
// inheriting another rule's track record.
type RuleID string

// Category groups rules for reporting and for policy targeting.
type Category string

const (
	CategoryRightsizing   Category = "rightsizing"
	CategoryWaste         Category = "waste"
	CategoryStorage       Category = "storage"
	CategoryCommitment    Category = "commitment"
	CategoryNetwork       Category = "network"
	CategoryArchitecture  Category = "architecture"
	CategoryScheduling    Category = "scheduling"
	CategoryLicensing     Category = "licensing"
	CategoryDataLifecycle Category = "data_lifecycle"
	CategoryObservability Category = "observability_cost"
)

// ActionType is the executable verb a recommendation resolves to. The set is
// closed and each member maps to exactly one executor implementation with its
// own IAM action list, precondition check and rollback procedure. A
// recommendation whose action type has no executor can be approved but never
// executed, and the UI says so plainly.
type ActionType string

const (
	ActionResizeInstance               ActionType = "resize_instance"
	ActionStopInstance                 ActionType = "stop_instance"
	ActionTerminateInstance            ActionType = "terminate_instance"
	ActionDeleteVolume                 ActionType = "delete_volume"
	ActionResizeVolume                 ActionType = "resize_volume"
	ActionModifyVolumeType             ActionType = "modify_volume_type"
	ActionDeleteSnapshot               ActionType = "delete_snapshot"
	ActionDeregisterAMI                ActionType = "deregister_ami"
	ActionReleaseElasticIP             ActionType = "release_elastic_ip"
	ActionResizeRDS                    ActionType = "resize_rds_instance"
	ActionModifyRDSStorage             ActionType = "modify_rds_storage"
	ActionRemoveRDSReplica             ActionType = "remove_rds_replica"
	ActionStopRDS                      ActionType = "stop_rds_instance"
	ActionApplyS3Lifecycle             ActionType = "apply_s3_lifecycle"
	ActionAbortMultipartUploads        ActionType = "abort_multipart_uploads"
	ActionSetLogRetention              ActionType = "set_log_retention"
	ActionResizeLambdaMemory           ActionType = "resize_lambda_memory"
	ActionRemoveProvisionedConcurrency ActionType = "remove_provisioned_concurrency"
	ActionSwitchLambdaArch             ActionType = "switch_lambda_architecture"
	ActionResizeNodeGroup              ActionType = "resize_node_group"
	ActionAdjustPodResources           ActionType = "adjust_pod_resources"
	ActionEnableSpot                   ActionType = "enable_spot_capacity"
	ActionCreateVPCEndpoint            ActionType = "create_vpc_endpoint"
	ActionRemoveNATGateway             ActionType = "remove_nat_gateway"
	ActionScheduleShutdown             ActionType = "schedule_shutdown"
	ActionPurchaseCommitment           ActionType = "purchase_commitment"
	ActionSwitchDynamoBilling          ActionType = "switch_dynamodb_billing_mode"
	ActionAdvisoryOnly                 ActionType = "advisory_only" // architecture advice with no direct executor
)

// Mutating reports whether the action changes customer infrastructure.
func (a ActionType) Mutating() bool { return a != ActionAdvisoryOnly }

// Destructive reports whether the action deletes state that cannot be
// recreated from a snapshot. Destructive actions never auto-execute, in any
// environment, under any policy — the policy engine treats a policy that tries
// to allow it as a validation error.
func (a ActionType) Destructive() bool {
	switch a {
	case ActionTerminateInstance, ActionDeleteVolume, ActionDeleteSnapshot,
		ActionDeregisterAMI, ActionRemoveRDSReplica, ActionRemoveNATGateway:
		return true
	}
	return false
}

// Reversibility scores how cheaply a change can be undone. It is a direct
// multiplier in the priority formula: an equally valuable, equally risky
// change that can be undone in sixty seconds should be done first.
type Reversibility string

const (
	// ReversibilityInstant: undo is a single API call with no data movement,
	// e.g. re-attaching an Elastic IP, restoring log retention.
	ReversibilityInstant Reversibility = "instant"
	// ReversibilityFast: undo requires a restart or a short reconfiguration,
	// e.g. resizing an instance back.
	ReversibilityFast Reversibility = "fast"
	// ReversibilitySlow: undo requires restoring from a snapshot or a
	// maintenance window, e.g. RDS storage changes.
	ReversibilitySlow Reversibility = "slow"
	// ReversibilityNone: the change cannot be undone, e.g. deleting the last
	// snapshot of a volume, purchasing a three-year commitment.
	ReversibilityNone Reversibility = "none"
)

// Factor is the numeric weight used in priority scoring.
func (r Reversibility) Factor() float64 {
	switch r {
	case ReversibilityInstant:
		return 1.0
	case ReversibilityFast:
		return 0.8
	case ReversibilitySlow:
		return 0.45
	case ReversibilityNone:
		return 0.15
	}
	return 0.5
}

// Evidence is one observation supporting a finding. Every finding must carry
// evidence; a finding with no evidence is rejected at construction time. This
// is what makes a recommendation defensible in a change review.
type Evidence struct {
	Kind        string            `json:"kind"` // "metric" | "config" | "cost" | "topology" | "history"
	Label       string            `json:"label"`
	Value       string            `json:"value"`
	Window      core.Period       `json:"window,omitempty"`
	Source      string            `json:"source"` // "cloudwatch" | "discovery" | "cost_explorer"
	Percentiles *core.Percentiles `json:"percentiles,omitempty"`
}

// Finding is a rule firing on a resource. It states what is true, not what to
// do about it.
type Finding struct {
	ID           core.ID          `json:"id"`
	TenantID     core.TenantID    `json:"tenant_id"`
	RuleID       RuleID           `json:"rule_id"`
	RuleName     string           `json:"rule_name"`
	Category     Category         `json:"category"`
	ResourceID   core.ID          `json:"resource_id"`
	ResourceName string           `json:"resource_name"`
	ResourceKind cloud.Kind       `json:"resource_kind"`
	AccountID    core.AccountID   `json:"account_id"`
	Region       core.Region      `json:"region"`
	Environment  core.Environment `json:"environment"`
	Severity     core.Severity    `json:"severity"`
	Summary      string           `json:"summary"`
	Detail       string           `json:"detail,omitempty"`
	Evidence     []Evidence       `json:"evidence"`
	// CurrentMonthlyCost is what the resource costs today; EstimatedMonthlySaving
	// is what the rule believes the change recovers. Both are required: a
	// saving without a baseline cannot be validated after execution.
	CurrentMonthlyCost     core.Money `json:"current_monthly_cost"`
	EstimatedMonthlySaving core.Money `json:"estimated_monthly_saving"`
	DetectedAt             time.Time  `json:"detected_at"`
}

// Validate enforces the evidence invariant.
func (f Finding) Validate() error {
	var v core.ValidationResult
	if f.RuleID == "" {
		v.Add("rule_id", "required", core.SeverityCritical, "finding must name the rule that produced it")
	}
	if f.ResourceID.IsZero() {
		v.Add("resource_id", "required", core.SeverityCritical, "finding must reference a resource")
	}
	if len(f.Evidence) == 0 {
		v.Add("evidence", "required", core.SeverityCritical,
			"a finding without evidence cannot be reviewed or validated")
	}
	if f.EstimatedMonthlySaving.GreaterThan(f.CurrentMonthlyCost) {
		v.Add("estimated_monthly_saving", "implausible", core.SeverityHigh,
			"estimated saving %s exceeds the resource's own cost %s",
			f.EstimatedMonthlySaving.Format(), f.CurrentMonthlyCost.Format())
	}
	return v.Err()
}

// Status is the recommendation lifecycle.
type Status string

const (
	StatusOpen        Status = "open"
	StatusUnderReview Status = "under_review"
	StatusApproved    Status = "approved"
	StatusRejected    Status = "rejected"
	StatusScheduled   Status = "scheduled"
	StatusExecuting   Status = "executing"
	StatusExecuted    Status = "executed"
	StatusValidating  Status = "validating"
	StatusValidated   Status = "validated"
	StatusFailed      Status = "failed"
	StatusRolledBack  Status = "rolled_back"
	StatusSuperseded  Status = "superseded"
	StatusSnoozed     Status = "snoozed"
	StatusDismissed   Status = "dismissed"
)

// Terminal reports whether no further transition is expected.
func (s Status) Terminal() bool {
	switch s {
	case StatusValidated, StatusRejected, StatusRolledBack, StatusSuperseded, StatusDismissed:
		return true
	}
	return false
}

// Recommendation is a proposed, costed, risk-assessed change.
type Recommendation struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	Finding  Finding       `json:"finding"`

	Title     string     `json:"title"`
	Rationale string     `json:"rationale"`
	Action    ActionType `json:"action"`
	// Parameters are the typed inputs the executor needs. They are produced by
	// the rule, validated against a per-action schema, and are the only thing
	// the executor reads — an executor never re-derives intent from prose.
	//
	// The key names are the EXECUTOR's vocabulary, not the rule's: an
	// executor for resize_instance reads "instance_type", so that is the key
	// a rule emits, and it is also the key the executor's own pre-change
	// snapshot uses so that rollback restores through the same path. A rule
	// that invents its own spelling ("target_instance_type") produces a
	// recommendation that reaches an executor and fails at the mutate step
	// with a missing-parameter error, having already passed policy,
	// approval and preflight. That failure mode is why this comment names
	// the direction the contract runs in.
	Parameters map[string]any `json:"parameters"`

	CurrentState  StateSnapshot `json:"current_state"`
	ProposedState StateSnapshot `json:"proposed_state"`

	EstimatedMonthlySaving core.Money `json:"estimated_monthly_saving"`
	EstimatedAnnualSaving  core.Money `json:"estimated_annual_saving"`
	ImplementationCost     core.Money `json:"implementation_cost,omitempty"`
	PaybackDays            float64    `json:"payback_days,omitempty"`

	Confidence      core.Confidence   `json:"confidence"`
	ConfidenceBasis []ConfidenceInput `json:"confidence_basis,omitempty"`
	Risk            RiskAssessment    `json:"risk"`
	BlastRadius     BlastRadius       `json:"blast_radius"`
	Reversibility   Reversibility     `json:"reversibility"`
	Complexity      Complexity        `json:"complexity"`

	PriorityScore float64 `json:"priority_score"`
	Rank          int     `json:"rank,omitempty"`

	// ConflictDomain is what this change contends for on its resource; it is
	// declared by the rule that produced the recommendation (defaulting from
	// the action, see DefaultConflictDomain) and is the input GroupConflicts
	// partitions on. The three fields below it are GroupConflicts' output and
	// are never set by a rule.
	ConflictDomain ConflictDomain `json:"conflict_domain,omitempty"`
	// ConflictGroupID identifies the set of recommendations competing for the
	// same resource in the same domain. Empty means this recommendation
	// competes with nothing.
	ConflictGroupID string `json:"conflict_group_id,omitempty"`
	// MutuallyExclusive is true when at most one member of this
	// recommendation's conflict group can be applied. It is set on every
	// member, primary included, because the primary is exactly as mutually
	// exclusive as its alternatives — it just happens to be the one CloudOptix
	// recommends.
	MutuallyExclusive bool `json:"mutually_exclusive,omitempty"`
	// AlternativeIDs are the other members of this recommendation's conflict
	// group, in the order the priority formula ranks them, so a reviewer can
	// see every way of fixing the problem and not just the recommended one.
	AlternativeIDs []core.ID `json:"alternative_ids,omitempty"`
	// PreferredAlternativeID names the member of this conflict group that
	// CloudOptix recommends instead of this one. It is empty on the primary,
	// and a non-empty value is exactly what excludes a recommendation from
	// every total — see CountsTowardTotal.
	PreferredAlternativeID core.ID `json:"preferred_alternative_id,omitempty"`

	Status       Status     `json:"status"`
	StatusReason string     `json:"status_reason,omitempty"`
	SnoozedUntil *time.Time `json:"snoozed_until,omitempty"`

	// RequiresApproval and PolicyDecisionID are filled by the policy engine.
	// A recommendation never self-assesses whether it may run.
	RequiresApproval bool    `json:"requires_approval"`
	PolicyDecisionID core.ID `json:"policy_decision_id,omitempty"`
	AutoExecutable   bool    `json:"auto_executable"`

	// Narrative is the LLM-generated explanation. It is decorative: removing
	// it changes nothing about what executes. Keeping the generated text in a
	// clearly-labelled field is how the codebase enforces that boundary.
	Narrative string `json:"narrative,omitempty"`

	MaintenanceWindow string    `json:"maintenance_window,omitempty"`
	SupersedesID      core.ID   `json:"supersedes_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// StateSnapshot captures the before/after shape of a resource in the terms a
// human reviews and the validator checks.
type StateSnapshot struct {
	InstanceType string            `json:"instance_type,omitempty"`
	VolumeType   string            `json:"volume_type,omitempty"`
	SizeGiB      float64           `json:"size_gib,omitempty"`
	IOPS         int64             `json:"iops,omitempty"`
	MemoryMB     int               `json:"memory_mb,omitempty"`
	Count        int               `json:"count,omitempty"`
	VCPU         float64           `json:"vcpu,omitempty"`
	MemoryGiB    float64           `json:"memory_gib,omitempty"`
	MonthlyCost  core.Money        `json:"monthly_cost"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}

// Complexity rates the operational effort to apply the change.
type Complexity string

const (
	ComplexityTrivial Complexity = "trivial" // one API call, no downtime
	ComplexityLow     Complexity = "low"     // brief restart
	ComplexityMedium  Complexity = "medium"  // maintenance window
	ComplexityHigh    Complexity = "high"    // coordinated change, code or IaC edits
	ComplexityProject Complexity = "project" // architecture migration
)

// Factor converts complexity into the priority-formula divisor.
func (c Complexity) Factor() float64 {
	switch c {
	case ComplexityTrivial:
		return 1.0
	case ComplexityLow:
		return 0.85
	case ComplexityMedium:
		return 0.6
	case ComplexityHigh:
		return 0.35
	case ComplexityProject:
		return 0.15
	}
	return 0.5
}

// ConfidenceInput records one contributor to the computed confidence, so the
// number can be explained rather than merely displayed.
type ConfidenceInput struct {
	Name        string  `json:"name"`
	Value       float64 `json:"value"`
	Weight      float64 `json:"weight"`
	Explanation string  `json:"explanation"`
}

// RiskAssessment is the deterministic risk analysis of a change.
type RiskAssessment struct {
	Score            float64        `json:"score"` // 0..1
	Level            core.RiskLevel `json:"level"`
	AvailabilityRisk core.RiskLevel `json:"availability_risk"`
	PerformanceRisk  core.RiskLevel `json:"performance_risk"`
	SecurityRisk     core.RiskLevel `json:"security_risk"`
	DataLossRisk     core.RiskLevel `json:"data_loss_risk"`
	Factors          []RiskFactor   `json:"factors"`
	Mitigations      []string       `json:"mitigations,omitempty"`
}

// RiskFactor is one contributor to the risk score.
type RiskFactor struct {
	Name         string  `json:"name"`
	Contribution float64 `json:"contribution"`
	Explanation  string  `json:"explanation"`
}

// BlastRadius quantifies what a change can touch if it goes wrong.
//
// This is computed from the architecture twin, not estimated: services and
// transactions are counted by walking the dependency graph, and customers are
// derived from the transaction volumes attached to those transactions. A
// recommendation whose blast radius cannot be computed — because the twin does
// not know the resource's dependents — is marked low-confidence rather than
// assumed safe.
//
// Traceability: REQ-OPT-008, SPEC-OPT-005.
type BlastRadius struct {
	ResourcesAffected    int                `json:"resources_affected"`
	ServicesAffected     int                `json:"services_affected"`
	CriticalServices     int                `json:"critical_services"`
	WorkloadsAffected    []string           `json:"workloads_affected,omitempty"`
	APIsAffected         int                `json:"apis_affected"`
	TransactionsAffected []string           `json:"transactions_affected,omitempty"`
	EstimatedUsers       int64              `json:"estimated_users"`
	MonthlyRevenueAtRisk core.Money         `json:"monthly_revenue_at_risk,omitempty"`
	EnvironmentsAffected []core.Environment `json:"environments_affected,omitempty"`
	CrossAccount         bool               `json:"cross_account"`
	Score                float64            `json:"score"` // 0..1
	Level                core.RiskLevel     `json:"level"`
	// Completeness is how much of the graph the calculation could see. A
	// blast radius computed on a partially-discovered estate is not a small
	// blast radius, and this field stops it being read as one.
	Completeness float64 `json:"completeness"`
	Explanation  string  `json:"explanation"`
}

// Describe renders the blast radius for a change review.
func (b BlastRadius) Describe() string {
	return fmt.Sprintf("%d resources, %d services (%d critical), %d transactions, ~%s users; %s risk (%.0f%% graph coverage)",
		b.ResourcesAffected, b.ServicesAffected, b.CriticalServices,
		len(b.TransactionsAffected), humanizeCount(b.EstimatedUsers), b.Level, b.Completeness*100)
}

func humanizeCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// PriorityFormula is the configurable scoring rule.
//
// The default expresses a deliberate stance: prefer changes that save real
// money, that CloudOptix is confident about, and that can be undone — and
// penalise risk and effort. Exposing the exponents rather than hard-coding the
// arithmetic lets a tenant tune the stance (a startup may weight savings far
// above reversibility; a bank the reverse) without a code change.
//
// Traceability: REQ-OPT-012, SPEC-OPT-006.
type PriorityFormula struct {
	SavingsWeight       float64 `json:"savings_weight" yaml:"savings_weight"`
	ConfidenceWeight    float64 `json:"confidence_weight" yaml:"confidence_weight"`
	ReversibilityWeight float64 `json:"reversibility_weight" yaml:"reversibility_weight"`
	RiskPenalty         float64 `json:"risk_penalty" yaml:"risk_penalty"`
	BlastPenalty        float64 `json:"blast_penalty" yaml:"blast_penalty"`
	ComplexityWeight    float64 `json:"complexity_weight" yaml:"complexity_weight"`
	CriticalityPenalty  float64 `json:"criticality_penalty" yaml:"criticality_penalty"`
	// SavingsNormalizer is the monthly saving that scores 1.0 on the savings
	// axis, so the formula is scale-free across tenants of different sizes.
	SavingsNormalizer core.Money `json:"savings_normalizer" yaml:"savings_normalizer"`
}

// DefaultPriorityFormula is the platform default.
func DefaultPriorityFormula() PriorityFormula {
	return PriorityFormula{
		SavingsWeight:       1.0,
		ConfidenceWeight:    1.0,
		ReversibilityWeight: 0.7,
		RiskPenalty:         1.0,
		BlastPenalty:        0.6,
		ComplexityWeight:    0.5,
		CriticalityPenalty:  0.4,
		SavingsNormalizer:   core.USDollars(5000),
	}
}

// Score computes the priority of a recommendation. The result is unbounded
// above but in practice lands in 0..100 for the default formula, which is what
// the UI renders.
func (f PriorityFormula) Score(r Recommendation) float64 {
	norm := f.SavingsNormalizer
	if norm.IsZero() {
		norm = core.USDollars(5000)
	}
	// Savings uses a saturating curve: the difference between $50 and $500 a
	// month matters far more than between $50,000 and $50,450.
	savingsAxis := saturate(r.EstimatedMonthlySaving.Ratio(norm))
	confAxis := float64(r.Confidence.Clamp())
	revAxis := r.Reversibility.Factor()
	cxAxis := r.Complexity.Factor()

	numerator := pow(savingsAxis, f.SavingsWeight) *
		pow(confAxis, f.ConfidenceWeight) *
		pow(revAxis, f.ReversibilityWeight) *
		pow(cxAxis, f.ComplexityWeight)

	riskTerm := 1 + f.RiskPenalty*r.Risk.Score
	blastTerm := 1 + f.BlastPenalty*r.BlastRadius.Score
	critTerm := 1.0
	if r.Finding.Environment.IsProduction() {
		critTerm = 1 + f.CriticalityPenalty
	}
	denominator := riskTerm * blastTerm * critTerm
	if denominator == 0 {
		return 0
	}
	return 100 * numerator / denominator
}

func saturate(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return x / (1 + x) * 2 // approaches 2 asymptotically; 1.0 at x=1
}

func pow(base, exp float64) float64 {
	if base <= 0 {
		return 0
	}
	if exp == 1 {
		return base
	}
	// math.Pow, inlined via the stdlib in the caller's build; kept explicit
	// so the formula reads as written.
	return powFloat(base, exp)
}

// Rank sorts recommendations by descending priority and assigns Rank.
func Rank(recs []Recommendation, formula PriorityFormula) []Recommendation {
	for i := range recs {
		recs[i].PriorityScore = formula.Score(recs[i])
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].PriorityScore != recs[j].PriorityScore {
			return recs[i].PriorityScore > recs[j].PriorityScore
		}
		return recs[i].EstimatedMonthlySaving.Micros() > recs[j].EstimatedMonthlySaving.Micros()
	})
	for i := range recs {
		recs[i].Rank = i + 1
	}
	return recs
}

// TotalPotentialSaving sums the monthly savings a recommendation set can
// actually deliver.
//
// It counts primaries only. Within a conflict group at most one member can be
// applied, so summing every member would report money the estate does not
// contain — see conflict.go. The alternatives are still in the slice and
// still worth showing; they are simply not additive.
func TotalPotentialSaving(recs []Recommendation) core.Money {
	total := core.ZeroUSD()
	for _, r := range recs {
		if !r.CountsTowardTotal() {
			continue
		}
		total = total.MustAdd(r.EstimatedMonthlySaving)
	}
	return total
}
