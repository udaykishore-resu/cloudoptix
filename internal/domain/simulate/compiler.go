package simulate

import (
	"fmt"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// SourceKind names the infrastructure-as-code dialect the cost compiler
// consumed.
type SourceKind string

const (
	SourceTerraformPlan  SourceKind = "terraform_plan"
	SourceTerraformHCL   SourceKind = "terraform_hcl"
	SourceCloudFormation SourceKind = "cloudformation"
	SourceKubernetes     SourceKind = "kubernetes_manifest"
	SourceHelmRelease    SourceKind = "helm_release"
	SourceLiveTopology   SourceKind = "live_topology"
)

// ChangeAction mirrors the Terraform plan verbs.
type ChangeAction string

const (
	ChangeCreate  ChangeAction = "create"
	ChangeUpdate  ChangeAction = "update"
	ChangeReplace ChangeAction = "replace"
	ChangeDelete  ChangeAction = "delete"
	ChangeNoOp    ChangeAction = "no-op"
)

// PricedChange is one infrastructure change with its economic consequence.
//
// The compiler prices what it can identify and is explicit about what it
// cannot. A resource whose cost depends on usage — a Lambda function, a NAT
// gateway's data processing, an S3 bucket — gets a usage-dependent entry with
// the assumed usage stated, rather than a fabricated fixed number or a silent
// zero. "Unpriced" and "free" are different answers and the compiler never
// conflates them.
type PricedChange struct {
	Address      string       `json:"address"` // module.web.aws_instance.api[0]
	ResourceType string       `json:"resource_type"`
	Kind         string       `json:"kind"`
	Action       ChangeAction `json:"action"`
	Region       core.Region  `json:"region,omitempty"`

	BeforeMonthly core.Money `json:"before_monthly"`
	AfterMonthly  core.Money `json:"after_monthly"`
	MonthlyDelta  core.Money `json:"monthly_delta"`

	// UsageDependent marks a resource whose cost cannot be known from the IaC
	// alone. Its AfterMonthly is a modelled estimate under the stated
	// assumptions.
	UsageDependent bool         `json:"usage_dependent"`
	Assumptions    []Assumption `json:"assumptions,omitempty"`
	// Unpriced marks a resource the compiler could not price at all. It is
	// counted and listed so a reviewer knows the estimate is incomplete.
	Unpriced        bool             `json:"unpriced"`
	UnpricedReason  string           `json:"unpriced_reason,omitempty"`
	PriceComponents []PriceComponent `json:"price_components,omitempty"`
	Warnings        []string         `json:"warnings,omitempty"`
}

// PriceComponent is one billed dimension of a resource, e.g. instance hours,
// EBS storage, provisioned IOPS.
type PriceComponent struct {
	Name       string     `json:"name"`
	Unit       string     `json:"unit"`
	Quantity   float64    `json:"quantity"`
	UnitPrice  core.Money `json:"unit_price"`
	Monthly    core.Money `json:"monthly"`
	PriceBasis string     `json:"price_basis"` // "on_demand" | "savings_plan_effective" | "spot_avg"
}

// CompilationResult is the cost compiler's output for one change set.
type CompilationResult struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	Source   SourceKind    `json:"source"`
	Label    string        `json:"label"` // PR title, plan file name

	Changes []PricedChange `json:"changes"`

	BaselineMonthly  core.Money `json:"baseline_monthly"`
	ProjectedMonthly core.Money `json:"projected_monthly"`
	MonthlyDelta     core.Money `json:"monthly_delta"`
	AnnualDelta      core.Money `json:"annual_delta"`
	DeltaPct         float64    `json:"delta_pct"`

	CreatedCount  int `json:"created_count"`
	UpdatedCount  int `json:"updated_count"`
	DeletedCount  int `json:"deleted_count"`
	UnpricedCount int `json:"unpriced_count"`

	// Coverage is the share of changed resources the compiler could price. A
	// delta computed at 60% coverage is reported with that caveat attached
	// rather than as a confident figure.
	Coverage      float64         `json:"coverage"`
	Confidence    core.Confidence `json:"confidence"`
	Assumptions   []Assumption    `json:"assumptions"`
	Risks         []CostRisk      `json:"risks,omitempty"`
	Opportunities []Opportunity   `json:"opportunities,omitempty"`

	PricingDate time.Time `json:"pricing_date"`
	CompiledAt  time.Time `json:"compiled_at"`
	DurationMS  int64     `json:"duration_ms"`
}

