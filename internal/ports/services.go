package ports

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// AWSCredentialBroker mints short-lived credentials for a customer account.
//
// This is the only way anything in CloudOptix obtains AWS access. There is no
// method that accepts an access key, and the interface deliberately returns a
// scoped provider rather than raw credentials, so a caller cannot widen its
// own permissions after the fact.
//
// Traceability: REQ-SEC-001, SPEC-SEC-001.
type AWSCredentialBroker interface {
	// Assume returns a credentials provider for one scope of one account. The
	// implementation caches sessions until shortly before expiry and records
	// every assumption in the audit log.
	Assume(ctx context.Context, account cloud.AWSAccount, scope cloud.RoleScope) (AWSSession, error)
	// Verify probes the account's roles and reports which scopes actually work
	// and which IAM actions are missing, which is what turns a failed
	// connection into an actionable list for the customer.
	Verify(ctx context.Context, account cloud.AWSAccount) (ConnectionCheck, error)
}

// AWSSession is an assumed-role session bound to one account, region set and
// scope.
type AWSSession interface {
	AccountID() core.AccountID
	Scope() cloud.RoleScope
	ExpiresAt() time.Time
	// Config returns the opaque SDK configuration for a region. The concrete
	// type is aws.Config; it is returned as any so that the ports package
	// carries no AWS SDK dependency and the domain stays provider-neutral.
	Config(region core.Region) any
}

// ConnectionCheck is the result of verifying an account's roles.
type ConnectionCheck struct {
	AccountID             core.AccountID      `json:"account_id"`
	Reachable             bool                `json:"reachable"`
	GrantedScopes         []cloud.RoleScope   `json:"granted_scopes"`
	MissingActions        map[string][]string `json:"missing_actions,omitempty"` // scope -> actions
	Regions               []core.Region       `json:"regions"`
	IdentityARN           string              `json:"identity_arn,omitempty"`
	IsPayer               bool                `json:"is_payer"`
	CURAvailable          bool                `json:"cur_available"`
	CostExplorerAvailable bool                `json:"cost_explorer_available"`
	Errors                []string            `json:"errors,omitempty"`
	CheckedAt             time.Time           `json:"checked_at"`
}

// ResourceDiscoverer scans one AWS service in one region and returns
// normalized resources plus the relationships it can see.
//
// One implementation per service keeps each one small enough to be obviously
// correct, and lets the orchestrator run them concurrently with per-service
// error isolation.
type ResourceDiscoverer interface {
	// Service returns the AWS service code this discoverer handles.
	Service() string
	// Kinds lists the resource kinds it can produce, used by MarkAbsent so a
	// tombstone pass never removes a kind the scan did not cover.
	Kinds() []cloud.Kind
	// RequiredActions lists the IAM actions needed, which drives both the
	// generated onboarding policy and the permission probe.
	RequiredActions() []string
	// Discover scans one region.
	Discover(ctx context.Context, in DiscoveryInput) (DiscoveryOutput, error)
}

// DiscoveryInput is what a discoverer needs.
type DiscoveryInput struct {
	TenantID  core.TenantID
	Session   AWSSession
	AccountID core.AccountID
	Region    core.Region
	// Existing lets a discoverer resolve cross-service references without
	// re-querying, e.g. attaching volumes to instances already found.
	Existing *cloud.Inventory
}

// DiscoveryOutput is what a discoverer produces.
type DiscoveryOutput struct {
	Resources     []cloud.Resource
	Relationships []cloud.Relationship
	APICalls      int
	Throttled     int
	Warnings      []string
}

// CostIngestor pulls billed cost from AWS.
type CostIngestor interface {
	Source() string // "cost_explorer" | "cur" | "simulator"
	// Fetch returns normalized cost records for a period.
	Fetch(ctx context.Context, in CostIngestInput) ([]cost.Record, error)
	// Available reports whether this source is usable for the account, so the
	// orchestrator can prefer CUR (resource-level, hourly) and fall back to
	// Cost Explorer (daily, coarser) without failing.
	Available(ctx context.Context, session AWSSession, account cloud.AWSAccount) bool
}

// CostIngestInput is a cost fetch request.
type CostIngestInput struct {
	TenantID    core.TenantID
	Session     AWSSession
	Account     cloud.AWSAccount
	Period      core.Period
	Granularity cost.Granularity
	Basis       cost.AmortizationBasis
	// ResourceLevel requests per-resource attribution, which only the CUR
	// source can provide.
	ResourceLevel bool
}

// MetricCollector gathers utilisation telemetry.
type MetricCollector interface {
	Source() string // "cloudwatch" | "prometheus" | "otel" | "simulator"
	Collect(ctx context.Context, in MetricCollectInput) ([]ResourceMetrics, error)
	Available(ctx context.Context, session AWSSession) bool
}

// MetricCollectInput is a telemetry request.
type MetricCollectInput struct {
	TenantID  core.TenantID
	Session   AWSSession
	Region    core.Region
	Resources []cloud.Resource
	Window    core.Period
	// StepSeconds is the requested resolution. CloudWatch charges per metric
	// query, so the collector coarsens the step for long windows rather than
	// issuing a request per minute per resource.
	StepSeconds int
}

