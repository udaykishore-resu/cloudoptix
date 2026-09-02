// Package awssim's Discoverer is one multiplexed ResourceDiscoverer
// registering every Kind the estate can hold, rather than one discoverer
// per AWS service. A real deployment runs one discoverer per service so
// that one service throttling does not block the other twenty-four; the
// simulator has no throttling and no per-service API to isolate, so
// splitting it thirty ways would only be thirty files that all do
// `for id, x := range estate.Foo`. What the split buys in production
// (independent failure and concurrency) does not exist here, so this file
// keeps the one property that does matter for a discoverer under test: it
// is Discoverer's job to turn Estate state into cloud.Resource/
// cloud.Relationship, deterministically, on every call.
package awssim

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Discoverer implements ports.ResourceDiscoverer against an Estate reached
// through the session (see Session.Config / FromSession).
type Discoverer struct{}

var _ ports.ResourceDiscoverer = (*Discoverer)(nil)

// NewDiscoverer builds the multiplexed discoverer.
func NewDiscoverer() *Discoverer { return &Discoverer{} }

// Service reports the discoverer's identity. It covers many AWS services at
// once (see the package doc comment), so this is a synthetic code rather
// than a real Cost-Explorer SERVICE dimension.
func (d *Discoverer) Service() string { return "awssim" }

// Kinds lists every resource kind this discoverer can produce.
func (d *Discoverer) Kinds() []cloud.Kind {
	return []cloud.Kind{
		cloud.KindEC2Instance, cloud.KindEBSVolume, cloud.KindEBSSnapshot, cloud.KindElasticIP,
		cloud.KindAMI, cloud.KindRDSInstance, cloud.KindRDSCluster, cloud.KindRDSSnapshot,
		cloud.KindDynamoDBTable, cloud.KindS3Bucket, cloud.KindLambdaFunction, cloud.KindECSCluster,
		cloud.KindECSService, cloud.KindEKSCluster, cloud.KindEKSNodeGroup, cloud.KindALB, cloud.KindNLB,
		cloud.KindTargetGroup, cloud.KindCloudFront, cloud.KindAPIGateway, cloud.KindNATGateway,
		cloud.KindVPC, cloud.KindSubnet, cloud.KindSecurityGroup, cloud.KindVPCEndpoint,
		cloud.KindElastiCache, cloud.KindSQSQueue, cloud.KindSNSTopic, cloud.KindLogGroup,
		cloud.KindKMSKey, cloud.KindSecret,
	}
}

// RequiredActions lists the read-only IAM actions a real deployment of this
// scope would need. The simulator does not enforce them (Verify on the
// Broker does that), but a discoverer's RequiredActions feeds the generated
// onboarding policy, so the list is realistic rather than a placeholder.
func (d *Discoverer) RequiredActions() []string {
	return []string{
		"ec2:Describe*", "rds:Describe*", "dynamodb:ListTables", "dynamodb:DescribeTable",
		"s3:ListAllMyBuckets", "s3:GetBucketLifecycleConfiguration", "s3:GetBucketVersioning",
		"lambda:ListFunctions", "lambda:GetFunctionConcurrency", "ecs:ListClusters", "ecs:DescribeServices",
		"eks:ListClusters", "eks:DescribeNodegroup", "elasticloadbalancing:Describe*",
		"cloudfront:ListDistributions", "apigateway:GET", "elasticache:DescribeCacheClusters",
		"sqs:ListQueues", "sns:ListTopics", "logs:DescribeLogGroups", "kms:ListKeys",
		"secretsmanager:ListSecrets",
	}
}

