// Package cloud defines the CloudOptix Resource Model: the provider-neutral
// normalization of everything discovered in a customer's AWS estate.
//
// Every AWS service returns a differently-shaped object. Rather than let that
// shape leak into the optimization rules, the twin, the economics engine and
// the UI, discovery adapters normalize into exactly one struct — Resource —
// with a typed Kind, a common capacity vocabulary and a service-specific
// attribute bag. A new AWS service therefore costs one adapter and zero
// changes anywhere downstream.
//
// Traceability: REQ-DSC-002, SPEC-DSC-001.
package cloud

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Kind enumerates every resource type CloudOptix models. It is a closed set:
// discovery of an unmodelled type produces KindUnknown plus a warning, never a
// silently mistyped resource.
type Kind string

const (
	KindEC2Instance      Kind = "aws.ec2.instance"
	KindEBSVolume        Kind = "aws.ebs.volume"
	KindEBSSnapshot      Kind = "aws.ebs.snapshot"
	KindElasticIP        Kind = "aws.ec2.elastic_ip"
	KindAMI              Kind = "aws.ec2.image"
	KindAutoScalingGroup Kind = "aws.autoscaling.group"
	KindRDSInstance      Kind = "aws.rds.instance"
	KindRDSCluster       Kind = "aws.rds.cluster"
	KindRDSSnapshot      Kind = "aws.rds.snapshot"
	KindDynamoDBTable    Kind = "aws.dynamodb.table"
	KindS3Bucket         Kind = "aws.s3.bucket"
	KindLambdaFunction   Kind = "aws.lambda.function"
	KindECSCluster       Kind = "aws.ecs.cluster"
	KindECSService       Kind = "aws.ecs.service"
	KindECSTaskDef       Kind = "aws.ecs.task_definition"
	KindEKSCluster       Kind = "aws.eks.cluster"
	KindEKSNodeGroup     Kind = "aws.eks.nodegroup"
	KindK8sWorkload      Kind = "k8s.workload"
	KindK8sNamespace     Kind = "k8s.namespace"
	KindALB              Kind = "aws.elbv2.application"
	KindNLB              Kind = "aws.elbv2.network"
	KindTargetGroup      Kind = "aws.elbv2.target_group"
	KindCloudFront       Kind = "aws.cloudfront.distribution"
	KindAPIGateway       Kind = "aws.apigateway.api"
	KindNATGateway       Kind = "aws.ec2.nat_gateway"
	KindVPC              Kind = "aws.ec2.vpc"
	KindSubnet           Kind = "aws.ec2.subnet"
	KindSecurityGroup    Kind = "aws.ec2.security_group"
	KindVPCEndpoint      Kind = "aws.ec2.vpc_endpoint"
	KindTransitGateway   Kind = "aws.ec2.transit_gateway"
	KindRoute53Zone      Kind = "aws.route53.hosted_zone"
	KindElastiCache      Kind = "aws.elasticache.cluster"
	KindMSKCluster       Kind = "aws.msk.cluster"
	KindSQSQueue         Kind = "aws.sqs.queue"
	KindSNSTopic         Kind = "aws.sns.topic"
	KindKinesisStream    Kind = "aws.kinesis.stream"
	KindEventBus         Kind = "aws.events.event_bus"
	KindLogGroup         Kind = "aws.logs.log_group"
	KindCloudTrail       Kind = "aws.cloudtrail.trail"
	KindConfigRecorder   Kind = "aws.config.recorder"
	KindKMSKey           Kind = "aws.kms.key"
	KindSecret           Kind = "aws.secretsmanager.secret"
	KindUnknown          Kind = "unknown"
)

// Category groups kinds for cost roll-ups and dashboard filters.
type Category string

const (
	CategoryCompute       Category = "compute"
	CategoryDatabase      Category = "database"
	CategoryStorage       Category = "storage"
	CategoryNetwork       Category = "network"
	CategoryMessaging     Category = "messaging"
	CategoryObservability Category = "observability"
	CategorySecurity      Category = "security"
	CategoryOther         Category = "other"
)