// Executor applies one action type to AWS. One implementation per action keeps
// the IAM surface, the precondition checks and the rollback construction
// together in one small, reviewable unit.
type Executor interface {
	Action() optimize.ActionType
	RequiredActions() []string
	// Plan builds the forward steps, the snapshot steps and the rollback plan
	// for a recommendation. It performs no mutation.
	Plan(ctx context.Context, in ExecutionPlanInput) (execute.Plan, error)
	// Preflight re-checks that the world still matches what the plan assumed.
	// It runs immediately before execution, however long ago the plan was
	// approved.
	Preflight(ctx context.Context, session AWSSession, plan execute.Plan) error
	// Apply performs one mutating step. It must be idempotent on the step's
	// IdempotencyKey.
	Apply(ctx context.Context, session AWSSession, plan execute.Plan, step execute.Step) (map[string]any, error)
	// Rollback performs one reverse step.
	Rollback(ctx context.Context, session AWSSession, plan execute.Plan, step execute.Step) error
}

// ExecutionPlanInput is what an executor needs to build a plan.
type ExecutionPlanInput struct {
	TenantID       core.TenantID
	Recommendation optimize.Recommendation
	Resource       cloud.Resource
	Account        cloud.AWSAccount
	Session        AWSSession
	DryRun         bool
	RequestedBy    string
}

// PricingCatalog answers "what does this configuration cost" without calling
// AWS. It is the engine behind the cost compiler, the mutation engine and
// every counterfactual.
type PricingCatalog interface {
	// InstancePrice returns the hourly on-demand price for an instance type.
	InstancePrice(region core.Region, instanceType string, platform string) (core.Money, bool)
	// SpotPrice returns a recent average spot price, and whether one is known.
	SpotPrice(region core.Region, instanceType string) (core.Money, bool)
	// StoragePrice returns the per-GiB-month price for a storage class.
	StoragePrice(region core.Region, storageClass string) (core.Money, bool)
	// IOPSPrice returns the per-provisioned-IOPS-month price.
	IOPSPrice(region core.Region, volumeType string) (core.Money, bool)
	// ThroughputPrice returns the per-MiBps-month price for gp3-style volumes.
	ThroughputPrice(region core.Region, volumeType string) (core.Money, bool)
	// DatabasePrice returns the hourly price for a managed database class.
	DatabasePrice(region core.Region, instanceClass, engine string, multiAZ bool) (core.Money, bool)
	// CachePrice returns the hourly price for an ElastiCache node type.
	CachePrice(region core.Region, nodeType, engine string) (core.Money, bool)
	// ServicePrice returns a unit price for a metered service dimension, e.g.
	// ("nat_gateway", "hours") or ("lambda", "gb_second").
	ServicePrice(region core.Region, service, dimension string) (core.Money, bool)
	// DataTransferPrice returns the per-GB price for a transfer direction:
	// "internet_out", "cross_az", "cross_region", "nat_processed".
	DataTransferPrice(region core.Region, direction string) (core.Money, bool)
	// InstanceSpec returns the capacity of an instance type, which is how
	// rightsizing compares candidates.
	InstanceSpec(instanceType string) (InstanceSpec, bool)
	// InstanceFamily returns every type in the same family, ordered by size,
	// which is the candidate set for a rightsizing decision.
	InstanceFamily(instanceType string) []string
	// CommitmentPrice returns the effective hourly rate under a commitment.
	CommitmentPrice(region core.Region, instanceType, term, payment string) (core.Money, bool)
	// PricingDate reports how fresh the catalog is; every simulated figure is
	// stamped with it.
	PricingDate() time.Time
}

// InstanceSpec is the capacity and generation of an instance type.
type InstanceSpec struct {
	Type         string  `json:"type"`
	Family       string  `json:"family"`
	Size         string  `json:"size"`
	Generation   int     `json:"generation"`
	VCPU         float64 `json:"vcpu"`
	MemoryGiB    float64 `json:"memory_gib"`
	NetworkGbps  float64 `json:"network_gbps"`
	EBSOptimized bool    `json:"ebs_optimized"`
	Architecture string  `json:"architecture"` // x86_64 | arm64
	Burstable    bool    `json:"burstable"`
	// SuccessorType is the current-generation equivalent, which is what the
	// old-generation rule recommends moving to.
	SuccessorType string `json:"successor_type,omitempty"`
}

// EventPublisher emits domain events. Every consequential state change
// publishes one; workers and notifications subscribe rather than polling.
type EventPublisher interface {
	Publish(ctx context.Context, e Event) error
	PublishBatch(ctx context.Context, events []Event) error
}

// EventSubscriber consumes domain events.
type EventSubscriber interface {
	Subscribe(ctx context.Context, types []EventType, handler EventHandler) error
}

// EventHandler processes one event. Returning an error causes redelivery up to
// the transport's retry limit, then routes to the dead-letter queue.
type EventHandler func(ctx context.Context, e Event) error