// CostRisk is a structural cost hazard the compiler spotted in the change,
// independent of the headline delta: a new NAT gateway in every AZ, a
// provisioned-IOPS volume with no workload evidence, a cross-region replica.
type CostRisk struct {
	Code          string        `json:"code"`
	Severity      core.Severity `json:"severity"`
	Address       string        `json:"address,omitempty"`
	Summary       string        `json:"summary"`
	Detail        string        `json:"detail,omitempty"`
	MonthlyImpact core.Money    `json:"monthly_impact,omitempty"`
	Remediation   string        `json:"remediation,omitempty"`
}

// Opportunity is a cheaper alternative the compiler noticed while pricing,
// surfaced at review time when changing it is nearly free.
type Opportunity struct {
	Address       string     `json:"address"`
	Summary       string     `json:"summary"`
	MonthlySaving core.Money `json:"monthly_saving"`
	Change        string     `json:"change"`
}

// Summarize computes the roll-ups from the priced changes.
func (r *CompilationResult) Summarize() {
	r.BaselineMonthly = core.ZeroUSD()
	r.ProjectedMonthly = core.ZeroUSD()
	priced := 0
	for _, c := range r.Changes {
		r.BaselineMonthly = r.BaselineMonthly.MustAdd(c.BeforeMonthly)
		r.ProjectedMonthly = r.ProjectedMonthly.MustAdd(c.AfterMonthly)
		switch c.Action {
		case ChangeCreate:
			r.CreatedCount++
		case ChangeUpdate, ChangeReplace:
			r.UpdatedCount++
		case ChangeDelete:
			r.DeletedCount++
		}
		if c.Unpriced {
			r.UnpricedCount++
		} else {
			priced++
		}
	}
	r.MonthlyDelta = r.ProjectedMonthly.MustSub(r.BaselineMonthly)
	r.AnnualDelta = r.MonthlyDelta.Annualized()
	if !r.BaselineMonthly.IsZero() {
		r.DeltaPct = r.MonthlyDelta.Ratio(r.BaselineMonthly) * 100
	}
	if total := len(r.Changes); total > 0 {
		r.Coverage = float64(priced) / float64(total)
	} else {
		r.Coverage = 1
	}
	// Confidence tracks coverage, discounted when much of the delta is
	// usage-dependent modelling rather than fixed pricing.
	usageDependent := core.ZeroUSD()
	for _, c := range r.Changes {
		if c.UsageDependent {
			usageDependent = usageDependent.MustAdd(c.AfterMonthly.Abs())
		}
	}
	share := 0.0
	if !r.ProjectedMonthly.IsZero() {
		share = usageDependent.Ratio(r.ProjectedMonthly.Abs())
	}
	r.Confidence = core.Confidence(r.Coverage * (1 - 0.35*share)).Clamp()

	sort.SliceStable(r.Changes, func(i, j int) bool {
		return r.Changes[i].MonthlyDelta.Abs().Micros() > r.Changes[j].MonthlyDelta.Abs().Micros()
	})
}

// Verdict is the outcome of a cost regression test.
type Verdict string

const (
	VerdictPass    Verdict = "PASS"
	VerdictWarning Verdict = "WARNING"
	VerdictFail    Verdict = "FAIL"
)

// Worse reports whether v is a stronger failure than other.
func (v Verdict) Worse(other Verdict) bool {
	rank := map[Verdict]int{VerdictPass: 0, VerdictWarning: 1, VerdictFail: 2}
	return rank[v] > rank[other]
}

// RegressionCheckKind names the assertion types available to a cost test.
type RegressionCheckKind string

const (
	// CheckMaxMonthlyIncreasePct fails when the change raises monthly cost by
	// more than a percentage.
	CheckMaxMonthlyIncreasePct RegressionCheckKind = "max_monthly_increase_pct"
	// CheckMaxMonthlyIncreaseAbs fails on an absolute dollar increase.
	CheckMaxMonthlyIncreaseAbs RegressionCheckKind = "max_monthly_increase_abs"
	// CheckMaxCostPerTransaction fails when projected unit economics regress.
	CheckMaxCostPerTransaction RegressionCheckKind = "max_cost_per_transaction"
	// CheckForbiddenResource fails when the change introduces a resource type
	// the tenant has decided requires architecture review, most commonly a
	// NAT gateway.
	CheckForbiddenResource RegressionCheckKind = "forbidden_resource"
	// CheckRequireTags fails when new resources lack the tags the economics
	// engine needs for attribution — an untagged resource is a future
	// unattributed cost.
	CheckRequireTags RegressionCheckKind = "require_tags"
	// CheckMaxUnpricedRatio fails when too much of the change could not be
	// priced for the verdict to mean anything.
	CheckMaxUnpricedRatio RegressionCheckKind = "max_unpriced_ratio"
	// CheckBudgetHeadroom fails when the change would exhaust an economic
	// error budget.
	CheckBudgetHeadroom RegressionCheckKind = "budget_headroom"
)

