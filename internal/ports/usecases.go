package ports

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
)

// This file declares the driving ports: the use-case interfaces the HTTP
// layer, the CLI and the background workers call. Application services in
// internal/application implement them.
//
// Keeping the driving side as interfaces rather than concrete structs is what
// lets the transport layer, the engines and the tests be developed and
// reviewed independently, and it makes the platform's whole capability surface
// readable in one file.
//
// Traceability: SPEC-ARCH-003, SPEC-API-001.

// OnboardingService runs the conversational onboarding flow.
type OnboardingService interface {
	// Start opens a conversation and returns the agent's opening turn. No
	// tenant exists yet at this point: onboarding produces the specification
	// that a tenant is created from.
	Start(ctx context.Context, in StartOnboardingInput) (OnboardingState, error)
	// Send processes one user message: extracts requirements, updates the
	// draft specification, and returns the agent's reply plus the current
	// state of what is known, inferred, unknown and outstanding.
	Send(ctx context.Context, conversationID core.ID, message string) (OnboardingState, error)
	// State returns the current onboarding state without sending a message.
	State(ctx context.Context, conversationID core.ID) (OnboardingState, error)
	// Summarize produces the pre-approval review packet.
	Summarize(ctx context.Context, conversationID core.ID) (OnboardingSummary, error)
	// ApplyEdit applies a direct specification edit made in the review UI
	// rather than through conversation.
	ApplyEdit(ctx context.Context, conversationID core.ID, patch map[string]any, actor string) (OnboardingState, error)
	// Approve freezes the specification, creates the tenant and returns both.
	// This is the single transition from "talking about it" to "it exists".
	Approve(ctx context.Context, in ApproveOnboardingInput) (OnboardingResult, error)
	// Cancel abandons the conversation.
	Cancel(ctx context.Context, conversationID core.ID, reason string) error
}

// StartOnboardingInput opens an onboarding conversation.
type StartOnboardingInput struct {
	Actor          string
	ActorEmail     string
	InitialMessage string
	// ExistingTenant is set when an established tenant is revising its
	// specification rather than onboarding for the first time.
	ExistingTenant core.TenantID
}

// OnboardingState is what the chat UI renders after every turn.
type OnboardingState struct {
	ConversationID core.ID           `json:"conversation_id"`
	Reply          string            `json:"reply"`
	Stage          string            `json:"stage"` // organization | application | aws | workloads | business | objectives | governance | review
	Draft          spec.Spec         `json:"draft"`
	Completeness   spec.Completeness `json:"completeness"`

	// Collected, Inferred, Unknown and NeedsConfirmation are the four buckets
	// the UI shows beside the conversation, so the user always sees what
	// CloudOptix believes and how it came to believe it.
	Collected         []FieldState `json:"collected"`
	Inferred          []FieldState `json:"inferred"`
	Unknown           []FieldState `json:"unknown"`
	NeedsConfirmation []FieldState `json:"needs_confirmation"`

	OpenQuestions  []spec.OpenQuestion   `json:"open_questions"`
	Validation     core.ValidationResult `json:"validation"`
	ReadyForReview bool                  `json:"ready_for_review"`
	// Suggestions are quick replies the UI offers, which is what keeps
	// onboarding from feeling like an interrogation.
	Suggestions []string `json:"suggestions,omitempty"`
	Degraded    bool     `json:"degraded"` // answered without the model
}

// FieldState is one specification field's status for the UI.
type FieldState struct {
	Path       string          `json:"path"`
	Label      string          `json:"label"`
	Value      string          `json:"value,omitempty"`
	Provenance core.Provenance `json:"provenance"`
	Rationale  string          `json:"rationale,omitempty"`
	Source     string          `json:"source,omitempty"`
}

// OnboardingSummary is the pre-approval review packet.
type OnboardingSummary struct {
	ConversationID core.ID               `json:"conversation_id"`
	Spec           spec.Spec             `json:"spec"`
	SpecYAML       string                `json:"spec_yaml"`
	Completeness   spec.Completeness     `json:"completeness"`
	Validation     core.ValidationResult `json:"validation"`
	Sections       []SummarySection      `json:"sections"`
	// WhatHappensNext states plainly what approval will cause, because
	// approval is the moment a human takes responsibility.
	WhatHappensNext []string `json:"what_happens_next"`
	CanApprove      bool     `json:"can_approve"`
	BlockingReasons []string `json:"blocking_reasons,omitempty"`
}

// SummarySection is one block of the review screen.
type SummarySection struct {
	Title  string       `json:"title"`
	Fields []FieldState `json:"fields"`
	Note   string       `json:"note,omitempty"`
}

