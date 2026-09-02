// Package ports declares every interface the application layer depends on.
//
// This is the hexagon's edge. Application services are written exclusively
// against these interfaces; adapters in internal/adapters implement them
// against Postgres, Redis, AWS, an LLM provider, or in-memory fakes. The
// dependency rule is enforced in CI: nothing under internal/domain or
// internal/application may import internal/adapters.
//
// Every repository method takes a context carrying the caller's principal and
// an explicit core.TenantID. The redundancy is deliberate — the tenant is
// checked against the principal inside every implementation, so a service that
// forgets to scope a query fails closed rather than leaking.
//
// Traceability: SPEC-ARCH-003 (hexagonal ports), SPEC-SEC-003 (tenant isolation).
package ports

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
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

// Page is the cursor-paginated result envelope used by every list method.
// Offset pagination is deliberately absent: on a resource table with hundreds
// of thousands of rows per tenant it degrades badly and produces duplicates
// under concurrent discovery writes.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	Total      int    `json:"total,omitempty"`
}

// ListOptions is the common query envelope.
type ListOptions struct {
	Limit  int
	Cursor string
	SortBy string
	Desc   bool
}

// Normalize applies the platform's paging defaults and caps.
func (o ListOptions) Normalize() ListOptions {
	if o.Limit <= 0 {
		o.Limit = 50
	}
	if o.Limit > 500 {
		o.Limit = 500
	}
	return o
}

// TenantRepository stores tenants and their organizations.
type TenantRepository interface {
	Create(ctx context.Context, t tenancy.Tenant) error
	Get(ctx context.Context, id core.TenantID) (tenancy.Tenant, error)
	GetBySlug(ctx context.Context, slug string) (tenancy.Tenant, error)
	Update(ctx context.Context, t tenancy.Tenant) error
	List(ctx context.Context, opts ListOptions) (Page[tenancy.Tenant], error)
	CreateOrganization(ctx context.Context, o tenancy.Organization) error
	ListOrganizations(ctx context.Context, tenant core.TenantID) ([]tenancy.Organization, error)
}

// UserRepository stores users and their tenant memberships.
type UserRepository interface {
	Upsert(ctx context.Context, u tenancy.User) error
	GetBySubject(ctx context.Context, subject string) (tenancy.User, error)
	GetByEmail(ctx context.Context, email string) (tenancy.User, error)
	ListByTenant(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[tenancy.User], error)
	AddMembership(ctx context.Context, userID core.ID, m tenancy.Membership) error
	RemoveMembership(ctx context.Context, userID core.ID, tenant core.TenantID) error
}

// SpecRepository stores specification versions. There is no Update: an
// approved version is immutable, and a draft is replaced wholesale by
// SaveDraft. That is what makes the version history trustworthy.
type SpecRepository interface {
	SaveDraft(ctx context.Context, v spec.Version) error
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (spec.Version, error)
	GetVersion(ctx context.Context, tenant core.TenantID, specID core.ID, version int) (spec.Version, error)
	GetActive(ctx context.Context, tenant core.TenantID) (spec.Version, error)
	GetLatest(ctx context.Context, tenant core.TenantID, specID core.ID) (spec.Version, error)
	ListVersions(ctx context.Context, tenant core.TenantID, specID core.ID) ([]spec.Version, error)
	// Approve atomically freezes the given version and supersedes the prior
	// active one. It must be a single transaction: two active specifications
	// would mean two different configurations of the same engines.
	Approve(ctx context.Context, tenant core.TenantID, v spec.Version) error
	Reject(ctx context.Context, tenant core.TenantID, id core.ID, reason, by string) error
}

// AWSAccountRepository stores onboarded AWS accounts.
type AWSAccountRepository interface {
	Create(ctx context.Context, a cloud.AWSAccount) error
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.AWSAccount, error)
	GetByAccountID(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (cloud.AWSAccount, error)
	Update(ctx context.Context, a cloud.AWSAccount) error
	List(ctx context.Context, tenant core.TenantID) ([]cloud.AWSAccount, error)
	Delete(ctx context.Context, tenant core.TenantID, id core.ID) error
}

