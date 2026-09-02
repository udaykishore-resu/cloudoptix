// Package spec is the formal CloudOptix specification: the immutable,
// versioned artefact that a conversation produces and that everything
// downstream is configured from.
//
// This is the pivot of the whole product. The onboarding chat is pleasant and
// forgiving; the specification is exact and reviewable. A conversation cannot
// change infrastructure — it can only produce a draft specification, which a
// human reads, edits and approves. Once approved, the specification version is
// frozen and every engine reads from it. Changing anything means a new
// version, with a diff.
//
// Traceability: REQ-SPEC-001..015, SPEC-ONB-001.
package spec

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Status is the specification lifecycle.
type Status string

const (
	// StatusDraft is being assembled by the onboarding agent. Mutable.
	StatusDraft Status = "draft"
	// StatusValidating is undergoing deterministic validation.
	StatusValidating Status = "validating"
	// StatusPendingReview is complete and awaiting a human.
	StatusPendingReview Status = "pending_review"
	// StatusApproved is frozen and in force. Immutable.
	StatusApproved Status = "approved"
	// StatusSuperseded was replaced by a later version. Immutable.
	StatusSuperseded Status = "superseded"
	// StatusRejected was declined by the reviewer. Immutable.
	StatusRejected Status = "rejected"
)

// Mutable reports whether the specification can still be edited in place.
func (s Status) Mutable() bool { return s == StatusDraft }

// Field wraps a specification value with its provenance and the question that
// produced it. Wrapping rather than storing bare values is what lets the
// review screen show "we inferred this" beside every inferred value, and what
// lets the agent know what it still needs to ask about.
type Field[T any] struct {
	Value      T               `json:"value" yaml:"value"`
	Provenance core.Provenance `json:"provenance" yaml:"provenance"`
	// Source is where the value came from: "user", "aws_discovery",
	// "inference:tag_convention", "default".
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	// Rationale explains an inference in one sentence, shown on hover.
	Rationale string    `json:"rationale,omitempty" yaml:"rationale,omitempty"`
	AskedAt   time.Time `json:"asked_at,omitempty" yaml:"-"`
	SetAt     time.Time `json:"set_at,omitempty" yaml:"-"`
}

// Confirmed builds a field the user stated directly.
func Confirmed[T any](v T, source string) Field[T] {
	return Field[T]{Value: v, Provenance: core.ProvenanceConfirmed, Source: source, SetAt: time.Now().UTC()}
}

// Inferred builds a field CloudOptix derived, with its reasoning.
func Inferred[T any](v T, source, rationale string) Field[T] {
	return Field[T]{Value: v, Provenance: core.ProvenanceInferred, Source: source, Rationale: rationale, SetAt: time.Now().UTC()}
}

// Unknown builds a field the user could not answer.
func Unknown[T any]() Field[T] {
	return Field[T]{Provenance: core.ProvenanceUnknown, SetAt: time.Now().UTC()}
}

// NeedsConfirmation builds a field with a proposed value awaiting sign-off.
func NeedsConfirmation[T any](v T, source, rationale string) Field[T] {
	return Field[T]{Value: v, Provenance: core.ProvenanceRequiresConfirmation, Source: source, Rationale: rationale, SetAt: time.Now().UTC()}
}

// Known reports whether the field carries a usable value.
func (f Field[T]) Known() bool {
	return f.Provenance == core.ProvenanceConfirmed || f.Provenance == core.ProvenanceInferred ||
		f.Provenance == core.ProvenanceRequiresConfirmation
}