// ApproveOnboardingInput finalises onboarding.
type ApproveOnboardingInput struct {
	ConversationID core.ID
	Actor          string
	ActorEmail     string
	TenantName     string
	TenantSlug     string
	Plan           tenancy.Plan
	IPAddress      string
	UserAgent      string
	// Demo marks the created tenant as the built-in demonstration tenant,
	// the only kind permitted to register a simulated AWS account.
	// tenancy.Tenant.Demo is immutable after creation by design — it is the
	// sole gate on simulated access, and toggling it later would change what
	// access modes are legal for accounts already registered — so creation is
	// the only moment it can be set, and this field is that moment. It is
	// deliberately NOT decoded from the HTTP request body (see
	// internal/transport/http/handlers_onboarding.go): only in-process
	// callers, meaning internal/app's demo seed, can set it.
	Demo bool
}

// OnboardingResult is what approval produced.
type OnboardingResult struct {
	Tenant      tenancy.Tenant `json:"tenant"`
	SpecVersion spec.Version   `json:"spec_version"`
	// NextSteps carries the AWS onboarding instructions: the role to create,
	// the external id to use, and the CloudFormation/Terraform snippet.
	NextSteps AWSOnboardingInstructions `json:"next_steps"`
}

// AWSOnboardingInstructions tells the customer exactly how to grant access.
type AWSOnboardingInstructions struct {
	ExternalID          string                     `json:"external_id"`
	TrustedPrincipalARN string                     `json:"trusted_principal_arn"`
	RoleNames           map[cloud.RoleScope]string `json:"role_names"`
	PolicyDocuments     map[cloud.RoleScope]string `json:"policy_documents"`
	CloudFormationURL   string                     `json:"cloudformation_url,omitempty"`
	TerraformModule     string                     `json:"terraform_module,omitempty"`
	Instructions        []string                   `json:"instructions"`
}

// SpecService manages specification versions after onboarding.
type SpecService interface {
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (spec.Version, error)
	GetActive(ctx context.Context, tenant core.TenantID) (spec.Version, error)
	ListVersions(ctx context.Context, tenant core.TenantID) ([]spec.Version, error)
	Diff(ctx context.Context, tenant core.TenantID, fromVersion, toVersion int) ([]spec.Change, error)
	// ProposeRevision creates a new draft from the active version plus a
	// patch, returning the draft and its diff for review.
	ProposeRevision(ctx context.Context, tenant core.TenantID, patch map[string]any, actor string) (spec.Version, error)
	Approve(ctx context.Context, tenant core.TenantID, versionID core.ID, actor string) (spec.Version, error)
	Reject(ctx context.Context, tenant core.TenantID, versionID core.ID, reason, actor string) error
	Validate(ctx context.Context, s spec.Spec) core.ValidationResult
	ExportYAML(ctx context.Context, tenant core.TenantID, versionID core.ID) ([]byte, error)
	ImportYAML(ctx context.Context, tenant core.TenantID, data []byte, actor string) (spec.Version, error)
}

// AWSAccountService onboards and verifies customer accounts.
type AWSAccountService interface {
	Register(ctx context.Context, tenant core.TenantID, in RegisterAccountInput) (cloud.AWSAccount, AWSOnboardingInstructions, error)
	Verify(ctx context.Context, tenant core.TenantID, accountID core.ID) (cloud.AWSAccount, ConnectionCheck, error)
	List(ctx context.Context, tenant core.TenantID) ([]cloud.AWSAccount, error)
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.AWSAccount, error)
	Suspend(ctx context.Context, tenant core.TenantID, id core.ID, reason string) error
	Remove(ctx context.Context, tenant core.TenantID, id core.ID) error
	// Instructions regenerates the onboarding guidance, including the exact
	// least-privilege policy documents derived from the registered
	// discoverers and executors.
	Instructions(ctx context.Context, tenant core.TenantID, accountID core.ID) (AWSOnboardingInstructions, error)
}

// RegisterAccountInput registers an AWS account.
type RegisterAccountInput struct {
	AccountID   core.AccountID
	Alias       string
	Environment core.Environment
	Regions     []core.Region
	AccessMode  cloud.AccessMode
	RoleARNs    map[cloud.RoleScope]core.ARN
	IsPayer     bool
	CURBucket   string
	CURPrefix   string
}

// DiscoveryService runs and reports estate discovery.
type DiscoveryService interface {
	// Run scans one account (or all of a tenant's accounts when accountID is
	// empty) and returns the run record.
	Run(ctx context.Context, tenant core.TenantID, in DiscoveryRequest) (DiscoveryRun, error)
	Get(ctx context.Context, tenant core.TenantID, runID core.ID) (DiscoveryRun, error)
	ListRuns(ctx context.Context, tenant core.TenantID, limit int) ([]DiscoveryRun, error)
	Status(ctx context.Context, tenant core.TenantID) (DiscoveryStatus, error)
}

// DiscoveryRequest parameterises a scan.
type DiscoveryRequest struct {
	AccountID core.ID
	Regions   []core.Region
	Services  []string
	Trigger   string
	// IncludeMetrics collects utilisation alongside inventory. It is the
	// expensive part of a scan, so a quick inventory refresh can skip it.
	IncludeMetrics bool
	IncludeCost    bool
	Async          bool
}

