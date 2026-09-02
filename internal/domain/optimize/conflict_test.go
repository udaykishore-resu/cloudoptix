package optimize

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// rec builds the minimum of a Recommendation that GroupConflicts and the
// aggregates read: an id, the resource the change targets, what it contends
// for, its saving, and the priority score Rank would have already computed.
func rec(id string, resource core.ID, domain ConflictDomain, saving float64, priority float64) Recommendation {
	return Recommendation{
		ID:                     core.ID(id),
		Finding:                Finding{ResourceID: resource},
		ConflictDomain:         domain,
		EstimatedMonthlySaving: core.USDollars(saving),
		PriorityScore:          priority,
	}
}

// TestGroupConflicts covers both directions the predicate has to get right:
// changes that genuinely compete for the same money are grouped so only one
// of them counts, and changes that genuinely compose are left alone so the
// total does not silently shrink instead.
func TestGroupConflicts(t *testing.T) {
	const nodeGroup = core.ID("res_nodegroup")
	const bucket = core.ID("res_bucket")
	const volume = core.ID("res_volume")

	cases := []struct {
		name string
		in   []Recommendation
		// wantPrimary maps recommendation id -> whether it counts toward a
		// total. Every input must appear, so a case cannot pass by omission.
		wantPrimary map[string]bool
		wantTotal   float64
		wantGrouped map[string]bool // id -> MutuallyExclusive
	}{
		{
			name: "three ways to shrink one node group keep only the highest-priority one",
			in: []Recommendation{
				rec("consolidate", nodeGroup, ConflictDomainNodeGroupCapacity, 10652.16, 41),
				rec("pod-requests", nodeGroup, ConflictDomainNodeGroupCapacity, 10371.84, 40),
				rec("node-size", nodeGroup, ConflictDomainNodeGroupCapacity, 9110.40, 22),
			},
			wantPrimary: map[string]bool{"consolidate": true, "pod-requests": false, "node-size": false},
			wantTotal:   10652.16,
			wantGrouped: map[string]bool{"consolidate": true, "pod-requests": true, "node-size": true},
		},
		{
			name: "the priority formula picks the primary, not the largest saving",
			in: []Recommendation{
				// A bigger number that the formula ranks lower — a risky,
				// hard-to-reverse change — must not become the recommended
				// one just because it is bigger.
				rec("big-but-risky", nodeGroup, ConflictDomainNodeGroupCapacity, 20000, 5),
				rec("modest-and-safe", nodeGroup, ConflictDomainNodeGroupCapacity, 8000, 60),
			},
			wantPrimary: map[string]bool{"modest-and-safe": true, "big-but-risky": false},
			wantTotal:   8000,
			wantGrouped: map[string]bool{"modest-and-safe": true, "big-but-risky": true},
		},
		{
			name: "changes on one bucket that touch different byte pools compose",
			in: []Recommendation{
				rec("tiering", bucket, ConflictDomainObjectStorageClass, 283.50, 30),
				rec("noncurrent", bucket, ConflictDomainNoncurrentVersions, 57.50, 25),
				rec("multipart", bucket, ConflictDomainIncompleteUploads, 34.50, 20),
			},
			wantPrimary: map[string]bool{"tiering": true, "noncurrent": true, "multipart": true},
			wantTotal:   375.50,
			wantGrouped: map[string]bool{"tiering": false, "noncurrent": false, "multipart": false},
		},
		{
			name: "the same domain on different resources is not a conflict",
			in: []Recommendation{
				rec("vol-a", volume, ConflictDomainVolumeCapacity, 40, 30),
				rec("vol-b", core.ID("res_other_volume"), ConflictDomainVolumeCapacity, 25, 28),
			},
			wantPrimary: map[string]bool{"vol-a": true, "vol-b": true},
			wantTotal:   65,
			wantGrouped: map[string]bool{"vol-a": false, "vol-b": false},
		},
		{
			name: "deleting a volume and re-typing it are two answers to one question",
			in: []Recommendation{
				rec("delete", volume, ConflictDomainVolumeCapacity, 40, 30),
				rec("gp3", volume, ConflictDomainVolumeCapacity, 8, 55),
			},
			wantPrimary: map[string]bool{"gp3": true, "delete": false},
			wantTotal:   8,
			wantGrouped: map[string]bool{"gp3": true, "delete": true},
		},
		{
			name: "a change that contends for nothing is never grouped",
			in: []Recommendation{
				rec("advice-a", nodeGroup, ConflictDomainNone, 500, 10),
				rec("advice-b", nodeGroup, ConflictDomainNone, 300, 9),
			},
			wantPrimary: map[string]bool{"advice-a": true, "advice-b": true},
			wantTotal:   800,
			wantGrouped: map[string]bool{"advice-a": false, "advice-b": false},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := GroupConflicts(append([]Recommendation(nil), tc.in...))
			require.Len(t, out, len(tc.in))

			byID := map[string]Recommendation{}
			for _, r := range out {
				byID[string(r.ID)] = r
			}
			for id, wantPrimary := range tc.wantPrimary {
				r, ok := byID[id]
				require.True(t, ok, "recommendation %s went missing", id)
				assert.Equal(t, wantPrimary, r.CountsTowardTotal(), "recommendation %s", id)
				if !wantPrimary {
					assert.NotEmpty(t, r.PreferredAlternativeID,
						"an alternative must name the recommendation preferred over it")
					assert.True(t, byID[string(r.PreferredAlternativeID)].IsPrimary(),
						"an alternative must point at its group's primary")
				}
			}
			for id, wantGrouped := range tc.wantGrouped {
				assert.Equal(t, wantGrouped, byID[id].MutuallyExclusive, "recommendation %s", id)
				if wantGrouped {
					assert.NotEmpty(t, byID[id].ConflictGroupID)
					assert.Len(t, byID[id].AlternativeIDs, len(tc.in)-1,
						"every other member of the group must be listed as an alternative")
				} else {
					assert.Empty(t, byID[id].ConflictGroupID)
					assert.Empty(t, byID[id].AlternativeIDs)
				}
			}
			assert.InDelta(t, tc.wantTotal, TotalPotentialSaving(out).Units(), 0.005)
		})
	}
}

