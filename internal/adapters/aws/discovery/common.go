// Package discovery is one ports.ResourceDiscoverer per AWS service, each
// normalizing that service's API shapes into cloud.Resource/cloud.Relationship
// using exactly the attribute keys and capacity fields
// internal/adapters/awssim uses, so the simulator and the real adapters are
// interchangeable inputs to every downstream engine.
//
// The key design decision is one discoverer per service rather than one
// discoverer for the whole account (the shape awssim uses, appropriately,
// since it has no real per-service failure modes to isolate): a real AWS
// account can have DynamoDB throttling it hard while EC2 is fine, or EKS
// simply not enabled in a region while everything else is, and the discovery
// orchestrator runs each ports.ResourceDiscoverer concurrently with its own
// error isolation and backoff. Splitting by service is what makes "EKS is
// disabled in eu-north-1" a warning on one discoverer's output rather than a
// failed scan of the whole account.
//
// Every discoverer is built against a narrow interface it declares itself
// (e.g. ec2API in ec2.go) rather than the full generated service client, so
// it can be tested with a hand-written fake and so its actual AWS surface
// area — the exact set of Describe/List/Get calls it makes — is visible at a
// glance rather than hidden behind "whatever *ec2.Client exposes".
//
// Traceability: REQ-DSC-001..010, SPEC-DSC-001.
package discovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	awssts "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/sts"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// configFor resolves the aws.Config a discoverer's client should be built
// from, the one place every discoverer in this package goes through rather
// than each reimplementing the ports.AWSSession type assertion.
func configFor(in ports.DiscoveryInput) (aws.Config, error) {
	return awssts.FromSession(in.Session, in.Region)
}

// builder accumulates one Discover call's resources and relationships. It is
// deliberately small: unlike awssim's discoveryBuilder (which reads a whole
// in-memory estate and can defer relationship-building to a final pass over
// every kind at once) a real per-service discoverer usually has everything
// it needs to emit both a resource and its edges in the same loop iteration,
// since it is not waiting on a sibling service's pass to populate an id
// table.
type builder struct {
	in  ports.DiscoveryInput
	out ports.DiscoveryOutput
	now time.Time
	// ids maps a native AWS identifier discovered in *this* call to the
	// CloudOptix ID minted for it, so relationships within one discoverer's
	// pass (instance -> volume, cluster -> node group) can be wired without a
	// second lookup. Cross-service edges (e.g. ALB -> EC2 instance) instead
	// resolve the target through in.Existing, the inventory already on disk,
	// because the target's discoverer may not have run yet in this scan.
	ids map[string]core.ID
}

func newBuilder(in ports.DiscoveryInput) *builder {
	return &builder{in: in, now: time.Now().UTC(), ids: map[string]core.ID{}}
}

// resourceSpec is the full set of fields add needs. Passed as a struct
// rather than a long positional parameter list because several discoverers
// (EC2 especially) populate a dozen of these per resource kind, and a
// fourteen-argument positional call is unreviewable.
type resourceSpec struct {
	Kind         cloud.Kind
	NativeID     string
	ARN          core.ARN
	Name         string
	Region       core.Region
	AZ           string
	State        cloud.State
	InstanceType string
	Engine       string
	EngineVer    string
	Capacity     cloud.Capacity
	Purchase     cloud.PurchaseModel
	Tags         core.Tags
	Attributes   map[string]string
	CreatedAt    time.Time
	MonthlyCost  core.Money
	DiscoveredBy string
}

// add builds one cloud.Resource from spec, appends it to the output and
// records its minted ID for same-pass relationship linking.
func (b *builder) add(spec resourceSpec) core.ID {
	env, envSrc := core.EnvUnknown, core.ProvenanceUnknown
	if v, ok := spec.Tags.Get("Environment"); ok && v != "" {
		env, envSrc = core.NormalizeEnvironment(v), core.ProvenanceConfirmed
	}
	id := core.NewID(idPrefix(spec.Kind))
	r := cloud.Resource{
		ID: id, TenantID: b.in.TenantID, AccountID: b.in.AccountID, Region: spec.Region, AZ: spec.AZ,
		Kind: spec.Kind, ARN: spec.ARN, NativeID: spec.NativeID, Name: spec.Name, State: spec.State,
		InstanceType: spec.InstanceType, Engine: spec.Engine, EngineVersion: spec.EngineVer,
		Capacity: spec.Capacity, Purchase: spec.Purchase, Tags: spec.Tags,
		Environment: env, EnvironmentSource: envSrc, Owner: spec.Tags.First("Team", "Owner"),
		CostCenter: spec.Tags.First("CostCenter", "cost-center"), Criticality: core.CriticalityUnset,
		Attributes: spec.Attributes, CreatedAt: spec.CreatedAt, FirstSeenAt: b.now, LastSeenAt: b.now,
		DiscoveredBy: spec.DiscoveredBy, MonthlyCost: spec.MonthlyCost, CostSource: core.ProvenanceUnknown,
	}
	b.out.Resources = append(b.out.Resources, r)
	if spec.NativeID != "" {
		b.ids[spec.NativeID] = id
	}
	return id
}