// DiscoveryStatus is the tenant-level discovery health summary.
type DiscoveryStatus struct {
	LastRunAt        time.Time      `json:"last_run_at"`
	ResourceCount    int            `json:"resource_count"`
	AccountsCovered  int            `json:"accounts_covered"`
	AccountsTotal    int            `json:"accounts_total"`
	Coverage         float64        `json:"coverage"`
	InProgress       bool           `json:"in_progress"`
	RecentRuns       []DiscoveryRun `json:"recent_runs,omitempty"`
	PermissionIssues []string       `json:"permission_issues,omitempty"`
}

// TwinService serves the Architecture Digital Twin.
type TwinService interface {
	// Graph returns the twin projected onto one view: architecture, cost,
	// performance, reliability, security or economic footprint.
	Graph(ctx context.Context, tenant core.TenantID, q TwinQuery) (TwinGraph, error)
	// Node returns one node with its full detail panel.
	Node(ctx context.Context, tenant core.TenantID, resourceID core.ID) (TwinNode, error)
	// CostFlow returns the money-flow projection: how spend accumulates along
	// the request path.
	CostFlow(ctx context.Context, tenant core.TenantID, q TwinQuery) (CostFlowGraph, error)
	// Rebuild recomputes the twin after discovery or a cost refresh.
	Rebuild(ctx context.Context, tenant core.TenantID) (TwinStats, error)
	// Dependents returns the transitive dependents of a resource, which is
	// what blast radius is computed from.
	Dependents(ctx context.Context, tenant core.TenantID, resourceID core.ID, maxDepth int) ([]TwinNode, error)
}

// TwinQuery narrows and projects the graph.
type TwinQuery struct {
	View           string // architecture | cost | performance | reliability | security | economics
	AccountIDs     []core.AccountID
	Regions        []core.Region
	Environments   []core.Environment
	ApplicationID  core.ID
	WorkloadID     core.ID
	Kinds          []cloud.Kind
	RootID         core.ID
	MaxDepth       int
	MinMonthlyCost core.Money
	Search         string
	// Collapse groups low-value leaf nodes so a 40,000-resource estate is
	// still navigable.
	Collapse bool
}

// TwinGraph is the renderable graph.
type TwinGraph struct {
	Nodes []TwinNode `json:"nodes"`
	Edges []TwinEdge `json:"edges"`
	Stats TwinStats  `json:"stats"`
	View  string     `json:"view"`
	// Legend describes the active colour and size encodings for the view.
	Legend    map[string]string `json:"legend,omitempty"`
	Truncated bool              `json:"truncated"`
}

// TwinNode is one graph node with everything the detail panel shows.
type TwinNode struct {
	ID          core.ID          `json:"id"`
	Label       string           `json:"label"`
	Kind        cloud.Kind       `json:"kind"`
	Category    cloud.Category   `json:"category"`
	Service     string           `json:"service"`
	AccountID   core.AccountID   `json:"account_id"`
	Region      core.Region      `json:"region"`
	AZ          string           `json:"availability_zone,omitempty"`
	Environment core.Environment `json:"environment"`
	State       cloud.State      `json:"state"`

	MonthlyCost       core.Money `json:"monthly_cost"`
	EconomicFootprint core.Money `json:"economic_footprint"`
	CostShare         float64    `json:"cost_share"`

	CPU          *core.Percentiles `json:"cpu,omitempty"`
	Memory       *core.Percentiles `json:"memory,omitempty"`
	LatencyP99   float64           `json:"latency_p99_ms,omitempty"`
	ErrorRate    float64           `json:"error_rate,omitempty"`
	Availability float64           `json:"availability,omitempty"`

	Risk        core.RiskLevel   `json:"risk"`
	Criticality core.Criticality `json:"criticality"`
	Owner       string           `json:"owner,omitempty"`
	Application string           `json:"application,omitempty"`
	Workload    string           `json:"workload,omitempty"`
	Tags        core.Tags        `json:"tags,omitempty"`

	FindingCount    int        `json:"finding_count"`
	PotentialSaving core.Money `json:"potential_saving"`
	// Group is set when the node collapses several similar resources.
	Group      bool `json:"group,omitempty"`
	GroupCount int  `json:"group_count,omitempty"`
}

// TwinEdge is one graph edge.
type TwinEdge struct {
	From       core.ID            `json:"from"`
	To         core.ID            `json:"to"`
	Kind       cloud.RelationKind `json:"kind"`
	Label      string             `json:"label,omitempty"`
	Weight     float64            `json:"weight"`
	Confidence core.Confidence    `json:"confidence"`
	// CostFlow is the money attributed along this edge in the cost view.
	CostFlow core.Money `json:"cost_flow,omitempty"`
}