// TestGroupConflictsIsIdempotent guards the re-analysis path: Analyze ranks
// and groups a set every run, and a set that has already been grouped must
// come out the same rather than accumulating stale alternatives.
func TestGroupConflictsIsIdempotent(t *testing.T) {
	const nodeGroup = core.ID("res_nodegroup")
	in := []Recommendation{
		rec("a", nodeGroup, ConflictDomainNodeGroupCapacity, 100, 30),
		rec("b", nodeGroup, ConflictDomainNodeGroupCapacity, 80, 20),
	}
	once := GroupConflicts(append([]Recommendation(nil), in...))
	twice := GroupConflicts(append([]Recommendation(nil), once...))
	assert.Equal(t, once, twice)
}

// TestGroupConflictsRegroupsWhenThePrimaryChanges proves the prior grouping
// is cleared rather than merged: a re-run in which the formula now prefers
// the other member must flip the primary, not leave both marked.
func TestGroupConflictsRegroupsWhenThePrimaryChanges(t *testing.T) {
	const nodeGroup = core.ID("res_nodegroup")
	first := GroupConflicts([]Recommendation{
		rec("a", nodeGroup, ConflictDomainNodeGroupCapacity, 100, 30),
		rec("b", nodeGroup, ConflictDomainNodeGroupCapacity, 80, 20),
	})
	require.True(t, first[0].IsPrimary())

	first[0].PriorityScore = 1 // the formula now prefers b
	second := GroupConflicts(first)

	byID := map[string]Recommendation{}
	for _, r := range second {
		byID[string(r.ID)] = r
	}
	assert.False(t, byID["a"].IsPrimary())
	assert.True(t, byID["b"].IsPrimary())
	assert.Equal(t, core.ID("b"), byID["a"].PreferredAlternativeID)
	assert.Empty(t, byID["b"].PreferredAlternativeID)
}

// TestGroupConflictsIsDeterministic pins the tie-break, since map iteration
// order would otherwise make "which of two equally-ranked fixes do we
// recommend" change between runs of the same analysis.
func TestGroupConflictsIsDeterministic(t *testing.T) {
	const nodeGroup = core.ID("res_nodegroup")
	build := func() []Recommendation {
		return []Recommendation{
			rec("zeta", nodeGroup, ConflictDomainNodeGroupCapacity, 100, 30),
			rec("alpha", nodeGroup, ConflictDomainNodeGroupCapacity, 100, 30),
		}
	}
	for i := 0; i < 20; i++ {
		out := GroupConflicts(build())
		byID := map[string]Recommendation{}
		for _, r := range out {
			byID[string(r.ID)] = r
		}
		require.True(t, byID["alpha"].IsPrimary(),
			"equal priority and equal saving must break on the recommendation id, lowest first")
	}
}

// TestDefaultConflictDomainCoversEveryMutatingAction is the guard against a
// new action silently resolving to "contends with nothing", which would
// exempt it from grouping and quietly reinstate a double count.
func TestDefaultConflictDomainCoversEveryMutatingAction(t *testing.T) {
	all := []ActionType{
		ActionResizeInstance, ActionStopInstance, ActionTerminateInstance, ActionDeleteVolume,
		ActionResizeVolume, ActionModifyVolumeType, ActionDeleteSnapshot, ActionDeregisterAMI,
		ActionReleaseElasticIP, ActionResizeRDS, ActionModifyRDSStorage, ActionRemoveRDSReplica,
		ActionStopRDS, ActionApplyS3Lifecycle, ActionAbortMultipartUploads, ActionSetLogRetention,
		ActionResizeLambdaMemory, ActionRemoveProvisionedConcurrency, ActionSwitchLambdaArch,
		ActionResizeNodeGroup, ActionAdjustPodResources, ActionEnableSpot, ActionCreateVPCEndpoint,
		ActionRemoveNATGateway, ActionScheduleShutdown, ActionPurchaseCommitment,
		ActionSwitchDynamoBilling,
	}
	for _, a := range all {
		assert.NotEqual(t, ConflictDomainNone, DefaultConflictDomain(a),
			"mutating action %s has no default conflict domain, so two rules emitting it against "+
				"one resource would both be counted", a)
	}
	// Advice mutates nothing and so mechanically excludes nothing; a rule
	// whose advice does claim another rule's dollars declares its domain
	// explicitly (see RuleAction.ConflictDomain).
	assert.Equal(t, ConflictDomainNone, DefaultConflictDomain(ActionAdvisoryOnly))
}

// TestCountAlternatives is what reconciles a summary's counts with its
// money: the alternatives are still there, they simply do not add up.
func TestCountAlternatives(t *testing.T) {
	const nodeGroup = core.ID("res_nodegroup")
	out := GroupConflicts([]Recommendation{
		rec("a", nodeGroup, ConflictDomainNodeGroupCapacity, 100, 30),
		rec("b", nodeGroup, ConflictDomainNodeGroupCapacity, 80, 20),
		rec("c", core.ID("res_other"), ConflictDomainComputeCapacity, 10, 15),
	})
	assert.Equal(t, 1, CountAlternatives(out))
	assert.Len(t, Primaries(out), 2)
}