// Spec is a complete CloudOptix specification.
//
// The YAML tags matter: this struct is the Go form of cloudoptix.yaml, and the
// file a customer commits to their repository round-trips through it exactly.
type Spec struct {
	APIVersion string `json:"api_version" yaml:"apiVersion"`
	Kind       string `json:"kind" yaml:"kind"`

	Organization  Organization  `json:"organization" yaml:"organization"`
	Application   Application   `json:"application" yaml:"application"`
	AWS           AWS           `json:"aws" yaml:"aws"`
	Workloads     []Workload    `json:"workloads,omitempty" yaml:"workloads,omitempty"`
	Business      Business      `json:"business" yaml:"business"`
	Objectives    Objectives    `json:"objectives" yaml:"objectives"`
	Optimization  Optimization  `json:"optimization" yaml:"optimization"`
	Automation    Automation    `json:"automation" yaml:"automation"`
	Governance    Governance    `json:"governance" yaml:"governance"`
	Security      Security      `json:"security" yaml:"security"`
	Observability Observability `json:"observability" yaml:"observability"`
	Notifications Notifications `json:"notifications" yaml:"notifications"`
	Teams         []Team        `json:"teams,omitempty" yaml:"teams,omitempty"`

	// Provenance is the per-path provenance map, kept alongside the values so
	// the YAML a customer edits stays clean while the review UI stays
	// informative.
	Provenance map[string]core.Provenance `json:"provenance,omitempty" yaml:"-"`
	// OpenQuestions is what the agent still needs. It is part of the spec so
	// an interrupted onboarding can resume days later on another device.
	OpenQuestions []OpenQuestion `json:"open_questions,omitempty" yaml:"-"`
}

// Organization is the tenant's company context.
type Organization struct {
	Name           string   `json:"name" yaml:"name"`
	Industry       string   `json:"industry,omitempty" yaml:"industry,omitempty"`
	Size           string   `json:"size,omitempty" yaml:"size,omitempty"`
	Regions        []string `json:"business_regions,omitempty" yaml:"businessRegions,omitempty"`
	PrimaryContact string   `json:"primary_contact,omitempty" yaml:"primaryContact,omitempty"`
}

// Application is the software being optimized.
type Application struct {
	Name         string              `json:"name" yaml:"name"`
	Description  string              `json:"description,omitempty" yaml:"description,omitempty"`
	Domain       string              `json:"domain,omitempty" yaml:"domain,omitempty"`
	Criticality  string              `json:"criticality,omitempty" yaml:"criticality,omitempty"`
	Architecture ArchitectureProfile `json:"architecture" yaml:"architecture"`
}

// ArchitectureProfile is the shape of the system, gathered conversationally
// and then reconciled against what discovery actually finds. A mismatch
// between what the user described and what exists is itself a finding.
type ArchitectureProfile struct {
	Style            string   `json:"style,omitempty" yaml:"style,omitempty"` // microservices, monolith, serverless
	ComputePlatforms []string `json:"compute_platforms,omitempty" yaml:"computePlatforms,omitempty"`
	Databases        []string `json:"databases,omitempty" yaml:"databases,omitempty"`
	Caches           []string `json:"caches,omitempty" yaml:"caches,omitempty"`
	Messaging        []string `json:"messaging,omitempty" yaml:"messaging,omitempty"`
	Storage          []string `json:"storage,omitempty" yaml:"storage,omitempty"`
	Networking       []string `json:"networking,omitempty" yaml:"networking,omitempty"`
	Observability    []string `json:"observability,omitempty" yaml:"observability,omitempty"`
	DeploymentModel  string   `json:"deployment_model,omitempty" yaml:"deploymentModel,omitempty"`
	IaC              []string `json:"iac,omitempty" yaml:"iac,omitempty"`
}

// AWS is the account and region topology.
type AWS struct {
	Accounts       []Account  `json:"accounts" yaml:"accounts"`
	AccessMode     string     `json:"access_mode" yaml:"accessMode"`
	PayerAccountID string     `json:"payer_account_id,omitempty" yaml:"payerAccountId,omitempty"`
	OrganizationID string     `json:"organization_id,omitempty" yaml:"organizationId,omitempty"`
	CUR            *CURConfig `json:"cur,omitempty" yaml:"cur,omitempty"`
}