// TwinStats summarises the graph.
type TwinStats struct {
	NodeCount    int        `json:"node_count"`
	EdgeCount    int        `json:"edge_count"`
	TotalCost    core.Money `json:"total_cost"`
	Environments int        `json:"environments"`
	Accounts     int        `json:"accounts"`
	Regions      int        `json:"regions"`
	Applications int        `json:"applications"`
	OrphanCount  int        `json:"orphan_count"`
	Completeness float64    `json:"completeness"`
	BuiltAt      time.Time  `json:"built_at"`
}

// CostFlowGraph is the Sankey-style money-flow projection.
type CostFlowGraph struct {
	Levels       []CostFlowLevel `json:"levels"`
	Links        []CostFlowLink  `json:"links"`
	Total        core.Money      `json:"total"`
	Unattributed core.Money      `json:"unattributed"`
	Period       core.Period     `json:"period"`
}

// CostFlowLevel is one tier of the flow diagram.
type CostFlowLevel struct {
	Depth int            `json:"depth"`
	Nodes []CostFlowNode `json:"nodes"`
}

// CostFlowNode is one node in the flow diagram.
type CostFlowNode struct {
	ID     core.ID    `json:"id"`
	Label  string     `json:"label"`
	Kind   string     `json:"kind"`
	Amount core.Money `json:"amount"`
	Share  float64    `json:"share"`
}

// CostFlowLink is money moving between two flow nodes.
type CostFlowLink struct {
	From   core.ID    `json:"from"`
	To     core.ID    `json:"to"`
	Amount core.Money `json:"amount"`
	Basis  string     `json:"basis"`
}

// CostService serves cost intelligence.
type CostService interface {
	Ingest(ctx context.Context, tenant core.TenantID, accountID core.ID, period core.Period) (IngestResult, error)
	Summary(ctx context.Context, tenant core.TenantID, period core.Period) (CostSummary, error)
	Series(ctx context.Context, tenant core.TenantID, f CostFilter) (cost.Series, error)
	Breakdown(ctx context.Context, tenant core.TenantID, f CostFilter, dimension string) (cost.Breakdown, error)
	Forecast(ctx context.Context, tenant core.TenantID, f CostFilter, horizon core.Period) (cost.Forecast, error)
	DetectAnomalies(ctx context.Context, tenant core.TenantID, lookback core.Period) ([]cost.Anomaly, error)
	ListAnomalies(ctx context.Context, tenant core.TenantID, from, to time.Time) ([]cost.Anomaly, error)
	// Explain answers "why did cost change", decomposing a movement into
	// ranked contributors rather than merely reporting it.
	Explain(ctx context.Context, tenant core.TenantID, current, baseline core.Period) (CostExplanation, error)
}

// IngestResult reports a cost ingestion.
type IngestResult struct {
	RecordsIngested  int         `json:"records_ingested"`
	Period           core.Period `json:"period"`
	Source           string      `json:"source"`
	TotalCost        core.Money  `json:"total_cost"`
	ResourceCoverage float64     `json:"resource_coverage"`
	DurationMS       int64       `json:"duration_ms"`
}

// CostSummary is the cost-intelligence headline.
type CostSummary struct {
	Period         core.Period    `json:"period"`
	Total          core.Money     `json:"total"`
	DailyAverage   core.Money     `json:"daily_average"`
	MonthToDate    core.Money     `json:"month_to_date"`
	PriorMonth     core.Money     `json:"prior_month"`
	ChangePct      float64        `json:"change_pct"`
	Forecast       cost.Forecast  `json:"forecast"`
	ByService      cost.Breakdown `json:"by_service"`
	ByAccount      cost.Breakdown `json:"by_account"`
	ByEnvironment  cost.Breakdown `json:"by_environment"`
	ByApplication  cost.Breakdown `json:"by_application"`
	Trend          cost.Series    `json:"trend"`
	OpenAnomalies  int            `json:"open_anomalies"`
	LastIngestedAt time.Time      `json:"last_ingested_at"`
	Freshness      string         `json:"freshness"`
}

// CostExplanation decomposes a cost movement.
type CostExplanation struct {
	CurrentPeriod  core.Period         `json:"current_period"`
	BaselinePeriod core.Period         `json:"baseline_period"`
	CurrentTotal   core.Money          `json:"current_total"`
	BaselineTotal  core.Money          `json:"baseline_total"`
	Delta          core.Money          `json:"delta"`
	DeltaPct       float64             `json:"delta_pct"`
	Contributors   []cost.Contribution `json:"contributors"`
	Narrative      string              `json:"narrative"`
	// LinkedChanges connects a cost movement to CloudOptix's own executed
	// changes and to compiled infrastructure changes, which is how "what
	// Terraform change increased cost" is answered.
	LinkedChanges []LinkedChange `json:"linked_changes,omitempty"`
}

// LinkedChange is a change correlated with a cost movement.
type LinkedChange struct {
	Kind        string     `json:"kind"` // "execution" | "compilation" | "discovery_delta"
	ID          core.ID    `json:"id"`
	Label       string     `json:"label"`
	At          time.Time  `json:"at"`
	CostImpact  core.Money `json:"cost_impact"`
	Correlation float64    `json:"correlation"`
}