// ResourceFilter narrows a resource query.
type ResourceFilter struct {
	AccountIDs     []core.AccountID
	Regions        []core.Region
	Kinds          []cloud.Kind
	Categories     []cloud.Category
	Environments   []core.Environment
	ApplicationID  core.ID
	WorkloadID     core.ID
	States         []cloud.State
	TagKey         string
	TagValue       string
	Search         string
	MinMonthlyCost core.Money
	IncludeDeleted bool
}

// ResourceRepository stores the normalized resource model and topology.
type ResourceRepository interface {
	// UpsertBatch is idempotent on Resource.Key(). Discovery calls it with
	// thousands of rows, so implementations batch rather than iterate.
	UpsertBatch(ctx context.Context, tenant core.TenantID, resources []cloud.Resource) (upserted int, err error)
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Resource, error)
	GetByNativeID(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, native string) (cloud.Resource, error)
	List(ctx context.Context, tenant core.TenantID, f ResourceFilter, opts ListOptions) (Page[cloud.Resource], error)
	// LoadInventory returns the full filtered set for analysis. Rule
	// evaluation needs every resource at once; paginating it would make
	// cross-resource rules impossible.
	LoadInventory(ctx context.Context, tenant core.TenantID, f ResourceFilter) (*cloud.Inventory, error)
	// MarkAbsent tombstones resources not seen in a completed discovery scan,
	// scoped to what the scan actually covered so a partial scan never
	// deletes the estate.
	MarkAbsent(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, kinds []cloud.Kind, seenKeys []string, at time.Time) (marked int, err error)
	Count(ctx context.Context, tenant core.TenantID, f ResourceFilter) (int, error)

	ReplaceRelationships(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, edges []cloud.Relationship) error
	LoadTopology(ctx context.Context, tenant core.TenantID, f ResourceFilter) (*cloud.Topology, error)
}

// ApplicationRepository stores applications and workloads.
type ApplicationRepository interface {
	UpsertApplication(ctx context.Context, a cloud.Application) error
	GetApplication(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Application, error)
	ListApplications(ctx context.Context, tenant core.TenantID) ([]cloud.Application, error)
	UpsertWorkload(ctx context.Context, w cloud.Workload) error
	GetWorkload(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Workload, error)
	ListWorkloads(ctx context.Context, tenant core.TenantID, applicationID core.ID) ([]cloud.Workload, error)
}

// CostFilter narrows a cost query.
type CostFilter struct {
	Period        core.Period
	Granularity   cost.Granularity
	AccountIDs    []core.AccountID
	Regions       []core.Region
	Services      []string
	Environments  []core.Environment
	ResourceIDs   []core.ID
	ApplicationID core.ID
	ChargeTypes   []cost.ChargeType
	Basis         cost.AmortizationBasis
	TagKey        string
	TagValue      string
}

// CostRepository stores billed cost records and serves roll-ups.
type CostRepository interface {
	UpsertBatch(ctx context.Context, tenant core.TenantID, records []cost.Record) (int, error)
	// Series returns a time-bucketed total for the filter.
	Series(ctx context.Context, tenant core.TenantID, f CostFilter) (cost.Series, error)
	// Breakdown groups by a dimension: "service", "account", "region",
	// "environment", "usage_type", "application", "resource".
	Breakdown(ctx context.Context, tenant core.TenantID, f CostFilter, dimension string) (cost.Breakdown, error)
	Total(ctx context.Context, tenant core.TenantID, f CostFilter) (core.Money, error)
	// ByResource returns attributed cost keyed by resource id, which is what
	// the twin and the economics engine join against.
	ByResource(ctx context.Context, tenant core.TenantID, f CostFilter) (map[core.ID]core.Money, error)
	LatestIngestedAt(ctx context.Context, tenant core.TenantID) (time.Time, error)

	SaveAnomalies(ctx context.Context, tenant core.TenantID, anomalies []cost.Anomaly) error
	ListAnomalies(ctx context.Context, tenant core.TenantID, from, to time.Time, opts ListOptions) (Page[cost.Anomaly], error)
	AcknowledgeAnomaly(ctx context.Context, tenant core.TenantID, id core.ID, by string) error
}