// RegressionCheck is one assertion in a cost test suite.
type RegressionCheck struct {
	Name      string              `json:"name" yaml:"name"`
	Kind      RegressionCheckKind `json:"kind" yaml:"kind"`
	Threshold float64             `json:"threshold,omitempty" yaml:"threshold,omitempty"`
	Amount    core.Money          `json:"amount,omitempty" yaml:"amount,omitempty"`
	// Scope narrows the check, e.g. to production or one application.
	Environments    []core.Environment `json:"environments,omitempty" yaml:"environments,omitempty"`
	ResourceTypes   []string           `json:"resource_types,omitempty" yaml:"resource_types,omitempty"`
	RequiredTags    []string           `json:"required_tags,omitempty" yaml:"required_tags,omitempty"`
	TransactionName string             `json:"transaction_name,omitempty" yaml:"transaction_name,omitempty"`
	// OnViolation selects FAIL or WARNING. A team adopting cost testing starts
	// everything at WARNING and promotes checks as they gain confidence.
	OnViolation Verdict `json:"on_violation" yaml:"on_violation"`
	Message     string  `json:"message,omitempty" yaml:"message,omitempty"`
}

// RegressionSuite is a tenant's cost test suite, stored as policy-as-code.
type RegressionSuite struct {
	ID        core.ID           `json:"id"`
	TenantID  core.TenantID     `json:"tenant_id"`
	Name      string            `json:"name" yaml:"name"`
	Version   int               `json:"version" yaml:"version"`
	Checks    []RegressionCheck `json:"checks" yaml:"checks"`
	Enabled   bool              `json:"enabled" yaml:"enabled"`
	CreatedAt time.Time         `json:"created_at"`
}

// CheckResult is one assertion's outcome.
type CheckResult struct {
	Name      string              `json:"name"`
	Kind      RegressionCheckKind `json:"kind"`
	Verdict   Verdict             `json:"verdict"`
	Expected  string              `json:"expected"`
	Actual    string              `json:"actual"`
	Message   string              `json:"message"`
	Offenders []string            `json:"offenders,omitempty"`
}

// RegressionReport is the CI-facing result of running a suite against a
// compilation.
type RegressionReport struct {
	ID            core.ID       `json:"id"`
	TenantID      core.TenantID `json:"tenant_id"`
	CompilationID core.ID       `json:"compilation_id"`
	SuiteName     string        `json:"suite_name"`
	Verdict       Verdict       `json:"verdict"`
	Results       []CheckResult `json:"results"`
	MonthlyDelta  core.Money    `json:"monthly_delta"`
	AnnualDelta   core.Money    `json:"annual_delta"`
	Summary       string        `json:"summary"`
	// RequiredAction states what CI should do, in the words the PR comment
	// uses: "Architecture review required".
	RequiredAction string    `json:"required_action,omitempty"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
}

// Finalize computes the overall verdict and the human summary.
func (r *RegressionReport) Finalize() {
	r.Verdict = VerdictPass
	fails, warns := 0, 0
	for _, res := range r.Results {
		if res.Verdict.Worse(r.Verdict) {
			r.Verdict = res.Verdict
		}
		switch res.Verdict {
		case VerdictFail:
			fails++
		case VerdictWarning:
			warns++
		}
	}
	switch r.Verdict {
	case VerdictPass:
		r.Summary = fmt.Sprintf("All %d cost checks passed. Monthly impact %s (%s/year).",
			len(r.Results), r.MonthlyDelta.Format(), r.AnnualDelta.Format())
	case VerdictWarning:
		r.Summary = fmt.Sprintf("%d cost check(s) raised a warning. Monthly impact %s (%s/year).",
			warns, r.MonthlyDelta.Format(), r.AnnualDelta.Format())
		r.RequiredAction = "Review the flagged items before merging."
	case VerdictFail:
		r.Summary = fmt.Sprintf("COST TEST FAILED: %d check(s) failed. Monthly impact %s (%s/year).",
			fails, r.MonthlyDelta.Format(), r.AnnualDelta.Format())
		r.RequiredAction = "Architecture review required."
	}
	r.EvaluatedAt = time.Now().UTC()
}