// EconomicsService serves architecture economics.
type EconomicsService interface {
	Compute(ctx context.Context, tenant core.TenantID, period core.Period) (EconomicsResult, error)
	Footprint(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID, period core.Period) (econ.Footprint, error)
	ListFootprints(ctx context.Context, tenant core.TenantID, scope econ.Scope, period core.Period) ([]econ.Footprint, error)

	UpsertTransaction(ctx context.Context, t econ.BusinessTransaction) (econ.BusinessTransaction, error)
	ListTransactions(ctx context.Context, tenant core.TenantID) ([]econ.BusinessTransaction, error)
	UnitEconomics(ctx context.Context, tenant core.TenantID, transactionID core.ID, period core.Period) (econ.UnitEconomics, error)
	UnitEconomicsHistory(ctx context.Context, tenant core.TenantID, transactionID core.ID, from, to time.Time) ([]econ.UnitEconomics, error)

	UpsertCostSLO(ctx context.Context, s econ.CostSLO) (econ.CostSLO, error)
	ListCostSLOs(ctx context.Context, tenant core.TenantID) ([]econ.CostSLO, error)
	DeleteCostSLO(ctx context.Context, tenant core.TenantID, id core.ID) error
	EvaluateSLOs(ctx context.Context, tenant core.TenantID) ([]econ.EconomicErrorBudget, error)
	BudgetStates(ctx context.Context, tenant core.TenantID) ([]econ.EconomicErrorBudget, error)

	EfficiencyScore(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID) (econ.EfficiencyScore, error)
	ExecutiveSummary(ctx context.Context, tenant core.TenantID) (ExecutiveSummary, error)
}

// EconomicsResult reports an economics computation run.
type EconomicsResult struct {
	Period             core.Period `json:"period"`
	FootprintsComputed int         `json:"footprints_computed"`
	TotalAttributed    core.Money  `json:"total_attributed"`
	TotalUnattributed  core.Money  `json:"total_unattributed"`
	Coverage           float64     `json:"coverage"`
	TransactionsPriced int         `json:"transactions_priced"`
	DurationMS         int64       `json:"duration_ms"`
}

// ExecutiveSummary is the board-level view.
type ExecutiveSummary struct {
	Period             core.Period                `json:"period"`
	MonthlySpend       core.Money                 `json:"monthly_spend"`
	ForecastMonthEnd   core.Money                 `json:"forecast_month_end"`
	PriorMonthSpend    core.Money                 `json:"prior_month_spend"`
	SpendChangePct     float64                    `json:"spend_change_pct"`
	PotentialSavings   core.Money                 `json:"potential_savings"`
	RealizedSavings    core.Money                 `json:"realized_savings"`
	RealizedAnnualized core.Money                 `json:"realized_annualized"`
	WastePct           float64                    `json:"waste_pct"`
	EfficiencyScore    float64                    `json:"efficiency_score"`
	EfficiencyGrade    string                     `json:"efficiency_grade"`
	CostSLOsHealthy    int                        `json:"cost_slos_healthy"`
	CostSLOsAtRisk     int                        `json:"cost_slos_at_risk"`
	CostSLOsBreached   int                        `json:"cost_slos_breached"`
	BudgetStates       []econ.EconomicErrorBudget `json:"budget_states,omitempty"`
	TopOpportunities   []optimize.Recommendation  `json:"top_opportunities,omitempty"`
	TopTransactions    []econ.UnitEconomics       `json:"top_transactions,omitempty"`
	SavingsFunnel      execute.Funnel             `json:"savings_funnel"`
	GeneratedAt        time.Time                  `json:"generated_at"`
}

// OptimizationService generates and serves recommendations.
type OptimizationService interface {
	// Analyze runs the full rule set over the current model and produces a
	// fresh, ranked recommendation set.
	Analyze(ctx context.Context, tenant core.TenantID, in AnalyzeRequest) (AnalyzeResult, error)
	List(ctx context.Context, tenant core.TenantID, f RecommendationFilter, opts ListOptions) (Page[optimize.Recommendation], error)
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (optimize.Recommendation, error)
	Summary(ctx context.Context, tenant core.TenantID) (RecommendationSummary, error)
	Dismiss(ctx context.Context, tenant core.TenantID, id core.ID, reason, actor string) error
	Snooze(ctx context.Context, tenant core.TenantID, id core.ID, until time.Time, reason, actor string) error
	// Explain returns the full reasoning packet for one recommendation:
	// evidence, confidence inputs, blast radius, risk factors and the policy
	// decision.
	Explain(ctx context.Context, tenant core.TenantID, id core.ID) (RecommendationExplanation, error)
	// ListRules exposes the rule catalog with each rule's current calibration.
	ListRules(ctx context.Context, tenant core.TenantID) ([]RuleInfo, error)
}