// Account is one AWS account in scope.
type Account struct {
	ID          string   `json:"id" yaml:"id"`
	Alias       string   `json:"alias,omitempty" yaml:"alias,omitempty"`
	Environment string   `json:"environment" yaml:"environment"`
	Regions     []string `json:"regions" yaml:"regions"`
	RoleARN     string   `json:"role_arn,omitempty" yaml:"roleArn,omitempty"`
	ExternalID  string   `json:"external_id,omitempty" yaml:"externalId,omitempty"`
	Production  bool     `json:"production" yaml:"production"`
}

// CURConfig locates the Cost & Usage Report.
type CURConfig struct {
	Bucket     string `json:"bucket" yaml:"bucket"`
	Prefix     string `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	Region     string `json:"region,omitempty" yaml:"region,omitempty"`
	ReportName string `json:"report_name,omitempty" yaml:"reportName,omitempty"`
}

// Workload is a declared workload, which seeds attribution before discovery
// has anything to work with.
type Workload struct {
	Name        string            `json:"name" yaml:"name"`
	Type        string            `json:"type,omitempty" yaml:"type,omitempty"`
	Platform    string            `json:"platform,omitempty" yaml:"platform,omitempty"`
	Environment string            `json:"environment,omitempty" yaml:"environment,omitempty"`
	Criticality string            `json:"criticality,omitempty" yaml:"criticality,omitempty"`
	Owner       string            `json:"owner,omitempty" yaml:"owner,omitempty"`
	Team        string            `json:"team,omitempty" yaml:"team,omitempty"`
	Cluster     string            `json:"cluster,omitempty" yaml:"cluster,omitempty"`
	Namespace   string            `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	DependsOn   []string          `json:"depends_on,omitempty" yaml:"dependsOn,omitempty"`
	TagSelector map[string]string `json:"tag_selector,omitempty" yaml:"tagSelector,omitempty"`
	SLO         WorkloadSLO       `json:"slo,omitempty" yaml:"slo,omitempty"`
}

// WorkloadSLO is the per-workload reliability contract.
type WorkloadSLO struct {
	Availability float64 `json:"availability,omitempty" yaml:"availability,omitempty"`
	LatencyP95MS float64 `json:"latency_p95_ms,omitempty" yaml:"latencyP95Ms,omitempty"`
	LatencyP99MS float64 `json:"latency_p99_ms,omitempty" yaml:"latencyP99Ms,omitempty"`
	ErrorRateMax float64 `json:"error_rate_max,omitempty" yaml:"errorRateMax,omitempty"`
}

// Business is the commercial context: the denominators that make cost mean
// something.
type Business struct {
	Transactions   []TransactionSpec `json:"transactions,omitempty" yaml:"transactions,omitempty"`
	KPIs           []KPI             `json:"kpis,omitempty" yaml:"kpis,omitempty"`
	CustomerCount  int64             `json:"customer_count,omitempty" yaml:"customerCount,omitempty"`
	PeakSeasons    []string          `json:"peak_seasons,omitempty" yaml:"peakSeasons,omitempty"`
	TrafficProfile string            `json:"traffic_profile,omitempty" yaml:"trafficProfile,omitempty"`
}

// TransactionSpec declares a business transaction and its volume.
type TransactionSpec struct {
	Name              string   `json:"name" yaml:"name"`
	Description       string   `json:"description,omitempty" yaml:"description,omitempty"`
	MonthlyVolume     float64  `json:"monthly_volume,omitempty" yaml:"monthlyVolume,omitempty"`
	Workloads         []string `json:"workloads,omitempty" yaml:"workloads,omitempty"`
	TargetCostPerUnit float64  `json:"target_cost_per_unit,omitempty" yaml:"targetCostPerUnit,omitempty"`
	VolumeMetric      string   `json:"volume_metric,omitempty" yaml:"volumeMetric,omitempty"`
	Critical          bool     `json:"critical" yaml:"critical"`
}

// KPI is a business metric the tenant tracks alongside cost.
type KPI struct {
	Name   string  `json:"name" yaml:"name"`
	Unit   string  `json:"unit,omitempty" yaml:"unit,omitempty"`
	Target float64 `json:"target,omitempty" yaml:"target,omitempty"`
}