// EventType names a domain event.
type EventType string

const (
	EventTenantCreated          EventType = "cloudoptix.tenant.created"
	EventSpecDrafted            EventType = "cloudoptix.spec.drafted"
	EventSpecApproved           EventType = "cloudoptix.spec.approved"
	EventAWSAccountConnected    EventType = "cloudoptix.aws_account.connected"
	EventAWSAccountFailed       EventType = "cloudoptix.aws_account.failed"
	EventDiscoveryStarted       EventType = "cloudoptix.discovery.started"
	EventDiscoveryCompleted     EventType = "cloudoptix.discovery.completed"
	EventTwinUpdated            EventType = "cloudoptix.twin.updated"
	EventCostUpdated            EventType = "cloudoptix.cost.updated"
	EventCostAnomalyDetected    EventType = "cloudoptix.cost.anomaly_detected"
	EventEconomicsComputed      EventType = "cloudoptix.economics.computed"
	EventRecommendationCreated  EventType = "cloudoptix.recommendation.created"
	EventApprovalRequested      EventType = "cloudoptix.approval.requested"
	EventApprovalGranted        EventType = "cloudoptix.approval.granted"
	EventApprovalRejected       EventType = "cloudoptix.approval.rejected"
	EventExecutionScheduled     EventType = "cloudoptix.execution.scheduled"
	EventOptimizationExecuted   EventType = "cloudoptix.optimization.executed"
	EventOptimizationValidated  EventType = "cloudoptix.optimization.validated"
	EventOptimizationRolledBack EventType = "cloudoptix.optimization.rolled_back"
	EventCostSLOBreached        EventType = "cloudoptix.cost_slo.breached"
	EventBudgetExhausted        EventType = "cloudoptix.economic_error_budget.exhausted"
	EventCostRegressionDetected EventType = "cloudoptix.cost_regression.detected"
	EventSavingsRealized        EventType = "cloudoptix.savings.realized"
)

// Event is a domain event envelope. It carries the tenant so every consumer
// can scope itself, and a correlation id so a whole optimization story can be
// traced from detection through to realized savings.
type Event struct {
	ID            string         `json:"id"`
	Type          EventType      `json:"type"`
	TenantID      core.TenantID  `json:"tenant_id"`
	SubjectID     core.ID        `json:"subject_id,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	OccurredAt    time.Time      `json:"occurred_at"`
	Actor         string         `json:"actor,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
	// IdempotencyKey lets a consumer detect redelivery. At-least-once
	// delivery is assumed everywhere.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Notifier delivers a message to a channel.
type Notifier interface {
	Channel() string // "email" | "slack" | "webhook"
	Send(ctx context.Context, n Notification) error
}

// Notification is a rendered outbound message.
type Notification struct {
	ID        core.ID       `json:"id"`
	TenantID  core.TenantID `json:"tenant_id"`
	Channel   string        `json:"channel"`
	Target    string        `json:"target"`
	SecretRef string        `json:"secret_ref,omitempty"`
	Subject   string        `json:"subject"`
	Body      string        `json:"body"`
	// Blocks carries a structured payload for rich channels; plain-text
	// consumers fall back to Body.
	Blocks    map[string]any `json:"blocks,omitempty"`
	Severity  core.Severity  `json:"severity"`
	EventType EventType      `json:"event_type"`
	LinkURL   string         `json:"link_url,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	SentAt    *time.Time     `json:"sent_at,omitempty"`
	Attempts  int            `json:"attempts"`
	Error     string         `json:"error,omitempty"`
}

// NotificationRepository stores outbound notifications for retry and audit.
type NotificationRepository interface {
	Enqueue(ctx context.Context, n Notification) error
	ClaimPending(ctx context.Context, workerID string, limit int) ([]Notification, error)
	MarkSent(ctx context.Context, tenant core.TenantID, id core.ID, at time.Time) error
	MarkFailed(ctx context.Context, tenant core.TenantID, id core.ID, err string) error
	List(ctx context.Context, tenant core.TenantID, opts ListOptions) (Page[Notification], error)
}

// SecretResolver resolves a secret reference to its value. Secrets never
// appear in specifications, policies, events or the database — only
// references do.
type SecretResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// Cache is the tenant-scoped cache. Keys are namespaced by tenant inside the
// implementation, so a caller cannot construct a key that reads another
// tenant's entry.
type Cache interface {
	Get(ctx context.Context, tenant core.TenantID, key string, dest any) (bool, error)
	Set(ctx context.Context, tenant core.TenantID, key string, value any, ttl time.Duration) error
	Delete(ctx context.Context, tenant core.TenantID, key string) error
	// InvalidatePrefix clears a whole family of derived data after discovery
	// or a cost refresh.
	InvalidatePrefix(ctx context.Context, tenant core.TenantID, prefix string) error
}

// Locker provides distributed mutual exclusion so two workers never run the
// same discovery scan, execution or validation.
type Locker interface {
	// Acquire returns a release function, or ErrConflict when the lock is held.
	Acquire(ctx context.Context, key string, ttl time.Duration) (release func(), err error)
}