// AnalyzeRequest parameterises an optimization run.
type AnalyzeRequest struct {
	AccountIDs   []core.AccountID
	Environments []core.Environment
	Categories   []optimize.Category
	RuleIDs      []optimize.RuleID
	Formula      *optimize.PriorityFormula
	// GenerateNarratives asks the LLM to write human explanations. It is
	// optional and never affects which recommendations exist or their scores.
	GenerateNarratives bool
	Async              bool
}

// AnalyzeResult reports an optimization run.
type AnalyzeResult struct {
	RunID                  core.ID `json:"run_id"`
	ResourcesAnalyzed      int     `json:"resources_analyzed"`
	RulesEvaluated         int     `json:"rules_evaluated"`
	FindingsProduced       int     `json:"findings_produced"`
	RecommendationsCreated int     `json:"recommendations_created"`
	Superseded             int     `json:"superseded"`
	// MutuallyExclusiveAlternatives is how many of RecommendationsCreated are
	// alternatives within a conflict group and therefore excluded from
	// TotalMonthlySaving. Reporting it alongside the total is what stops the
	// exclusion looking like recommendations went missing.
	MutuallyExclusiveAlternatives int        `json:"mutually_exclusive_alternatives"`
	TotalMonthlySaving            core.Money `json:"total_monthly_saving"`
	TotalAnnualSaving             core.Money `json:"total_annual_saving"`
	AutoExecutable                int        `json:"auto_executable"`
	RequiringApproval             int        `json:"requiring_approval"`
	Prohibited                    int        `json:"prohibited"`
	DurationMS                    int64      `json:"duration_ms"`
	Warnings                      []string   `json:"warnings,omitempty"`
}

// RecommendationExplanation is the full reasoning packet.
type RecommendationExplanation struct {
	Recommendation   optimize.Recommendation    `json:"recommendation"`
	Evidence         []optimize.Evidence        `json:"evidence"`
	ConfidenceInputs []optimize.ConfidenceInput `json:"confidence_inputs"`
	RiskFactors      []optimize.RiskFactor      `json:"risk_factors"`
	BlastRadius      optimize.BlastRadius       `json:"blast_radius"`
	AffectedNodes    []TwinNode                 `json:"affected_nodes,omitempty"`
	// Alternatives are the other recommendations in this one's conflict
	// group, ranked, so the detail view can say "there are three ways to fix
	// this, here is the one we recommend and why" rather than presenting one
	// approach as the only one. Empty when this recommendation competes with
	// nothing.
	Alternatives    []optimize.Recommendation `json:"alternatives,omitempty"`
	PolicyDecision  *govern.Decision          `json:"policy_decision,omitempty"`
	Calibration     *execute.RuleCalibration  `json:"calibration,omitempty"`
	RollbackSummary string                    `json:"rollback_summary,omitempty"`
	Narrative       string                    `json:"narrative,omitempty"`
	SimilarOutcomes []execute.Outcome         `json:"similar_outcomes,omitempty"`
}

// RuleInfo describes one optimization rule for the rules catalog.
type RuleInfo struct {
	ID          optimize.RuleID          `json:"id"`
	Name        string                   `json:"name"`
	Category    optimize.Category        `json:"category"`
	Action      optimize.ActionType      `json:"action"`
	Description string                   `json:"description"`
	Kinds       []cloud.Kind             `json:"kinds"`
	Enabled     bool                     `json:"enabled"`
	Thresholds  map[string]any           `json:"thresholds,omitempty"`
	Calibration *execute.RuleCalibration `json:"calibration,omitempty"`
}

// SimulationService runs the mutation, counterfactual and compiler engines.
type SimulationService interface {
	MutateArchitecture(ctx context.Context, tenant core.TenantID, in MutationRequest) (simulate.Simulation, error)
	Counterfactual(ctx context.Context, tenant core.TenantID, s simulate.Scenario) (simulate.Counterfactual, error)
	GetSimulation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Simulation, error)
	ListSimulations(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[simulate.Simulation], error)

	Compile(ctx context.Context, tenant core.TenantID, in CompileRequest) (simulate.CompilationResult, error)
	GetCompilation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.CompilationResult, error)
	RunRegression(ctx context.Context, tenant core.TenantID, compilationID core.ID, suiteName string) (simulate.RegressionReport, error)
	UpsertRegressionSuite(ctx context.Context, s simulate.RegressionSuite) (simulate.RegressionSuite, error)
	ListRegressionSuites(ctx context.Context, tenant core.TenantID) ([]simulate.RegressionSuite, error)
}

// MutationRequest asks for alternative architectures.
type MutationRequest struct {
	Scope         string // application | workload | account
	ScopeID       core.ID
	Name          string
	RiskTolerance string
	Patterns      []string
	MaxCandidates int
	RequestedBy   string
}