// Category classifies a kind. The mapping is used by the economics engine to
// decide which costs are directly attributable and which are shared platform
// overhead.
func (k Kind) Category() Category {
	switch k {
	case KindEC2Instance, KindAutoScalingGroup, KindLambdaFunction, KindECSCluster,
		KindECSService, KindECSTaskDef, KindEKSCluster, KindEKSNodeGroup, KindK8sWorkload:
		return CategoryCompute
	case KindRDSInstance, KindRDSCluster, KindRDSSnapshot, KindDynamoDBTable, KindElastiCache:
		return CategoryDatabase
	case KindEBSVolume, KindEBSSnapshot, KindS3Bucket, KindAMI:
		return CategoryStorage
	case KindALB, KindNLB, KindTargetGroup, KindCloudFront, KindAPIGateway,
		KindNATGateway, KindVPC, KindSubnet, KindSecurityGroup, KindVPCEndpoint,
		KindTransitGateway, KindRoute53Zone, KindElasticIP:
		return CategoryNetwork
	case KindSQSQueue, KindSNSTopic, KindKinesisStream, KindMSKCluster, KindEventBus:
		return CategoryMessaging
	case KindLogGroup, KindCloudTrail, KindConfigRecorder:
		return CategoryObservability
	case KindKMSKey, KindSecret:
		return CategorySecurity
	default:
		return CategoryOther
	}
}

// Service returns the AWS service code, matching the dimension used by Cost
// Explorer so that discovered resources join to billing rows.
func (k Kind) Service() string {
	parts := strings.Split(string(k), ".")
	if len(parts) >= 2 && parts[0] == "aws" {
		return parts[1]
	}
	if len(parts) >= 1 {
		return parts[0]
	}
	return "unknown"
}

// Mutable reports whether CloudOptix has any execution capability for the
// kind. Rules may raise findings on immutable kinds, but the execution engine
// refuses to plan against them.
func (k Kind) Mutable() bool {
	switch k {
	case KindEC2Instance, KindEBSVolume, KindEBSSnapshot, KindElasticIP,
		KindRDSInstance, KindRDSCluster, KindS3Bucket, KindLambdaFunction,
		KindAutoScalingGroup, KindEKSNodeGroup, KindECSService, KindDynamoDBTable,
		KindLogGroup, KindAMI:
		return true
	}
	return false
}

// State is the lifecycle state of a resource, normalized across services whose
// own vocabularies differ ("running"/"available"/"ACTIVE").
type State string

const (
	StateRunning    State = "running"
	StateStopped    State = "stopped"
	StateAvailable  State = "available"
	StateInUse      State = "in-use"
	StateIdle       State = "idle"
	StateTerminated State = "terminated"
	StatePending    State = "pending"
	StateModifying  State = "modifying"
	StateFailed     State = "failed"
	StateUnknown    State = "unknown"
)

// Active reports whether the resource is currently costing money in the normal
// case. Stopped EC2 still costs for its EBS, which is modelled on the volume.
func (s State) Active() bool {
	switch s {
	case StateRunning, StateAvailable, StateInUse, StateModifying, StateIdle:
		return true
	}
	return false
}

// Capacity is the normalized capacity vocabulary. Not every field applies to
// every kind; unset fields are zero. Keeping one struct rather than per-kind
// types is what lets a single rightsizing rule reason about an EC2 instance,
// an RDS instance and an ElastiCache node without a type switch.
type Capacity struct {
	VCPU             float64 `json:"vcpu,omitempty"`
	MemoryGiB        float64 `json:"memory_gib,omitempty"`
	StorageGiB       float64 `json:"storage_gib,omitempty"`
	ProvisionedIOPS  int64   `json:"provisioned_iops,omitempty"`
	ThroughputMiBps  float64 `json:"throughput_mibps,omitempty"`
	NetworkGbps      float64 `json:"network_gbps,omitempty"`
	InstanceCount    int     `json:"instance_count,omitempty"`
	DesiredCount     int     `json:"desired_count,omitempty"`
	MinCount         int     `json:"min_count,omitempty"`
	MaxCount         int     `json:"max_count,omitempty"`
	ReadReplicas     int     `json:"read_replicas,omitempty"`
	ShardCount       int     `json:"shard_count,omitempty"`
	MemoryMB         int     `json:"memory_mb,omitempty"`   // Lambda
	TimeoutSeconds   int     `json:"timeout_s,omitempty"`   // Lambda
	Concurrency      int     `json:"concurrency,omitempty"` // Lambda provisioned concurrency
	ObjectCount      int64   `json:"object_count,omitempty"`
	RetentionDays    int     `json:"retention_days,omitempty"`
	WriteCapacityRCU float64 `json:"write_capacity,omitempty"`
	ReadCapacityWCU  float64 `json:"read_capacity,omitempty"`
}

// PurchaseModel records how the capacity is paid for, which decides whether a
// commitment recommendation is even applicable.
type PurchaseModel string

const (
	PurchaseOnDemand    PurchaseModel = "on_demand"
	PurchaseSpot        PurchaseModel = "spot"
	PurchaseReserved    PurchaseModel = "reserved"
	PurchaseSavingsPlan PurchaseModel = "savings_plan"
	PurchaseServerless  PurchaseModel = "serverless"
	PurchaseUnknown     PurchaseModel = "unknown"
)

