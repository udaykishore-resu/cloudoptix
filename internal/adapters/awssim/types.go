package awssim

import (
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// UtilizationProfile names the shape of the utilisation series MetricCollector
// generates for a resource. It is the one field every metric-consuming rule
// ultimately cares about: whether the P99 diverges from the P50 (spiky),
// whether there is a daily/weekly cycle (cyclical), or whether the resource
// simply never does anything (idle).
type UtilizationProfile string

const (
	ProfileIdle UtilizationProfile = "idle"
	// ProfileUnused is a resource that was provisioned and then never put to
	// work. It is deliberately distinct from ProfileIdle, which models a
	// lightly-loaded but genuinely running server: an idle server still has a
	// tail (a cron job, a health check, a deploy) and so a P99 several times
	// its median, while an unused one is flat at the hypervisor's background
	// noise with no tail at all. The distinction is not cosmetic — the
	// ec2-never-used-instance rule keys on P99 rather than P50 precisely to
	// tell those two apart, and confidence is computed from the series'
	// stability, which is what makes a never-used instance a finding a policy
	// can clear without a human and a merely-idle one a finding that needs
	// someone to look.
	ProfileUnused    UtilizationProfile = "unused"
	ProfileSteady    UtilizationProfile = "steady"
	ProfileSpiky     UtilizationProfile = "spiky"
	ProfileCyclical  UtilizationProfile = "cyclical"
	ProfileSaturated UtilizationProfile = "saturated"
)

// Base carries the fields every simulated resource shares.
type Base struct {
	ID        string // native id, e.g. i-0abc123, vol-0abc123, my-bucket-name
	Name      string
	Region    core.Region
	AZ        string
	State     cloud.State
	Tags      core.Tags
	CreatedAt time.Time
}

// Application and Environment are read off Tags by the discoverer via
// AttributionRule-style keys ("Application", "Env"); a Base with no such tag
// is one of the ~15% deliberately-untagged resources.

// EC2Instance is a simulated EC2 instance.
type EC2Instance struct {
	Base
	InstanceType     string
	Platform         string // linux | windows
	Profile          UtilizationProfile
	CPUBaselineP50   float64 // anchors the metric generator
	StoppedAt        *time.Time
	NATGatewayID     string // egress path, for the egress_via edge
	SecurityGroupIDs []string
}

// EBSVolume is a simulated EBS volume.
type EBSVolume struct {
	Base
	VolumeType      string // gp2 | gp3 | io1 | io2 | st1 | sc1
	SizeGiB         float64
	IOPS            int64
	ThroughputMiBps float64
	AttachedTo      string // EC2 instance id, "" if unattached
	Encrypted       bool
}

// EBSSnapshot is a simulated EBS snapshot.
type EBSSnapshot struct {
	Base
	VolumeID string
	SizeGiB  float64
}

// ElasticIP is a simulated Elastic IP allocation.
type ElasticIP struct {
	Base
	PublicIP   string
	AttachedTo string // EC2 instance id or NAT gateway id, "" if unattached
}

// AMI is a simulated custom AMI.
type AMI struct {
	Base
	SizeGiB float64
}

// RDSInstance is a simulated RDS/Aurora instance.
type RDSInstance struct {
	Base
	InstanceClass string
	Engine        string // postgres | mysql | aurora-postgresql | aurora-mysql
	MultiAZ       bool
	StorageGiB    float64
	StorageType   string // gp2 | gp3 | io1
	IsReadReplica bool
	PrimaryID     string
	ClusterID     string // set for aurora members
	Profile       UtilizationProfile
}

// RDSCluster is a simulated Aurora cluster (the billing unit for storage and
// I/O; instances are the compute unit).
type RDSCluster struct {
	Base
	Engine      string
	InstanceIDs []string
}

// RDSSnapshot is a simulated RDS snapshot.
type RDSSnapshot struct {
	Base
	SourceID string
	SizeGiB  float64
}

// DynamoDBTable is a simulated DynamoDB table.
type DynamoDBTable struct {
	Base
	BillingMode string // provisioned | on_demand
	RCU, WCU    float64
	SizeGiB     float64
	Profile     UtilizationProfile
}

// S3Bucket is a simulated S3 bucket.
type S3Bucket struct {
	Base
	StorageGiB  map[string]float64 // storage class -> GiB
	ObjectCount int64
	// HasLifecyclePolicy is whether the bucket has any lifecycle
	// configuration at all; LifecycleRuleIDs names the individual rules
	// within it. The two are tracked separately because
	// PutBucketLifecycleConfiguration manages one rule at a time by id, and
	// a bucket can legitimately carry several — a storage-class transition
	// and a non-current-version expiry are two rules, not two attempts at
	// one. Collapsing them into a single boolean is what previously made the
	// second recommendation applied to a bucket report "already applied" and
	// do nothing.
	HasLifecyclePolicy       bool
	LifecycleRuleIDs         []string
	IncompleteMultipartCount int
	IncompleteMultipartGiB   float64
	NonCurrentVersionGiB     float64
	VersioningEnabled        bool
	PutRequestsPerMonth      int64
	GetRequestsPerMonth      int64
}

// LambdaFunction is a simulated Lambda function.
type LambdaFunction struct {
	Base
	MemoryMB               int
	AvgDurationMS          float64
	InvocationsPerMonth    int64
	Architecture           string // x86_64 | arm64
	ProvisionedConcurrency int
	Profile                UtilizationProfile
}

// ECSCluster is a simulated ECS cluster.
type ECSCluster struct{ Base }

// ECSService is a simulated ECS service.
type ECSService struct {
	Base
	ClusterID    string
	LaunchType   string // fargate | ec2
	DesiredCount int
	CPUUnits     int // 1024 == 1 vCPU
	MemoryMB     int
	Profile      UtilizationProfile
}

// EKSCluster is a simulated EKS control plane.
type EKSCluster struct{ Base }

// EKSNodeGroup is a simulated EKS managed node group.
type EKSNodeGroup struct {
	Base
	ClusterID    string
	InstanceType string
	DesiredSize  int
	// PackedFraction is the average fraction of allocatable node capacity
	// actually requested by scheduled pods.
	PackedFraction float64
	// RequestedOverActualRatio captures pods whose declared resource
	// requests wildly exceed what they use — the second, independent form
	// of EKS waste alongside low bin-packing.
	RequestedOverActualRatio float64
}

// LoadBalancer is a simulated ALB or NLB.
type LoadBalancer struct {
	Base
	Kind           string // application | network
	LCUHourAvg     float64
	TargetGroupIDs []string
}

// TargetGroup is a simulated ELBv2 target group.
type TargetGroup struct {
	Base
	LoadBalancerID    string
	TargetInstanceIDs []string
	TargetType        string // instance | ip | lambda
}

// CloudFrontDistribution is a simulated CloudFront distribution.
type CloudFrontDistribution struct {
	Base
	OriginID         string // ALB, API Gateway or S3 bucket native id
	GBOutPerMonth    float64
	RequestsPerMonth int64
}

// APIGateway is a simulated API Gateway API.
type APIGateway struct {
	Base
	Kind             string // rest | http
	TargetLambdaID   string
	TargetALBID      string
	RequestsPerMonth int64
}

// NATGateway is a simulated NAT gateway.
type NATGateway struct {
	Base
	SubnetID            string
	GBProcessedPerMonth float64
}

// VPC is a simulated VPC.
type VPC struct {
	Base
	CIDR string
}

// Subnet is a simulated subnet.
type Subnet struct {
	Base
	VPCID string
	CIDR  string
}

// SecurityGroup is a simulated security group.
type SecurityGroup struct {
	Base
	VPCID string
}

// VPCEndpoint is a simulated VPC endpoint (gateway or interface).
type VPCEndpoint struct {
	Base
	VPCID       string
	ServiceName string
}

// ElastiCacheCluster is a simulated ElastiCache cluster.
type ElastiCacheCluster struct {
	Base
	NodeType string
	Engine   string // redis | memcached
	NumNodes int
	Profile  UtilizationProfile
}

// SQSQueue is a simulated SQS queue.
type SQSQueue struct {
	Base
	RequestsPerMonth int64
}

// SNSTopic is a simulated SNS topic.
type SNSTopic struct {
	Base
	RequestsPerMonth int64
}

// LogGroup is a simulated CloudWatch log group.
type LogGroup struct {
	Base
	RetentionDays    int // 0 means never expire
	IngestGBPerMonth float64
	StoredGiB        float64
}

// KMSKey is a simulated customer-managed KMS key.
type KMSKey struct{ Base }

// Secret is a simulated Secrets Manager secret.
type Secret struct{ Base }

// Estate is the full in-memory model of one simulated AWS account. Every
// adapter in this package (Discoverer, CostIngestor, MetricCollector,
// Executor) reads and, for Executor, writes this same structure, which is
// what keeps the simulated account internally consistent: a resize applied
// by an Executor is immediately visible to the next Discover and changes
// what the next Fetch bills.
type Estate struct {
	mu sync.RWMutex

	AccountID core.AccountID
	Alias     string
	Regions   []core.Region
	Catalog   *pricing.Catalog

	EC2Instances        map[string]*EC2Instance
	EBSVolumes          map[string]*EBSVolume
	EBSSnapshots        map[string]*EBSSnapshot
	ElasticIPs          map[string]*ElasticIP
	AMIs                map[string]*AMI
	RDSInstances        map[string]*RDSInstance
	RDSClusters         map[string]*RDSCluster
	RDSSnapshots        map[string]*RDSSnapshot
	DynamoDBTables      map[string]*DynamoDBTable
	S3Buckets           map[string]*S3Bucket
	LambdaFunctions     map[string]*LambdaFunction
	ECSClusters         map[string]*ECSCluster
	ECSServices         map[string]*ECSService
	EKSClusters         map[string]*EKSCluster
	EKSNodeGroups       map[string]*EKSNodeGroup
	LoadBalancers       map[string]*LoadBalancer
	TargetGroups        map[string]*TargetGroup
	CloudFront          map[string]*CloudFrontDistribution
	APIGateways         map[string]*APIGateway
	NATGateways         map[string]*NATGateway
	VPCs                map[string]*VPC
	Subnets             map[string]*Subnet
	SecurityGroups      map[string]*SecurityGroup
	VPCEndpoints        map[string]*VPCEndpoint
	ElastiCacheClusters map[string]*ElastiCacheCluster
	SQSQueues           map[string]*SQSQueue
	SNSTopics           map[string]*SNSTopic
	LogGroups           map[string]*LogGroup
	KMSKeys             map[string]*KMSKey
	Secrets             map[string]*Secret
}

// NewEstate returns an empty estate bound to a pricing catalog. Every list
// is pre-allocated so downstream code can range over a nil-safe map.
func NewEstate(accountID core.AccountID, alias string, regions []core.Region, catalog *pricing.Catalog) *Estate {
	return &Estate{
		AccountID: accountID, Alias: alias, Regions: regions, Catalog: catalog,

		EC2Instances:        map[string]*EC2Instance{},
		EBSVolumes:          map[string]*EBSVolume{},
		EBSSnapshots:        map[string]*EBSSnapshot{},
		ElasticIPs:          map[string]*ElasticIP{},
		AMIs:                map[string]*AMI{},
		RDSInstances:        map[string]*RDSInstance{},
		RDSClusters:         map[string]*RDSCluster{},
		RDSSnapshots:        map[string]*RDSSnapshot{},
		DynamoDBTables:      map[string]*DynamoDBTable{},
		S3Buckets:           map[string]*S3Bucket{},
		LambdaFunctions:     map[string]*LambdaFunction{},
		ECSClusters:         map[string]*ECSCluster{},
		ECSServices:         map[string]*ECSService{},
		EKSClusters:         map[string]*EKSCluster{},
		EKSNodeGroups:       map[string]*EKSNodeGroup{},
		LoadBalancers:       map[string]*LoadBalancer{},
		TargetGroups:        map[string]*TargetGroup{},
		CloudFront:          map[string]*CloudFrontDistribution{},
		APIGateways:         map[string]*APIGateway{},
		NATGateways:         map[string]*NATGateway{},
		VPCs:                map[string]*VPC{},
		Subnets:             map[string]*Subnet{},
		SecurityGroups:      map[string]*SecurityGroup{},
		VPCEndpoints:        map[string]*VPCEndpoint{},
		ElastiCacheClusters: map[string]*ElastiCacheCluster{},
		SQSQueues:           map[string]*SQSQueue{},
		SNSTopics:           map[string]*SNSTopic{},
		LogGroups:           map[string]*LogGroup{},
		KMSKeys:             map[string]*KMSKey{},
		Secrets:             map[string]*Secret{},
	}
}