// CompileRequest asks the cost compiler to price a change set.
type CompileRequest struct {
	Source      simulate.SourceKind
	Label       string
	Content     []byte
	Region      core.Region
	AccountID   core.AccountID
	Environment core.Environment
	// Assumptions overrides the compiler's defaults for usage-dependent
	// resources, which is how a team encodes "this Lambda runs 40M times a
	// month" once rather than arguing about the estimate every PR.
	Assumptions map[string]float64
	RequestedBy string
}

// GovernanceService serves policy and approvals.
type GovernanceService interface {
	GetPolicy(ctx context.Context, tenant core.TenantID) (govern.Policy, error)
	ListPolicyVersions(ctx context.Context, tenant core.TenantID, name string) ([]govern.Policy, error)
	SavePolicy(ctx context.Context, tenant core.TenantID, p govern.Policy, actor string) (govern.Policy, error)
	ValidatePolicy(ctx context.Context, p govern.Policy) core.ValidationResult
	ActivatePolicy(ctx context.Context, tenant core.TenantID, id core.ID, actor string) error
	// Evaluate runs a policy decision for a recommendation without executing
	// anything, which is what the recommendation list uses to show each item's
	// governance state.
	Evaluate(ctx context.Context, tenant core.TenantID, recommendationID core.ID) (govern.Decision, error)
	// Simulate answers "what would this policy do to my current
	// recommendations", so a policy edit can be reviewed before activation.
	Simulate(ctx context.Context, tenant core.TenantID, p govern.Policy) (PolicySimulation, error)

	ListApprovals(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[govern.Request], error)
	GetApproval(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Request, error)
	Decide(ctx context.Context, tenant core.TenantID, id core.ID, resp govern.Response) (govern.Request, error)
	RequestApproval(ctx context.Context, r govern.Request) (govern.Request, error)
}

// PolicySimulation is the impact of a proposed policy.
type PolicySimulation struct {
	PolicyName           string                   `json:"policy_name"`
	Evaluated            int                      `json:"evaluated"`
	AutoExecute          int                      `json:"auto_execute"`
	RequireApproval      int                      `json:"require_approval"`
	Prohibited           int                      `json:"prohibited"`
	Advisory             int                      `json:"advisory"`
	Changes              []PolicySimulationChange `json:"changes,omitempty"`
	AutoExecutableSaving core.Money               `json:"auto_executable_saving"`
	Warnings             []string                 `json:"warnings,omitempty"`
}

// PolicySimulationChange is one recommendation whose governance outcome would
// change under the proposed policy.
type PolicySimulationChange struct {
	RecommendationID core.ID       `json:"recommendation_id"`
	Title            string        `json:"title"`
	From             govern.Effect `json:"from"`
	To               govern.Effect `json:"to"`
	MonthlySaving    core.Money    `json:"monthly_saving"`
}

// AutomationService plans, executes, validates and rolls back changes.
type AutomationService interface {
	// PlanExecution builds an execution plan from an approved
	// recommendation. It performs no mutation and always constructs a
	// rollback plan.
	PlanExecution(ctx context.Context, tenant core.TenantID, recommendationID core.ID, in PlanOptions) (execute.Plan, error)
	GetPlan(ctx context.Context, tenant core.TenantID, planID core.ID) (execute.Plan, error)
	ListPlans(ctx context.Context, tenant core.TenantID, states []execute.PlanState, opts ListOptions) (Page[execute.Plan], error)
	// Execute runs an approved plan. It re-checks policy and approval state
	// immediately before touching AWS.
	Execute(ctx context.Context, tenant core.TenantID, planID core.ID, actor string) (execute.Plan, error)
	Cancel(ctx context.Context, tenant core.TenantID, planID core.ID, reason, actor string) error
	// Validate evaluates a completed change against its validation plan.
	Validate(ctx context.Context, tenant core.TenantID, planID core.ID) (execute.ValidationResult, error)
	// Rollback reverses an executed plan.
	Rollback(ctx context.Context, tenant core.TenantID, planID core.ID, reason, actor string) (execute.Plan, error)
	// ProcessAutonomous is the worker entry point: it finds recommendations
	// the policy permits to auto-execute, plans them, executes them within
	// the maintenance window and concurrency limits, and schedules
	// validation.
	ProcessAutonomous(ctx context.Context, tenant core.TenantID) (AutonomousRunResult, error)
	Funnel(ctx context.Context, tenant core.TenantID, period core.Period) (execute.Funnel, error)
	// Learn recomputes rule calibrations from observed outcomes.
	Learn(ctx context.Context, tenant core.TenantID) (LearningResult, error)
}

// PlanOptions parameterises plan construction.
type PlanOptions struct {
	DryRun       bool
	ScheduledFor *time.Time
	RequestedBy  string
}