// Discover returns every resource and relationship in the estate for the
// requested region. S3 buckets and CloudFront distributions are returned
// regardless of the requested region, matching how those two AWS APIs are
// not region-scoped in reality — a bucket has a location constraint, not a
// discovery endpoint per region.
func (d *Discoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	estate, err := FromSession(in.Session, in.Region)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	estate.mu.RLock()
	defer estate.mu.RUnlock()

	b := &discoveryBuilder{
		in: in, estate: estate, ids: map[string]core.ID{},
		now: time.Now().UTC(),
	}
	b.discoverNetwork()
	b.discoverEC2()
	b.discoverStorage()
	b.discoverRDS()
	b.discoverDynamoDB()
	b.discoverS3()
	b.discoverLambda()
	b.discoverECS()
	b.discoverEKS()
	b.discoverLoadBalancing()
	b.discoverCloudFront()
	b.discoverAPIGateway()
	b.discoverNAT()
	b.discoverElastiCache()
	b.discoverMessaging()
	b.discoverLogsAndSecurity()
	b.linkRelationships()

	return ports.DiscoveryOutput{
		Resources: b.resources, Relationships: b.relationships,
		APICalls: len(b.resources), Warnings: nil,
	}, nil
}

// discoveryBuilder accumulates one Discover call's output. ids maps a native
// AWS id to the freshly-minted CloudOptix ID assigned to it in this call, so
// relationships built later in the pass can reference resources built
// earlier without a second lookup pass.
type discoveryBuilder struct {
	in            ports.DiscoveryInput
	estate        *Estate
	resources     []cloud.Resource
	relationships []cloud.Relationship
	ids           map[string]core.ID
	now           time.Time
}

func kindPrefix(k cloud.Kind) string {
	switch k {
	case cloud.KindEC2Instance:
		return "res-ec2"
	case cloud.KindEBSVolume:
		return "res-vol"
	default:
		return "res"
	}
}

// add constructs one cloud.Resource and records its assigned ID for
// relationship linking. Minting a fresh core.NewID on every call rather than
// deriving one from the native id is intentional and safe: ResourceRepository
// upserts by Resource.Key() (tenant/account/region/kind/native id), not by
// ID, so the repository — not the discoverer — is what makes a resource's
// identity stable across repeated scans.
func (b *discoveryBuilder) add(kind cloud.Kind, nativeID, name string, region core.Region, az string,
	state cloud.State, tags core.Tags, instanceType, engine string, capacity cloud.Capacity,
	purchase cloud.PurchaseModel, attrs map[string]string, createdAt time.Time, monthlyCost core.Money) core.ID {

	env, envSrc := core.EnvUnknown, core.ProvenanceUnknown
	if v, ok := tags.Get("Environment"); ok && v != "" {
		env, envSrc = core.NormalizeEnvironment(v), core.ProvenanceConfirmed
	}
	owner := tags.First("Team")

	id := core.NewID(kindPrefix(kind))
	r := cloud.Resource{
		ID: id, TenantID: b.in.TenantID, AccountID: b.in.AccountID, Region: region, AZ: az,
		Kind: kind, NativeID: nativeID, Name: name, State: state, InstanceType: instanceType,
		Engine: engine, Capacity: capacity, Purchase: purchase, Tags: tags,
		Environment: env, EnvironmentSource: envSrc, Owner: owner, Criticality: core.CriticalityUnset,
		Attributes: attrs, CreatedAt: createdAt, FirstSeenAt: b.now, LastSeenAt: b.now,
		DiscoveredBy: "awssim", MonthlyCost: monthlyCost, CostSource: core.ProvenanceConfirmed,
	}
	b.resources = append(b.resources, r)
	b.ids[nativeID] = id
	return id
}

func (b *discoveryBuilder) edge(kind cloud.RelationKind, fromNative, toNative string, weight float64) {
	fromID, ok1 := b.ids[fromNative]
	toID, ok2 := b.ids[toNative]
	if !ok1 || !ok2 || fromNative == "" || toNative == "" {
		return
	}
	b.relationships = append(b.relationships, cloud.Relationship{
		ID: core.NewID("rel"), TenantID: b.in.TenantID, FromID: fromID, ToID: toID, Kind: kind,
		Weight: weight, Confidence: 1.0, Source: core.ProvenanceConfirmed,
		FirstSeenAt: b.now, LastSeenAt: b.now,
	})
}

func inRegion(in ports.DiscoveryInput, r core.Region) bool { return in.Region == "" || in.Region == r }

func attrs(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func fstr(f float64) string { return fmt.Sprintf("%g", f) }