// idOf resolves a native id minted earlier in this same discoverer pass.
func (b *builder) idOf(nativeID string) (core.ID, bool) {
	id, ok := b.ids[nativeID]
	return id, ok
}

// edge appends a relationship between two ids already known to this builder
// (either minted in this pass, or resolved by the caller from in.Existing).
func (b *builder) edge(kind cloud.RelationKind, fromID, toID core.ID, weight float64, confidence core.Confidence, source core.Provenance) {
	if fromID == "" || toID == "" {
		return
	}
	b.out.Relationships = append(b.out.Relationships, cloud.Relationship{
		ID: core.NewID("rel"), TenantID: b.in.TenantID, FromID: fromID, ToID: toID, Kind: kind,
		Weight: weight, Confidence: confidence, Source: source, FirstSeenAt: b.now, LastSeenAt: b.now,
	})
}

// edgeNative is edge, but resolves both ends from native AWS ids minted in
// this same pass. Most intra-service edges (instance -> its own volume) use
// this; cross-service edges resolve one or both ends from in.Existing
// instead and call edge directly.
func (b *builder) edgeNative(kind cloud.RelationKind, fromNative, toNative string, weight float64) {
	from, ok1 := b.idOf(fromNative)
	to, ok2 := b.idOf(toNative)
	if !ok1 || !ok2 {
		return
	}
	b.edge(kind, from, to, weight, 1.0, core.ProvenanceConfirmed)
}

// existingIDByNative resolves a native id against the inventory the
// orchestrator already holds (resources found by a *different* discoverer,
// possibly in an earlier scan), for cross-service edges such as an ALB
// target group routing to EC2 instances that ec2.go, not this discoverer,
// owns.
func (b *builder) existingIDByNative(nativeID string) (core.ID, bool) {
	if b.in.Existing == nil || nativeID == "" {
		return "", false
	}
	if r, ok := b.in.Existing.ByNativeID(nativeID); ok {
		return r.ID, true
	}
	return "", false
}

func idPrefix(k cloud.Kind) string {
	parts := strings.Split(string(k), ".")
	if n := len(parts); n >= 2 {
		return "res-" + parts[n-1]
	}
	return "res"
}

// tagMap is the shape every AWS tag list boils down to before it reaches
// core.Tags — a plain string map. Each service's own SDK tag type (ec2
// types.Tag, rds types.Tag, dynamodb types.Tag, …) has the same two fields
// under slightly different names, so each file converts its own tag slice
// into this shape and hands it to core.Tags directly (core.Tags is itself
// map[string]string).
type tagMap = map[string]string

// attrs builds a service-specific attribute map from alternating key/value
// pairs, matching the convention internal/adapters/awssim/discover.go
// established (attrs("multi_az", "true", "storage_type", "gp3")) so the two
// adapters' Attributes bags use identical keys for identical facts.
func attrs(kv ...string) map[string]string {
	m := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func fstr(f float64) string { return fmt.Sprintf("%g", f) }
func istr(i int64) string   { return fmt.Sprintf("%d", i) }

// wrap translates an AWS SDK error using awserr.Translate, recording a
// throttle on the output before returning so the caller's APICalls/Throttled
// accounting (which the discovery orchestrator's backoff reads) stays
// accurate even on the failing call.
func (b *builder) wrap(err error, service, op, action string) error {
	if err == nil {
		return nil
	}
	if awserr.Throttled(err) {
		b.out.Throttled++
	}
	return awserr.Translate(err, service, op, action)
}

// isThrottledOrDenied reports whether err is a rate-limit or permission
// failure, as opposed to any other kind of error. A handful of discoverers
// (DynamoDB, and any other service whose bulk List call returns bare names
// that must be Describe'd one at a time) need this to decide whether one
// item's failure is serious enough to abort the whole pass — a throttle or a
// denial is systemic and will recur on every remaining item, so it is worth
// failing fast on, while a single item's transient error is not.
func isThrottledOrDenied(err error) bool {
	return awserr.Throttled(err) || awserr.AccessDenied(err)
}

// skipUnavailable reports whether err means "this service is not offered in
// this region" — a condition every regional discoverer must treat as an
// empty, warning-carrying result rather than a hard failure.
func skipUnavailable(err error) bool { return awserr.ServiceUnavailable(err) }

func (b *builder) warnf(format string, args ...any) {
	b.out.Warnings = append(b.out.Warnings, fmt.Sprintf(format, args...))
}

func (b *builder) countCall() { b.out.APICalls++ }

// ctxWithDefaultTimeout bounds one Describe/List call. A single discoverer
// call must never hang the whole scan waiting on one stuck region.
func ctxWithDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 30*time.Second)
}