// Objectives are the measurable goals CloudOptix is being hired to hit.
type Objectives struct {
	CostReductionTarget float64       `json:"cost_reduction_target,omitempty" yaml:"costReductionTarget,omitempty"`
	MonthlyBudget       float64       `json:"monthly_budget,omitempty" yaml:"monthlyBudget,omitempty"`
	AvailabilityTarget  float64       `json:"availability_target,omitempty" yaml:"availabilityTarget,omitempty"`
	MaxLatencyMS        float64       `json:"max_latency_ms,omitempty" yaml:"maxLatencyMs,omitempty"`
	CostSLOs            []CostSLOSpec `json:"cost_slos,omitempty" yaml:"costSlos,omitempty"`
	Timeframe           string        `json:"timeframe,omitempty" yaml:"timeframe,omitempty"`
}

// CostSLOSpec declares a cost objective in the specification.
type CostSLOSpec struct {
	Name           string   `json:"name" yaml:"name"`
	Kind           string   `json:"kind" yaml:"kind"`
	Scope          string   `json:"scope,omitempty" yaml:"scope,omitempty"`
	Target         float64  `json:"target" yaml:"target"`
	Transaction    string   `json:"transaction,omitempty" yaml:"transaction,omitempty"`
	Window         string   `json:"window,omitempty" yaml:"window,omitempty"`
	ErrorBudgetPct float64  `json:"error_budget_pct,omitempty" yaml:"errorBudgetPct,omitempty"`
	BreachActions  []string `json:"breach_actions,omitempty" yaml:"breachActions,omitempty"`
}

// Optimization captures the tenant's appetite and constraints.
type Optimization struct {
	RiskTolerance      string            `json:"risk_tolerance" yaml:"riskTolerance"` // low|medium|high
	Preferences        []string          `json:"preferences,omitempty" yaml:"preferences,omitempty"`
	ExcludedActions    []string          `json:"excluded_actions,omitempty" yaml:"excludedActions,omitempty"`
	ExcludedResources  []string          `json:"excluded_resources,omitempty" yaml:"excludedResources,omitempty"`
	ExcludedTags       map[string]string `json:"excluded_tags,omitempty" yaml:"excludedTags,omitempty"`
	SpotAllowed        bool              `json:"spot_allowed" yaml:"spotAllowed"`
	CommitmentAppetite string            `json:"commitment_appetite,omitempty" yaml:"commitmentAppetite,omitempty"`
	MinSavingThreshold float64           `json:"min_saving_threshold,omitempty" yaml:"minSavingThreshold,omitempty"`
}

// Automation is the master control over autonomous change.
type Automation struct {
	Enabled                 bool                `json:"enabled" yaml:"enabled"`
	Environments            []string            `json:"environments,omitempty" yaml:"environments,omitempty"`
	AutoExecuteActions      []string            `json:"auto_execute_actions,omitempty" yaml:"autoExecuteActions,omitempty"`
	MaintenanceWindows      []MaintenanceWindow `json:"maintenance_windows,omitempty" yaml:"maintenanceWindows,omitempty"`
	MaxConcurrentChanges    int                 `json:"max_concurrent_changes,omitempty" yaml:"maxConcurrentChanges,omitempty"`
	MaxMonthlyImpact        float64             `json:"max_monthly_impact,omitempty" yaml:"maxMonthlyImpact,omitempty"`
	ValidationWindowMinutes int                 `json:"validation_window_minutes,omitempty" yaml:"validationWindowMinutes,omitempty"`
	AutoRollback            bool                `json:"auto_rollback" yaml:"autoRollback"`
}