// AutonomousRunResult reports an autonomous optimization cycle.
type AutonomousRunResult struct {
	Considered    int            `json:"considered"`
	Planned       int            `json:"planned"`
	Executed      int            `json:"executed"`
	Skipped       int            `json:"skipped"`
	Failed        int            `json:"failed"`
	RolledBack    int            `json:"rolled_back"`
	MonthlySaving core.Money     `json:"monthly_saving"`
	SkipReasons   map[string]int `json:"skip_reasons,omitempty"`
	DurationMS    int64          `json:"duration_ms"`
}

// LearningResult reports a calibration pass.
type LearningResult struct {
	OutcomesConsidered int                                         `json:"outcomes_considered"`
	RulesCalibrated    int                                         `json:"rules_calibrated"`
	Calibrations       map[optimize.RuleID]execute.RuleCalibration `json:"calibrations,omitempty"`
	MeanAccuracy       float64                                     `json:"mean_accuracy"`
}

// CopilotService answers questions grounded in tenant data.
type CopilotService interface {
	Ask(ctx context.Context, tenant core.TenantID, in CopilotRequest) (CopilotAnswer, error)
	GetConversation(ctx context.Context, tenant core.TenantID, id core.ID) (Conversation, error)
	ListConversations(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[Conversation], error)
	// Suggestions returns the questions worth asking given the tenant's
	// current state, which is what makes an empty copilot screen useful.
	Suggestions(ctx context.Context, tenant core.TenantID) ([]string, error)
}

// CopilotRequest is one copilot question.
type CopilotRequest struct {
	ConversationID core.ID
	Question       string
	Actor          string
	// Context narrows the question to a resource, application or
	// recommendation the user is looking at.
	ContextKind string
	ContextID   core.ID
}

// CopilotAnswer is a grounded reply.
type CopilotAnswer struct {
	ConversationID  core.ID             `json:"conversation_id"`
	Answer          string              `json:"answer"`
	Citations       []Citation          `json:"citations,omitempty"`
	ToolCalls       []ToolResult        `json:"tool_calls,omitempty"`
	Retrieved       []RetrievedDocument `json:"retrieved,omitempty"`
	Grounded        bool                `json:"grounded"`
	GroundingIssues []string            `json:"grounding_issues,omitempty"`
	Degraded        bool                `json:"degraded"`
	FollowUps       []string            `json:"follow_ups,omitempty"`
	// Data carries a structured payload the UI can render as a chart or table
	// instead of prose.
	Data      map[string]any `json:"data,omitempty"`
	LatencyMS int64          `json:"latency_ms"`
}

// AuditService serves the audit trail.
type AuditService interface {
	Record(ctx context.Context, r AuditEntry) error
	Query(ctx context.Context, tenant core.TenantID, q AuditQuery) (Page[AuditEntry], error)
	Verify(ctx context.Context, tenant core.TenantID, from, to time.Time) (any, error)
	// Timeline assembles the full story of one change: recommendation,
	// policy decision, approval, execution steps, validation and savings.
	Timeline(ctx context.Context, tenant core.TenantID, recommendationID core.ID) ([]AuditEntry, error)
}

// AuditEntry is the transport-facing audit record shape.
type AuditEntry struct {
	ID        core.ID        `json:"id"`
	Sequence  int64          `json:"sequence"`
	Action    string         `json:"action"`
	Outcome   string         `json:"outcome"`
	Actor     string         `json:"actor"`
	Machine   bool           `json:"machine"`
	Subject   string         `json:"subject"`
	SubjectID core.ID        `json:"subject_id,omitempty"`
	Message   string         `json:"message"`
	Before    map[string]any `json:"before,omitempty"`
	After     map[string]any `json:"after,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	At        time.Time      `json:"at"`
	Hash      string         `json:"hash,omitempty"`
}

// AuditQuery filters the audit trail.
type AuditQuery struct {
	Actions   []string
	Actors    []string
	Outcomes  []string
	SubjectID core.ID
	From, To  time.Time
	Limit     int
	Cursor    string
}

// TenantService administers tenants and users.
type TenantService interface {
	Get(ctx context.Context, id core.TenantID) (tenancy.Tenant, error)
	Update(ctx context.Context, t tenancy.Tenant, actor string) (tenancy.Tenant, error)
	ListUsers(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[tenancy.User], error)
	InviteUser(ctx context.Context, tenant core.TenantID, email string, roles []core.Role, actor string) (tenancy.User, error)
	UpdateRoles(ctx context.Context, tenant core.TenantID, userID core.ID, roles []core.Role, actor string) error
	RemoveUser(ctx context.Context, tenant core.TenantID, userID core.ID, actor string) error
}

// Services bundles every driving port, which is what the HTTP router and the
// workers are constructed with.
type Services struct {
	Onboarding   OnboardingService
	Specs        SpecService
	AWSAccounts  AWSAccountService
	Discovery    DiscoveryService
	Twin         TwinService
	Costs        CostService
	Economics    EconomicsService
	Optimization OptimizationService
	Simulation   SimulationService
	Governance   GovernanceService
	Automation   AutomationService
	Copilot      CopilotService
	Audit        AuditService
	Tenants      TenantService
}