// mapState normalizes the many AWS lifecycle-state vocabularies (EC2's
// "running"/"stopped", RDS's "available"/"backing-up", ElastiCache's
// "available"/"modifying"...) onto cloud.State. One shared mapping is enough
// for nearly every discoverer because AWS services reuse the same handful of
// English words for the same handful of concepts; a discoverer whose service
// uses a state this map does not know falls through to StateUnknown rather
// than guessing, which is what tells a rule author to add it here instead of
// silently misclassifying the resource as idle or active.
func mapState(raw string) cloud.State {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "running":
		return cloud.StateRunning
	case "stopped":
		return cloud.StateStopped
	case "terminated", "deleted":
		return cloud.StateTerminated
	case "pending", "creating", "pendingacceptance", "pending-acceptance", "initiating",
		"provisioning", "backing-up", "starting", "creating-replica":
		return cloud.StatePending
	case "available", "active", "completed", "success", "in service", "in-service", "enabled":
		return cloud.StateAvailable
	case "in-use", "in_use", "attached", "associated":
		return cloud.StateInUse
	case "modifying", "updating", "stopping", "shutting-down", "deleting", "rebooting",
		"resetting-master-credentials", "renaming", "upgrading",
		"configuring-enhanced-monitoring", "maintenance", "moving-to-vpc",
		"storage-optimization", "rollingback", "rolling-back", "detaching", "disabling":
		return cloud.StateModifying
	case "failed", "error", "rejected", "expired", "impaired", "inactive",
		"incompatible-network", "incompatible-option-group", "incompatible-parameters",
		"restore-error", "storage-full":
		return cloud.StateFailed
	default:
		return cloud.StateUnknown
	}
}

// inferSGDependencies emits INFERRED depends_on edges from every previously
// discovered EC2 instance whose security_group_ids attribute names one of
// permittedSourceSGIDs onto toID (a database, cache or other data-service
// resource this discoverer just added).
//
// A security group rule is evidence of a possible dependency, not proof of
// one — the instance and the data resource might share a security group for
// reasons that have nothing to do with either calling the other, or the rule
// might be legacy and unused. Confidence 0.5 reflects that: high enough that
// the blast-radius walk (cloud.Topology.Dependents) still surfaces it, low
// enough that it decays out of a multi-hop chain quickly rather than
// inflating blast radius the way treating it as certain would. Every
// discoverer that calls this must have populated "security_group_ids" (a
// comma-separated attribute) on the EC2 instances it emits — ec2.go does —
// for there to be anything here to match against.
func (b *builder) inferSGDependencies(toID core.ID, permittedSourceSGIDs []string) {
	if len(permittedSourceSGIDs) == 0 || b.in.Existing == nil {
		return
	}
	permitted := make(map[string]bool, len(permittedSourceSGIDs))
	for _, id := range permittedSourceSGIDs {
		permitted[id] = true
	}
	for _, instance := range b.in.Existing.OfKind(cloud.KindEC2Instance) {
		sgIDs := strings.Split(instance.Attr("security_group_ids", ""), ",")
		matched := false
		for _, sg := range sgIDs {
			if permitted[strings.TrimSpace(sg)] {
				matched = true
				break
			}
		}
		if matched {
			b.edge(cloud.RelDependsOn, instance.ID, toID, 1, core.Confidence(0.5), core.ProvenanceInferred)
		}
	}
}

// tagsFromKV converts a slice of (key, value) pairs already extracted from
// whichever service-specific tag type a caller has (ec2 types.Tag, rds
// types.Tag, dynamodb types.Tag, ...) into core.Tags. Kept trivial and
// separate from the extraction itself because the extraction is the part
// that differs per service.
func tagsFromKV(pairs [][2]string) core.Tags {
	if len(pairs) == 0 {
		return nil
	}
	t := make(core.Tags, len(pairs))
	for _, p := range pairs {
		if p[0] == "" {
			continue
		}
		t[p[0]] = p[1]
	}
	return t
}