// MaintenanceWindow is a permitted change window in cron-like terms.
type MaintenanceWindow struct {
	Name            string   `json:"name" yaml:"name"`
	Days            []string `json:"days" yaml:"days"`
	StartUTC        string   `json:"start_utc" yaml:"startUtc"`
	DurationMinutes int      `json:"duration_minutes" yaml:"durationMinutes"`
	Environments    []string `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// Governance is the approval and change-management posture.
type Governance struct {
	ProductionChangesRequireApproval bool     `json:"production_changes_require_approval" yaml:"productionChangesRequireApproval"`
	MinApprovals                     int      `json:"min_approvals,omitempty" yaml:"minApprovals,omitempty"`
	ApproverRoles                    []string `json:"approver_roles,omitempty" yaml:"approverRoles,omitempty"`
	SegregationOfDuties              bool     `json:"segregation_of_duties" yaml:"segregationOfDuties"`
	ChangeManagementSystem           string   `json:"change_management_system,omitempty" yaml:"changeManagementSystem,omitempty"`
	ChangeFreezeWindows              []string `json:"change_freeze_windows,omitempty" yaml:"changeFreezeWindows,omitempty"`
	PolicyPack                       string   `json:"policy_pack,omitempty" yaml:"policyPack,omitempty"`
}

// Security is the security and compliance posture.
type Security struct {
	AWSAccessMode        string   `json:"aws_access_mode" yaml:"awsAccessMode"`
	ComplianceFrameworks []string `json:"compliance_frameworks,omitempty" yaml:"complianceFrameworks,omitempty"`
	DataResidency        []string `json:"data_residency,omitempty" yaml:"dataResidency,omitempty"`
	EncryptionRequired   bool     `json:"encryption_required" yaml:"encryptionRequired"`
	SSOProvider          string   `json:"sso_provider,omitempty" yaml:"ssoProvider,omitempty"`
	AuditRetentionDays   int      `json:"audit_retention_days,omitempty" yaml:"auditRetentionDays,omitempty"`
	PIIHandling          string   `json:"pii_handling,omitempty" yaml:"piiHandling,omitempty"`
}

// Observability declares the telemetry CloudOptix can read.
type Observability struct {
	MetricsSources     []string `json:"metrics_sources,omitempty" yaml:"metricsSources,omitempty"`
	PrometheusEndpoint string   `json:"prometheus_endpoint,omitempty" yaml:"prometheusEndpoint,omitempty"`
	OTelEndpoint       string   `json:"otel_endpoint,omitempty" yaml:"otelEndpoint,omitempty"`
	TracingEnabled     bool     `json:"tracing_enabled" yaml:"tracingEnabled"`
	LogRetentionDays   int      `json:"log_retention_days,omitempty" yaml:"logRetentionDays,omitempty"`
}

// Notifications is the routing configuration.
type Notifications struct {
	Channels []NotificationChannel `json:"channels,omitempty" yaml:"channels,omitempty"`
	// Subscriptions maps event types to channel names.
	Subscriptions map[string][]string `json:"subscriptions,omitempty" yaml:"subscriptions,omitempty"`
	QuietHoursUTC string              `json:"quiet_hours_utc,omitempty" yaml:"quietHoursUtc,omitempty"`
}

// NotificationChannel is one delivery target.
type NotificationChannel struct {
	Name   string `json:"name" yaml:"name"`
	Type   string `json:"type" yaml:"type"` // email|slack|webhook
	Target string `json:"target" yaml:"target"`
	// SecretRef points at a secret manager entry. Webhook URLs and Slack
	// tokens are never stored in the specification itself, which is designed
	// to be committed to a customer's git repository.
	SecretRef  string   `json:"secret_ref,omitempty" yaml:"secretRef,omitempty"`
	Severities []string `json:"severities,omitempty" yaml:"severities,omitempty"`
}

// Team is an ownership grouping.
type Team struct {
	Name    string   `json:"name" yaml:"name"`
	Contact string   `json:"contact,omitempty" yaml:"contact,omitempty"`
	Owns    []string `json:"owns,omitempty" yaml:"owns,omitempty"`
	Members []Member `json:"members,omitempty" yaml:"members,omitempty"`
}

// Member is a user with roles.
type Member struct {
	Email string   `json:"email" yaml:"email"`
	Name  string   `json:"name,omitempty" yaml:"name,omitempty"`
	Roles []string `json:"roles" yaml:"roles"`
}

// OpenQuestion is something the agent still needs to resolve.
type OpenQuestion struct {
	Path     string `json:"path"`
	Question string `json:"question"`
	Why      string `json:"why"`
	Required bool   `json:"required"`
	// Blocking marks questions that must be answered before onboarding can
	// complete, as distinct from ones that merely reduce quality.
	Blocking   bool     `json:"blocking"`
	Options    []string `json:"options,omitempty"`
	AskedCount int      `json:"asked_count"`
}

// Version is an immutable snapshot of a specification.
type Version struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	SpecID   core.ID       `json:"spec_id"`
	Version  int           `json:"version"`
	Status   Status        `json:"status"`

	Spec Spec `json:"spec"`

	Checksum string  `json:"checksum"`
	ParentID core.ID `json:"parent_id,omitempty"`
	// Diff against the parent version, computed at creation and stored so the
	// history is readable without recomputation.
	Diff []Change `json:"diff,omitempty"`

	Validation   core.ValidationResult `json:"validation"`
	Completeness Completeness          `json:"completeness"`

	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	ApprovedBy     string     `json:"approved_by,omitempty"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	ApprovalID     core.ID    `json:"approval_id,omitempty"`
	RejectedReason string     `json:"rejected_reason,omitempty"`
	// ConversationID links the version back to the chat that produced it, so
	// a reviewer can read exactly what was said.
	ConversationID core.ID `json:"conversation_id,omitempty"`
}

