package optimization

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestS3TransitionStorageClass pins the translation between the two
// vocabularies that the S3 lifecycle rules previously conflated: this
// package prices against the catalog's lower-case class names, and
// ec2/s3 lifecycle APIs take upper-case TransitionStorageClass values. A
// rule that emitted the former where the executor read the latter produced
// a lifecycle rule with no transition in it at all.
func TestS3TransitionStorageClass(t *testing.T) {
	cases := []struct {
		catalogClass string
		want         string
		wantOK       bool
	}{
		{"standard_ia", "STANDARD_IA", true},
		{"onezone_ia", "ONEZONE_IA", true},
		{"intelligent_tiering", "INTELLIGENT_TIERING", true},
		{"glacier", "GLACIER", true},
		{"deep_archive", "DEEP_ARCHIVE", true},
		// STANDARD is not a transition target: an object cannot transition
		// "up" into it, and a rule that asked S3 to would be rejected.
		{"standard", "", false},
		{"", "", false},
		{"nonsense", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.catalogClass, func(t *testing.T) {
			got, ok := S3TransitionStorageClass(tc.catalogClass)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestS3LifecycleRuleIDsAreDistinctPerRule is what lets a bucket carry both
// a tiering transition and a non-current-version expiry. apply_s3_lifecycle
// manages exactly one rule inside the bucket's configuration keyed by
// rule_id, so two recommendations sharing the default id would have each
// silently replaced the other.
func TestS3LifecycleRuleIDsAreDistinctPerRule(t *testing.T) {
	ids := map[string]bool{}
	for _, id := range []struct {
		rule string
		got  string
	}{
		{"no-lifecycle", s3LifecycleRuleID(RuleIDS3NoLifecycle)},
		{"noncurrent", s3LifecycleRuleID(RuleIDS3NoncurrentVersions)},
		{"intelligent-tiering", s3LifecycleRuleID(RuleIDS3IntelligentTiering)},
		{"wrong-storage-class", s3LifecycleRuleID(RuleIDS3WrongStorageClass)},
	} {
		assert.False(t, ids[id.got], "%s reuses the lifecycle rule id %q", id.rule, id.got)
		ids[id.got] = true
	}
}

// TestS3TransitionParametersDeclinesAnImpossibleTarget: a rule that cannot
// name a valid transition target must decline to propose an executable
// change rather than propose one with the field missing.
func TestS3TransitionParametersDeclinesAnImpossibleTarget(t *testing.T) {
	_, ok := s3TransitionParameters(RuleIDS3NoLifecycle, "bucket-1", "standard", 30)
	assert.False(t, ok)

	params, ok := s3TransitionParameters(RuleIDS3NoLifecycle, "bucket-1", "standard_ia", 30)
	assert.True(t, ok)
	assert.Equal(t, "STANDARD_IA", params["transition_storage_class"])
	assert.Equal(t, 30, params["transition_days"])
	assert.Equal(t, "cloudoptix-s3-no-lifecycle-policy", params["rule_id"])
}

// TestScheduleFromActiveHours renders detected telemetry into the one string
// the schedule_shutdown executor stores and compares against. The rule used
// to emit the raw hour list instead, under a key no executor reads, so the
// instance was tagged with the executor's generic weekday default rather
// than the window its own metrics showed.
func TestScheduleFromActiveHours(t *testing.T) {
	cases := []struct {
		name  string
		hours []int
		want  string
	}{
		{
			name:  "a contiguous business-hours window runs to the end of its last hour",
			hours: []int{8, 9, 10, 11, 12, 13, 14, 15, 16, 17},
			want:  "run 08:00-18:00 UTC daily; stopped otherwise",
		},
		{
			name:  "unsorted input is normalised, not trusted",
			hours: []int{10, 8, 9},
			want:  "run 08:00-11:00 UTC daily; stopped otherwise",
		},
		{
			name:  "a window ending at midnight wraps to 00:00 rather than reporting hour 24",
			hours: []int{21, 22, 23},
			want:  "run 21:00-00:00 UTC daily; stopped otherwise",
		},
		{
			name:  "a single active hour",
			hours: []int{3},
			want:  "run 03:00-04:00 UTC daily; stopped otherwise",
		},
		{
			name: "non-contiguous peaks are listed rather than widened into a range",
			// Rendering {2,3,14,15} as "02:00-16:00" would silently keep the
			// instance running for ten hours nobody asked for.
			hours: []int{2, 3, 14, 15},
			want:  "run during UTC hours 02,03,14,15; stopped otherwise",
		},
		{
			name:  "no detected peaks yields no schedule at all",
			hours: nil,
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ScheduleFromActiveHours(tc.hours))
		})
	}
}

// TestScheduleFromActiveHoursDoesNotMutateItsInput matters because the slice
// it is given is the live core.Percentiles.PeakHours of a metric summary
// several other rules read from the same EvalContext.
func TestScheduleFromActiveHoursDoesNotMutateItsInput(t *testing.T) {
	hours := []int{17, 8, 12}
	ScheduleFromActiveHours(hours)
	assert.Equal(t, []int{17, 8, 12}, hours)
}