// MetricQuery describes a utilisation series request.
type MetricQuery struct {
	ResourceID  core.ID
	Namespace   string
	MetricName  string
	Statistic   string
	Dimensions  map[string]string
	Period      core.Period
	StepSeconds int
}

// MetricSeries is a returned utilisation series.
type MetricSeries struct {
	ResourceID core.ID           `json:"resource_id"`
	MetricName string            `json:"metric_name"`
	Unit       string            `json:"unit"`
	Points     []MetricPoint     `json:"points"`
	Summary    core.Percentiles  `json:"summary"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
	Source     string            `json:"source"`
}

// MetricPoint is one observation.
type MetricPoint struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// MetricRepository stores the utilisation summaries discovery collects. Raw
// points are retained only for a short window; the percentile summaries that
// rules actually read are retained for the tenant's full retention period.
type MetricRepository interface {
	SaveSummaries(ctx context.Context, tenant core.TenantID, summaries []ResourceMetrics) error
	GetSummary(ctx context.Context, tenant core.TenantID, resourceID core.ID) (ResourceMetrics, error)
	LoadSummaries(ctx context.Context, tenant core.TenantID, resourceIDs []core.ID) (map[core.ID]ResourceMetrics, error)
	SaveSeries(ctx context.Context, tenant core.TenantID, series []MetricSeries) error
	GetSeries(ctx context.Context, tenant core.TenantID, q MetricQuery) (MetricSeries, error)
}

// ResourceMetrics is the per-resource utilisation summary the rule engine
// reads. Keeping named fields rather than a metric-name map means a rule that
// asks for CPU cannot silently receive memory.
type ResourceMetrics struct {
	ResourceID core.ID       `json:"resource_id"`
	TenantID   core.TenantID `json:"tenant_id"`
	Window     core.Period   `json:"window"`

	CPU         *core.Percentiles `json:"cpu,omitempty"`         // percent
	Memory      *core.Percentiles `json:"memory,omitempty"`      // percent
	DiskUsed    *core.Percentiles `json:"disk_used,omitempty"`   // percent
	NetworkIn   *core.Percentiles `json:"network_in,omitempty"`  // bytes/s
	NetworkOut  *core.Percentiles `json:"network_out,omitempty"` // bytes/s
	IOPS        *core.Percentiles `json:"iops,omitempty"`
	Throughput  *core.Percentiles `json:"throughput,omitempty"`  // bytes/s
	Requests    *core.Percentiles `json:"requests,omitempty"`    // count/s
	LatencyP99  *core.Percentiles `json:"latency_p99,omitempty"` // ms
	ErrorRate   *core.Percentiles `json:"error_rate,omitempty"`  // fraction
	Concurrency *core.Percentiles `json:"concurrency,omitempty"`
	Connections *core.Percentiles `json:"connections,omitempty"`

	// Custom carries service-specific metrics that do not fit the common
	// vocabulary, e.g. DynamoDB consumed capacity or Lambda init duration.
	Custom map[string]core.Percentiles `json:"custom,omitempty"`

	// Coverage is the fraction of the window with data. A resource with 12%
	// coverage is not a resource with low utilisation.
	Coverage    float64   `json:"coverage"`
	Source      string    `json:"source"`
	CollectedAt time.Time `json:"collected_at"`
}

// HasSignal reports whether enough telemetry exists to reason about the
// resource at all.
func (m ResourceMetrics) HasSignal(minCoverage float64) bool {
	return m.Coverage >= minCoverage && (m.CPU != nil || m.Memory != nil || m.Requests != nil || m.IOPS != nil)
}

// RecommendationFilter narrows a recommendation query.
type RecommendationFilter struct {
	Statuses           []optimize.Status
	Categories         []optimize.Category
	Actions            []optimize.ActionType
	RuleIDs            []optimize.RuleID
	Environments       []core.Environment
	AccountIDs         []core.AccountID
	ApplicationID      core.ID
	ResourceID         core.ID
	MinSaving          core.Money
	MinConfidence      float64
	MaxRisk            core.RiskLevel
	AutoExecutableOnly bool
}

// RecommendationRepository stores recommendations.
type RecommendationRepository interface {
	SaveBatch(ctx context.Context, tenant core.TenantID, recs []optimize.Recommendation) error
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (optimize.Recommendation, error)
	List(ctx context.Context, tenant core.TenantID, f RecommendationFilter, opts ListOptions) (Page[optimize.Recommendation], error)
	UpdateStatus(ctx context.Context, tenant core.TenantID, id core.ID, status optimize.Status, reason, by string) error
	Update(ctx context.Context, rec optimize.Recommendation) error
	// SupersedeStale marks open recommendations from an earlier analysis run
	// as superseded, so the list never mixes two generations of advice.
	SupersedeStale(ctx context.Context, tenant core.TenantID, before time.Time, keepIDs []core.ID) (int, error)
	Summary(ctx context.Context, tenant core.TenantID) (RecommendationSummary, error)
}

// RecommendationSummary is the dashboard roll-up.
//
// Counts and money deliberately answer different questions. Open and
// ByCategory count every open recommendation, alternatives included, because
// an alternative is a real thing a user can choose. TotalMonthlySaving and
// SavingByCategory count primaries only, because at most one member of a
// conflict group can be applied and summing them all would report money the
// estate does not hold (see optimize/conflict.go).
// MutuallyExclusiveAlternatives is the reconciling number between the two.
type RecommendationSummary struct {
	Open                          int                              `json:"open"`
	TotalMonthlySaving            core.Money                       `json:"total_monthly_saving"`
	ByCategory                    map[optimize.Category]int        `json:"by_category"`
	SavingByCategory              map[optimize.Category]core.Money `json:"saving_by_category"`
	ByRisk                        map[core.RiskLevel]int           `json:"by_risk"`
	AutoExecutable                int                              `json:"auto_executable"`
	AwaitingApproval              int                              `json:"awaiting_approval"`
	MutuallyExclusiveAlternatives int                              `json:"mutually_exclusive_alternatives"`
}

// PolicyRepository stores versioned policies and decision records.
type PolicyRepository interface {
	Save(ctx context.Context, p govern.Policy) error
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Policy, error)
	GetActive(ctx context.Context, tenant core.TenantID) (govern.Policy, error)
	ListVersions(ctx context.Context, tenant core.TenantID, name string) ([]govern.Policy, error)
	Activate(ctx context.Context, tenant core.TenantID, id core.ID, by string) error
	SaveDecision(ctx context.Context, d govern.Decision) error
	GetDecision(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Decision, error)
	ListDecisions(ctx context.Context, tenant core.TenantID, recommendationID core.ID) ([]govern.Decision, error)
}

// ApprovalRepository stores approval requests.
type ApprovalRepository interface {
	Create(ctx context.Context, r govern.Request) error
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Request, error)
	Update(ctx context.Context, r govern.Request) error
	ListPending(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[govern.Request], error)
	ListBySubject(ctx context.Context, tenant core.TenantID, kind govern.SubjectKind, subjectID core.ID) ([]govern.Request, error)
	ExpireOverdue(ctx context.Context, now time.Time) (int, error)
}

// ExecutionRepository stores plans, snapshots and their results.
type ExecutionRepository interface {
	CreatePlan(ctx context.Context, p execute.Plan) error
	GetPlan(ctx context.Context, tenant core.TenantID, id core.ID) (execute.Plan, error)
	UpdatePlan(ctx context.Context, p execute.Plan) error
	ListPlans(ctx context.Context, tenant core.TenantID, states []execute.PlanState, opts ListOptions) (Page[execute.Plan], error)
	// ClaimDueePlans atomically leases scheduled plans to one worker, so two
	// workers can never execute the same change.
	ClaimDuePlans(ctx context.Context, now time.Time, workerID string, limit int) ([]execute.Plan, error)
	SaveSnapshot(ctx context.Context, s execute.Snapshot) error
	GetSnapshot(ctx context.Context, tenant core.TenantID, planID core.ID, resourceID core.ID) (execute.Snapshot, error)
	SaveValidation(ctx context.Context, v execute.ValidationResult) error
	GetValidation(ctx context.Context, tenant core.TenantID, planID core.ID) (execute.ValidationResult, error)
	// ClaimPlansAwaitingValidation leases plans whose observation window has
	// closed.
	ClaimPlansAwaitingValidation(ctx context.Context, now time.Time, workerID string, limit int) ([]execute.Plan, error)
}

// SavingsRepository stores the savings lifecycle and the learning corpus.
type SavingsRepository interface {
	Save(ctx context.Context, r execute.SavingsRecord) error
	Get(ctx context.Context, tenant core.TenantID, recommendationID core.ID) (execute.SavingsRecord, error)
	List(ctx context.Context, tenant core.TenantID, period core.Period) ([]execute.SavingsRecord, error)
	Funnel(ctx context.Context, tenant core.TenantID, period core.Period) (execute.Funnel, error)

	SaveOutcome(ctx context.Context, o execute.Outcome) error
	ListOutcomes(ctx context.Context, tenant core.TenantID, ruleID optimize.RuleID, limit int) ([]execute.Outcome, error)
	SaveCalibration(ctx context.Context, c execute.RuleCalibration) error
	LoadCalibrations(ctx context.Context, tenant core.TenantID) (map[optimize.RuleID]execute.RuleCalibration, error)
}

// EconomicsRepository stores footprints, transactions, unit economics, SLOs
// and efficiency scores.
type EconomicsRepository interface {
	SaveFootprints(ctx context.Context, tenant core.TenantID, fps []econ.Footprint) error
	GetFootprint(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID, period core.Period) (econ.Footprint, error)
	ListFootprints(ctx context.Context, tenant core.TenantID, scope econ.Scope, period core.Period) ([]econ.Footprint, error)

	UpsertTransaction(ctx context.Context, t econ.BusinessTransaction) error
	GetTransaction(ctx context.Context, tenant core.TenantID, id core.ID) (econ.BusinessTransaction, error)
	GetTransactionByName(ctx context.Context, tenant core.TenantID, name string) (econ.BusinessTransaction, error)
	ListTransactions(ctx context.Context, tenant core.TenantID) ([]econ.BusinessTransaction, error)

	SaveUnitEconomics(ctx context.Context, tenant core.TenantID, ue []econ.UnitEconomics) error
	ListUnitEconomics(ctx context.Context, tenant core.TenantID, transactionID core.ID, from, to time.Time) ([]econ.UnitEconomics, error)

	UpsertCostSLO(ctx context.Context, s econ.CostSLO) error
	GetCostSLO(ctx context.Context, tenant core.TenantID, id core.ID) (econ.CostSLO, error)
	ListCostSLOs(ctx context.Context, tenant core.TenantID) ([]econ.CostSLO, error)
	DeleteCostSLO(ctx context.Context, tenant core.TenantID, id core.ID) error
	SaveBudgetState(ctx context.Context, b econ.EconomicErrorBudget) error
	ListBudgetStates(ctx context.Context, tenant core.TenantID) ([]econ.EconomicErrorBudget, error)

	SaveEfficiencyScore(ctx context.Context, s econ.EfficiencyScore) error
	GetEfficiencyScore(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID) (econ.EfficiencyScore, error)
	ListEfficiencyScores(ctx context.Context, tenant core.TenantID, scope econ.Scope) ([]econ.EfficiencyScore, error)
}

// SimulationRepository stores simulations, counterfactuals and compilations.
type SimulationRepository interface {
	SaveSimulation(ctx context.Context, s simulate.Simulation) error
	GetSimulation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Simulation, error)
	ListSimulations(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[simulate.Simulation], error)

	SaveCounterfactual(ctx context.Context, c simulate.Counterfactual) error
	GetCounterfactual(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Counterfactual, error)

	SaveCompilation(ctx context.Context, c simulate.CompilationResult) error
	GetCompilation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.CompilationResult, error)
	ListCompilations(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[simulate.CompilationResult], error)

	SaveRegressionSuite(ctx context.Context, s simulate.RegressionSuite) error
	GetRegressionSuite(ctx context.Context, tenant core.TenantID, name string) (simulate.RegressionSuite, error)
	ListRegressionSuites(ctx context.Context, tenant core.TenantID) ([]simulate.RegressionSuite, error)
	SaveRegressionReport(ctx context.Context, r simulate.RegressionReport) error
	GetRegressionReport(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.RegressionReport, error)
}

// AuditRepository appends and verifies the tamper-evident log.
type AuditRepository interface {
	// Append seals the record against the tenant's current chain head. It
	// must serialise per tenant; the sequence and hash chain depend on it.
	Append(ctx context.Context, r audit.Record) (audit.Record, error)
	Query(ctx context.Context, q audit.Query) (Page[audit.Record], error)
	Verify(ctx context.Context, tenant core.TenantID, from, to time.Time) (audit.ChainVerification, error)
	Head(ctx context.Context, tenant core.TenantID) (prevHash string, sequence int64, err error)
}

// DiscoveryRunRepository tracks discovery jobs.
type DiscoveryRunRepository interface {
	Create(ctx context.Context, r DiscoveryRun) error
	Get(ctx context.Context, tenant core.TenantID, id core.ID) (DiscoveryRun, error)
	Update(ctx context.Context, r DiscoveryRun) error
	ListRecent(ctx context.Context, tenant core.TenantID, limit int) ([]DiscoveryRun, error)
	Latest(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (DiscoveryRun, error)
}

// DiscoveryRun records one scan of an estate.
type DiscoveryRun struct {
	ID        core.ID        `json:"id"`
	TenantID  core.TenantID  `json:"tenant_id"`
	AccountID core.AccountID `json:"account_id"`
	Regions   []core.Region  `json:"regions"`
	Trigger   string         `json:"trigger"` // "onboarding" | "scheduled" | "manual" | "post_execution"

	State      string     `json:"state"` // running | completed | partial | failed
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	ResourcesDiscovered int `json:"resources_discovered"`
	ResourcesUpdated    int `json:"resources_updated"`
	ResourcesRemoved    int `json:"resources_removed"`
	RelationshipsFound  int `json:"relationships_found"`
	MetricsCollected    int `json:"metrics_collected"`

	// ServiceResults records per-service outcomes. Partial discovery is the
	// normal case in large estates — one service throttling must not discard
	// the other twenty-four — so the run reports exactly what succeeded.
	ServiceResults []ServiceScanResult `json:"service_results"`
	Errors         []string            `json:"errors,omitempty"`
	Coverage       float64             `json:"coverage"`
	DurationMS     int64               `json:"duration_ms"`
}

// ServiceScanResult is one service's discovery outcome.
type ServiceScanResult struct {
	Service      string `json:"service"`
	Region       string `json:"region"`
	Succeeded    bool   `json:"succeeded"`
	Count        int    `json:"count"`
	DurationMS   int64  `json:"duration_ms"`
	APICallCount int    `json:"api_call_count"`
	Throttled    int    `json:"throttled"`
	Error        string `json:"error,omitempty"`
	// MissingPermission names the IAM action that was denied, which turns an
	// opaque failure into an actionable "add this to your role" message.
	MissingPermission string `json:"missing_permission,omitempty"`
}

// UnitOfWork runs a function inside a transaction, giving the callback a
// repository set bound to that transaction. Services that must write several
// aggregates atomically — approving a spec and creating a tenant, executing a
// plan and advancing its savings record — take one of these.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, repos Repositories) error) error
}

// Repositories bundles the full repository set, which is what the application
// layer is constructed with.
type Repositories struct {
	Tenants         TenantRepository
	Users           UserRepository
	Specs           SpecRepository
	AWSAccounts     AWSAccountRepository
	Resources       ResourceRepository
	Applications    ApplicationRepository
	Costs           CostRepository
	Metrics         MetricRepository
	Recommendations RecommendationRepository
	Policies        PolicyRepository
	Approvals       ApprovalRepository
	Executions      ExecutionRepository
	Savings         SavingsRepository
	Economics       EconomicsRepository
	Simulations     SimulationRepository
	Audit           AuditRepository
	DiscoveryRuns   DiscoveryRunRepository
	Conversations   ConversationRepository
	Notifications   NotificationRepository
}