// Completeness summarises how much of the specification is known, which is
// what the onboarding progress indicator renders.
type Completeness struct {
	TotalFields       int      `json:"total_fields"`
	Confirmed         int      `json:"confirmed"`
	Inferred          int      `json:"inferred"`
	Unknown           int      `json:"unknown"`
	NeedsConfirmation int      `json:"needs_confirmation"`
	Score             float64  `json:"score"`
	ReadyForReview    bool     `json:"ready_for_review"`
	BlockingGaps      []string `json:"blocking_gaps,omitempty"`
}

// ChangeKind classifies a diff entry.
type ChangeKind string

const (
	ChangeAdded    ChangeKind = "added"
	ChangeRemoved  ChangeKind = "removed"
	ChangeModified ChangeKind = "modified"
)

// Change is one difference between two specification versions.
type Change struct {
	Path   string     `json:"path"`
	Kind   ChangeKind `json:"kind"`
	Before string     `json:"before,omitempty"`
	After  string     `json:"after,omitempty"`
	// Impact explains what changing this affects, so a reviewer approving
	// version 4 knows that raising the availability target will suppress a
	// class of recommendations.
	Impact   string        `json:"impact,omitempty"`
	Severity core.Severity `json:"severity"`
}

// Summarize renders a change for the diff view.
func (c Change) Summarize() string {
	switch c.Kind {
	case ChangeAdded:
		return fmt.Sprintf("+ %s = %s", c.Path, c.After)
	case ChangeRemoved:
		return fmt.Sprintf("- %s (was %s)", c.Path, c.Before)
	default:
		return fmt.Sprintf("~ %s: %s -> %s", c.Path, c.Before, c.After)
	}
}

// Approve freezes the version. It is the only transition that makes a
// specification authoritative, and it records who took responsibility.
func (v *Version) Approve(by string, approvalID core.ID, at time.Time) error {
	if v.Status != StatusPendingReview {
		return core.Conflict("specification v%d is %s and cannot be approved", v.Version, v.Status)
	}
	if v.Validation.HasBlocking() {
		return core.Invalid("specification v%d has blocking validation issues", v.Version).
			WithDetail("issues", v.Validation.Issues)
	}
	v.Status = StatusApproved
	v.ApprovedBy = by
	v.ApprovalID = approvalID
	t := at.UTC()
	v.ApprovedAt = &t
	return nil
}

// SortChanges orders a diff by severity then path, so a reviewer sees the
// consequential edits first.
func SortChanges(changes []Change) []Change {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Severity.Order() != changes[j].Severity.Order() {
			return changes[i].Severity.Order() > changes[j].Severity.Order()
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

// PathParts splits a dotted specification path.
func PathParts(path string) []string { return strings.Split(path, ".") }
