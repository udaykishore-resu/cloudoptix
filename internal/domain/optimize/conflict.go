package optimize

import (
	"fmt"
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// This file models the fact that two rules can both be right about the same
// resource and still not both be bankable.
//
// Three rules looking at one EKS node group will each report a real,
// defensible saving: shrink the node count, shrink the node size, or shrink
// the pod requests that force the node count. Each figure is correct in
// isolation. Adding them together is not — at most one of the three can be
// applied, and applying it invalidates the other two's arithmetic. A headline
// that sums them tells a CFO a number the platform cannot deliver, which is
// precisely the credibility failure CloudOptix exists to fix.
//
// The alternative recommendations are kept rather than discarded: a
// platform team may legitimately prefer fixing the pod requests to shrinking
// the node group, and hiding that choice would be its own dishonesty. They
// are kept, marked, and excluded from every total.
//
// Traceability: REQ-OPT-015, SPEC-OPT-009.

// ConflictDomain names the dimension of one resource's spend that a change
// competes for. Two recommendations conflict when they target the same
// resource in the same domain: only one of them can be applied, and whichever
// is applied changes the baseline the other was priced against.
//
// The domain is deliberately finer-grained than the resource and coarser
// than the action. Finer than the resource, because reducing an RDS
// instance's class and reducing its allocated storage genuinely compose —
// grouping everything that touches one resource would under-report by
// suppressing savings that really do add up. Coarser than the action,
// because "shrink the node group" and "shrink the pods that size the node
// group" are different verbs claiming the identical dollars.
type ConflictDomain string

const (
	// ConflictDomainNone marks a change that contends with nothing: it is
	// counted in full and never grouped. This is the resolved domain for
	// advisory recommendations unless the rule that produced one declares
	// otherwise — advice that changes nothing cannot mechanically exclude
	// another change, so the burden is on a rule that knows its advice
	// claims the same money as a sibling rule's to say so.
	ConflictDomainNone ConflictDomain = ""

	// ConflictDomainComputeCapacity covers everything that changes what a
	// single compute instance costs to run: its size, its run state, its
	// schedule and its purchase model. Rightsizing an instance and buying a
	// commitment against that same instance are the classic double count —
	// and worse than a double count, since the commitment would be bought
	// for capacity the rightsizing removes.
	ConflictDomainComputeCapacity ConflictDomain = "compute_capacity"

	// ConflictDomainNodeGroupCapacity covers every change to how much
	// capacity an EKS node group holds: node count, node size, and the pod
	// requests that determine both.
	ConflictDomainNodeGroupCapacity ConflictDomain = "node_group_capacity"

	// ConflictDomainServiceCapacity covers container-service task counts and
	// launch-type changes, which claim the same service's compute spend.
	ConflictDomainServiceCapacity ConflictDomain = "service_capacity"

	// ConflictDomainVolumeCapacity covers a block volume's existence, size
	// and type. Deleting an unattached volume and migrating it to gp3 are
	// two answers to one question.
	ConflictDomainVolumeCapacity ConflictDomain = "volume_capacity"

	// ConflictDomainObjectLifetime covers deleting a stored artifact —
	// a snapshot, an AMI, an idle address allocation. Two retention rules
	// that both want the same snapshot gone recover its cost once.
	ConflictDomainObjectLifetime ConflictDomain = "object_lifetime"

	// ConflictDomainRDSCompute covers a database's instance class, run state
	// and replica topology.
	ConflictDomainRDSCompute ConflictDomain = "rds_compute"

	// ConflictDomainRDSStorage covers a database's allocated storage and
	// storage type. Kept separate from RDS compute deliberately: shrinking
	// storage and downsizing the instance class are independent bills.
	ConflictDomainRDSStorage ConflictDomain = "rds_storage"

	// ConflictDomainRDSBackupStorage covers backup retention, a third
	// independent RDS bill.
	ConflictDomainRDSBackupStorage ConflictDomain = "rds_backup_storage"

	// ConflictDomainObjectStorageClass covers moving a bucket's current
	// objects to a cheaper class — a lifecycle transition, Intelligent
	// Tiering, or a direct storage-class change. All three re-price the same
	// bytes.
	ConflictDomainObjectStorageClass ConflictDomain = "object_storage_class"

	// ConflictDomainNoncurrentVersions covers expiring a bucket's
	// non-current object versions, a byte pool disjoint from its current
	// objects — which is why it is its own domain and composes with a
	// storage-class change on the same bucket.
	ConflictDomainNoncurrentVersions ConflictDomain = "noncurrent_versions"

	// ConflictDomainIncompleteUploads covers abandoned multipart upload
	// parts: again a disjoint byte pool, again composing with the other two.
	ConflictDomainIncompleteUploads ConflictDomain = "incomplete_uploads"

	// ConflictDomainLambdaCompute covers a function's per-invocation compute
	// bill: its memory size and its processor architecture.
	ConflictDomainLambdaCompute ConflictDomain = "lambda_compute"

	// ConflictDomainLambdaProvisioned covers provisioned concurrency, billed
	// around the clock independently of invocations.
	ConflictDomainLambdaProvisioned ConflictDomain = "lambda_provisioned_concurrency"

	// ConflictDomainNATTraffic covers a NAT gateway's processed-bytes bill:
	// removing the gateway and routing its traffic around it recover the
	// same charge.
	ConflictDomainNATTraffic ConflictDomain = "nat_traffic"

	// ConflictDomainLogRetention covers a log group's stored-bytes bill.
	ConflictDomainLogRetention ConflictDomain = "log_retention"

	// ConflictDomainTableBilling covers a table's capacity billing mode.
	ConflictDomainTableBilling ConflictDomain = "table_billing"
)

// DefaultConflictDomain resolves the domain an action contends in when the
// rule that produced it does not declare one. The mapping is exhaustive over
// the mutating actions: an action added to the closed ActionType set without
// an entry here resolves to ConflictDomainNone, which means "counted in
// full" — so the accompanying contract test asserts every mutating action
// has one, rather than leaving the omission to surface as an inflated total.
func DefaultConflictDomain(a ActionType) ConflictDomain {
	switch a {
	case ActionResizeInstance, ActionStopInstance, ActionTerminateInstance,
		ActionScheduleShutdown, ActionEnableSpot, ActionPurchaseCommitment:
		return ConflictDomainComputeCapacity
	case ActionResizeNodeGroup, ActionAdjustPodResources:
		return ConflictDomainNodeGroupCapacity
	case ActionDeleteVolume, ActionResizeVolume, ActionModifyVolumeType:
		return ConflictDomainVolumeCapacity
	case ActionDeleteSnapshot, ActionDeregisterAMI, ActionReleaseElasticIP:
		return ConflictDomainObjectLifetime
	case ActionResizeRDS, ActionStopRDS, ActionRemoveRDSReplica:
		return ConflictDomainRDSCompute
	case ActionModifyRDSStorage:
		return ConflictDomainRDSStorage
	case ActionApplyS3Lifecycle:
		return ConflictDomainObjectStorageClass
	case ActionAbortMultipartUploads:
		return ConflictDomainIncompleteUploads
	case ActionSetLogRetention:
		return ConflictDomainLogRetention
	case ActionResizeLambdaMemory, ActionSwitchLambdaArch:
		return ConflictDomainLambdaCompute
	case ActionRemoveProvisionedConcurrency:
		return ConflictDomainLambdaProvisioned
	case ActionCreateVPCEndpoint, ActionRemoveNATGateway:
		return ConflictDomainNATTraffic
	case ActionSwitchDynamoBilling:
		return ConflictDomainTableBilling
	}
	return ConflictDomainNone
}

// ConflictGroupKey is the identity of a conflict group: one resource, one
// contended domain. It is derived rather than minted so that two analysis
// runs over an unchanged estate produce the same group ids, which is what
// lets the UI keep a user's "I prefer the pod-request fix" choice stable
// across runs.
func ConflictGroupKey(resourceID string, domain ConflictDomain) string {
	if resourceID == "" || domain == ConflictDomainNone {
		return ""
	}
	return fmt.Sprintf("cg:%s:%s", resourceID, domain)
}

// IsPrimary reports whether this recommendation is the one CloudOptix
// recommends within its conflict group — which every recommendation outside
// a group trivially is.
func (r Recommendation) IsPrimary() bool { return r.PreferredAlternativeID.IsZero() }

// CountsTowardTotal reports whether this recommendation's estimated saving
// may be added to an aggregate. Every aggregate in the platform — the
// analysis run's total, the dashboard summary, the savings funnel's
// potential stage, the executive summary, the efficiency score's identified
// waste — must consult this rather than summing EstimatedMonthlySaving
// blindly; a single call site that forgets silently reinstates the
// double-counted headline this file exists to prevent.
func (r Recommendation) CountsTowardTotal() bool { return r.IsPrimary() }

// GroupConflicts partitions a scored recommendation set into conflict
// groups, marks the highest-priority member of each group as the primary and
// the rest as alternatives, and returns the annotated set.
//
// It must run after Rank: the primary is "the one the priority formula ranks
// highest", so a set whose PriorityScore is still zero would pick its primary
// on the saving tie-break alone — which is the ranking the formula exists to
// override (the biggest number is not the one to do first). Ties fall back to
// the larger saving and then to the recommendation id, so the choice is
// deterministic run to run rather than dependent on map iteration order.
//
// Any prior grouping on the input is cleared first, so re-running over an
// already-grouped set is idempotent rather than cumulative.
func GroupConflicts(recs []Recommendation) []Recommendation {
	byGroup := map[string][]int{}
	for i := range recs {
		recs[i].ConflictGroupID = ""
		recs[i].MutuallyExclusive = false
		recs[i].AlternativeIDs = nil
		recs[i].PreferredAlternativeID = ""

		key := ConflictGroupKey(string(recs[i].Finding.ResourceID), recs[i].ConflictDomain)
		if key == "" {
			continue
		}
		byGroup[key] = append(byGroup[key], i)
	}

	for key, members := range byGroup {
		if len(members) < 2 {
			// A domain with one claimant is not a conflict, and labelling it
			// as one would put a "3 ways to fix this" affordance on a screen
			// that has exactly one.
			continue
		}
		sort.SliceStable(members, func(a, b int) bool {
			x, y := recs[members[a]], recs[members[b]]
			if x.PriorityScore != y.PriorityScore {
				return x.PriorityScore > y.PriorityScore
			}
			if x.EstimatedMonthlySaving.Micros() != y.EstimatedMonthlySaving.Micros() {
				return x.EstimatedMonthlySaving.Micros() > y.EstimatedMonthlySaving.Micros()
			}
			return x.ID < y.ID
		})
		primary := recs[members[0]].ID
		for pos, idx := range members {
			recs[idx].ConflictGroupID = key
			recs[idx].MutuallyExclusive = true
			recs[idx].AlternativeIDs = otherIDs(recs, members, pos)
			if pos > 0 {
				recs[idx].PreferredAlternativeID = primary
			}
		}
	}
	return recs
}

// otherIDs lists the ids of every member of a group except the one at
// position skip, preserving the group's ranked order so a UI can render
// "here is the one we recommend, and here are the others in the order we
// would pick them".
func otherIDs(recs []Recommendation, members []int, skip int) []core.ID {
	out := make([]core.ID, 0, len(members)-1)
	for pos, idx := range members {
		if pos == skip {
			continue
		}
		out = append(out, recs[idx].ID)
	}
	return out
}

// Primaries returns only the recommendations whose saving counts toward a
// total, preserving input order. It is the filter every aggregate over a
// recommendation slice should apply.
func Primaries(recs []Recommendation) []Recommendation {
	out := make([]Recommendation, 0, len(recs))
	for _, r := range recs {
		if r.CountsTowardTotal() {
			out = append(out, r)
		}
	}
	return out
}

// CountAlternatives returns how many recommendations in the set are
// alternatives suppressed from totals — the number a dashboard needs to say
// "and 4 alternative approaches to the same six problems" rather than
// silently dropping them.
func CountAlternatives(recs []Recommendation) int {
	n := 0
	for _, r := range recs {
		if !r.CountsTowardTotal() {
			n++
		}
	}
	return n
}