// Resource is the normalized representation of one discovered thing.
//
// It is intentionally flat and self-describing. The graph lives in
// Relationships (a separate aggregate) rather than as pointers here, so a
// resource can be persisted, cached and diffed independently of the topology.
type Resource struct {
	ID            core.ID        `json:"id"`
	TenantID      core.TenantID  `json:"tenant_id"`
	AccountID     core.AccountID `json:"account_id"`
	Region        core.Region    `json:"region"`
	AZ            string         `json:"availability_zone,omitempty"`
	Kind          Kind           `json:"kind"`
	ARN           core.ARN       `json:"arn,omitempty"`
	NativeID      string         `json:"native_id"` // i-0abc…, bucket name, function name
	Name          string         `json:"name,omitempty"`
	State         State          `json:"state"`
	InstanceType  string         `json:"instance_type,omitempty"` // m5.2xlarge, db.r5.large, gp3
	Engine        string         `json:"engine,omitempty"`        // postgres, redis, nodejs20.x
	EngineVersion string         `json:"engine_version,omitempty"`
	Capacity      Capacity       `json:"capacity"`
	Purchase      PurchaseModel  `json:"purchase_model"`
	Tags          core.Tags      `json:"tags,omitempty"`

	// Ownership attribution, resolved by the attribution engine from tags,
	// account conventions and the onboarding spec. Provenance says which.
	Environment       core.Environment `json:"environment"`
	EnvironmentSource core.Provenance  `json:"environment_source"`
	ApplicationID     core.ID          `json:"application_id,omitempty"`
	WorkloadID        core.ID          `json:"workload_id,omitempty"`
	Owner             string           `json:"owner,omitempty"`
	CostCenter        string           `json:"cost_center,omitempty"`
	Criticality       core.Criticality `json:"criticality"`

	// Attributes carries service-specific detail that has no place in the
	// common vocabulary: bucket versioning status, NAT gateway subnet,
	// distribution price class. Rules read it by key with explicit defaults.
	Attributes map[string]string `json:"attributes,omitempty"`

	CreatedAt    time.Time `json:"created_at,omitempty"`
	FirstSeenAt  time.Time `json:"first_seen_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	DiscoveredBy string    `json:"discovered_by"` // adapter name, for provenance
	Deleted      bool      `json:"deleted"`

	// MonthlyCost is the amortized run-rate attributed to this resource by the
	// cost engine. It is denormalized here because every UI surface and every
	// rule needs it, and recomputing it per read would dominate latency.
	MonthlyCost core.Money      `json:"monthly_cost"`
	CostSource  core.Provenance `json:"cost_source"`
}

// Attr reads a service-specific attribute with a default.
func (r Resource) Attr(key, def string) string {
	if r.Attributes == nil {
		return def
	}
	if v, ok := r.Attributes[key]; ok && v != "" {
		return v
	}
	return def
}

// AttrBool reads a boolean attribute.
func (r Resource) AttrBool(key string, def bool) bool {
	switch strings.ToLower(r.Attr(key, "")) {
	case "true", "yes", "enabled", "1":
		return true
	case "false", "no", "disabled", "0":
		return false
	default:
		return def
	}
}

// DisplayName is the human label used in the UI and in copilot answers.
func (r Resource) DisplayName() string {
	if r.Name != "" {
		return r.Name
	}
	if n, ok := r.Tags.Get("Name"); ok && n != "" {
		return n
	}
	return r.NativeID
}

// Key is the stable, tenant-scoped natural key of a resource. Discovery is
// idempotent on this key, which is what allows a re-scan to update rather than
// duplicate.
func (r Resource) Key() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", r.TenantID, r.AccountID, r.Region, r.Kind, r.NativeID)
}

// IsProduction is a convenience used pervasively by policy and risk code.
func (r Resource) IsProduction() bool { return r.Environment.IsProduction() }

// Age returns how long the resource has existed, or zero when unknown.
func (r Resource) Age(now time.Time) time.Duration {
	if r.CreatedAt.IsZero() {
		return 0
	}
	return now.Sub(r.CreatedAt)
}

// Validate checks the invariants every discovered resource must satisfy before
// it is admitted to the model. Rejecting here rather than downstream is what
// keeps a malformed adapter from poisoning the twin.
func (r Resource) Validate() error {
	var v core.ValidationResult
	if r.TenantID.IsZero() {
		v.Add("tenant_id", "required", core.SeverityCritical, "resource must be tenant-scoped")
	}
	if r.NativeID == "" {
		v.Add("native_id", "required", core.SeverityCritical, "resource must carry its provider identifier")
	}
	if r.Kind == "" {
		v.Add("kind", "required", core.SeverityCritical, "resource kind is required")
	}
	if r.Region == "" && r.Kind != KindS3Bucket && r.Kind != KindCloudFront && r.Kind != KindRoute53Zone {
		v.Add("region", "required", core.SeverityHigh, "regional resource is missing its region")
	}
	if err := r.AccountID.Validate(); err != nil && r.AccountID != "" {
		v.Add("account_id", "invalid", core.SeverityHigh, "%v", err)
	}
	if !r.EnvironmentSource.Valid() && r.EnvironmentSource != "" {
		v.Add("environment_source", "invalid", core.SeverityMedium, "unknown provenance %q", r.EnvironmentSource)
	}
	return v.Err()
}

// Inventory is a set of resources with lookup helpers. The rule engine, the
// twin builder and the simulator all receive one rather than a bare slice, so
// that index construction happens once per analysis run.
type Inventory struct {
	resources []Resource
	byID      map[core.ID]int
	byKind    map[Kind][]int
	byARN     map[core.ARN]int
	byNative  map[string]int
}

// NewInventory indexes a resource slice.
func NewInventory(resources []Resource) *Inventory {
	inv := &Inventory{
		resources: resources,
		byID:      make(map[core.ID]int, len(resources)),
		byKind:    make(map[Kind][]int),
		byARN:     make(map[core.ARN]int),
		byNative:  make(map[string]int, len(resources)),
	}
	for i, r := range resources {
		inv.byID[r.ID] = i
		inv.byKind[r.Kind] = append(inv.byKind[r.Kind], i)
		if r.ARN != "" {
			inv.byARN[r.ARN] = i
		}
		inv.byNative[r.NativeID] = i
	}
	return inv
}

// All returns every resource.
func (i *Inventory) All() []Resource { return i.resources }

// Len returns the resource count.
func (i *Inventory) Len() int { return len(i.resources) }

// ByID looks a resource up by CloudOptix identifier.
func (i *Inventory) ByID(id core.ID) (Resource, bool) {
	idx, ok := i.byID[id]
	if !ok {
		return Resource{}, false
	}
	return i.resources[idx], true
}

// ByARN looks a resource up by ARN.
func (i *Inventory) ByARN(arn core.ARN) (Resource, bool) {
	idx, ok := i.byARN[arn]
	if !ok {
		return Resource{}, false
	}
	return i.resources[idx], true
}

// ByNativeID looks a resource up by its provider identifier.
func (i *Inventory) ByNativeID(native string) (Resource, bool) {
	idx, ok := i.byNative[native]
	if !ok {
		return Resource{}, false
	}
	return i.resources[idx], true
}

// OfKind returns every resource of a kind.
func (i *Inventory) OfKind(k Kind) []Resource {
	idxs := i.byKind[k]
	out := make([]Resource, 0, len(idxs))
	for _, idx := range idxs {
		out = append(out, i.resources[idx])
	}
	return out
}

// OfKinds returns every resource matching any of the kinds.
func (i *Inventory) OfKinds(kinds ...Kind) []Resource {
	var out []Resource
	for _, k := range kinds {
		out = append(out, i.OfKind(k)...)
	}
	return out
}

// Filter returns the resources satisfying a predicate.
func (i *Inventory) Filter(pred func(Resource) bool) []Resource {
	var out []Resource
	for _, r := range i.resources {
		if pred(r) {
			out = append(out, r)
		}
	}
	return out
}

// TotalMonthlyCost sums the attributed run-rate across the inventory.
func (i *Inventory) TotalMonthlyCost() core.Money {
	total := core.ZeroUSD()
	for _, r := range i.resources {
		total = total.MustAdd(r.MonthlyCost)
	}
	return total
}

// KindCounts summarises the inventory for the discovery report.
func (i *Inventory) KindCounts() map[Kind]int {
	out := make(map[Kind]int, len(i.byKind))
	for k, idxs := range i.byKind {
		out[k] = len(idxs)
	}
	return out
}

// SortedKinds returns kinds ordered by descending attributed cost, which is
// the order the dashboard and the copilot present them in.
func (i *Inventory) SortedKinds() []Kind {
	costs := map[Kind]int64{}
	for _, r := range i.resources {
		costs[r.Kind] += r.MonthlyCost.Micros()
	}
	kinds := make([]Kind, 0, len(costs))
	for k := range costs {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(a, b int) bool {
		if costs[kinds[a]] != costs[kinds[b]] {
			return costs[kinds[a]] > costs[kinds[b]]
		}
		return kinds[a] < kinds[b]
	})
	return kinds
}
