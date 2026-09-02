package specsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyDottedPatch_ScalarAndArrayIndex(t *testing.T) {
	base := baseSpec()

	patch := map[string]any{
		"objectives.availabilityTarget": 0.9999,
		"workloads[0].name":             "api",
		"workloads[0].criticality":      "high",
		"workloads[1].name":             "worker",
		"aws.accounts[0].regions[1]":    "us-west-2",
	}

	out, err := applyDottedPatch(base, patch)
	require.NoError(t, err)

	assert.Equal(t, 0.9999, out.Objectives.AvailabilityTarget)
	require.Len(t, out.Workloads, 2, "indexing past the end must grow the slice")
	assert.Equal(t, "api", out.Workloads[0].Name)
	assert.Equal(t, "high", out.Workloads[0].Criticality)
	assert.Equal(t, "worker", out.Workloads[1].Name)
	require.Len(t, out.AWS.Accounts[0].Regions, 2)
	assert.Equal(t, "us-west-2", out.AWS.Accounts[0].Regions[1])

	// base must be untouched: the patch is a copy-on-write operation.
	assert.Empty(t, base.Workloads)
	assert.NotEqual(t, 0.9999, base.Objectives.AvailabilityTarget)
}

func TestApplyDottedPatch_WholeObjectAtOnePath(t *testing.T) {
	base := baseSpec()
	patch := map[string]any{
		"aws.cur": map[string]any{"bucket": "my-cur-bucket", "prefix": "cur/"},
	}
	out, err := applyDottedPatch(base, patch)
	require.NoError(t, err)
	require.NotNil(t, out.AWS.CUR)
	assert.Equal(t, "my-cur-bucket", out.AWS.CUR.Bucket)
	assert.Equal(t, "cur/", out.AWS.CUR.Prefix)
}

func TestApplyDottedPatch_UnknownFieldRejected(t *testing.T) {
	_, err := applyDottedPatch(baseSpec(), map[string]any{"objectives.notAField": 1})
	assert.Error(t, err)
}

func TestApplyDottedPatch_TypeMismatchRejected(t *testing.T) {
	_, err := applyDottedPatch(baseSpec(), map[string]any{"objectives.availabilityTarget": "not-a-number"})
	assert.Error(t, err)
}

func TestPathPermission(t *testing.T) {
	tests := map[string]bool{
		"automation.enabled":                        true,
		"automation.maintenanceWindows[0].startUtc": true,
		"governance.minApprovals":                   true,
		"objectives.availabilityTarget":             false,
		"workloads[0].name":                         false,
	}
	for path, wantsExtra := range tests {
		got := pathPermission(path)
		if wantsExtra {
			assert.NotEmpty(t, got, "path %q should require an extra permission", path)
		} else {
			assert.Empty(t, got, "path %q should not require an extra permission", path)
		}
	}
}

// TestApplyDottedPatch_ObjectKeysUseTheYAMLVocabulary is the regression test
// for a silent half-application. Path segments have always resolved against
// yaml tags, but the keys *inside* an object assigned at a leaf were decoded
// against json tags, and spec.Spec's two tag sets disagree throughout
// (startUtc vs start_utc). json.Unmarshal ignores a key it cannot match, so
// a patch written in the documented vocabulary produced an object with some
// fields set and the rest at their zero values, and returned no error at
// all — which is how the demo tenant ended up declaring a maintenance window
// with no start time and a zero duration, and therefore never being inside
// one.
func TestApplyDottedPatch_ObjectKeysUseTheYAMLVocabulary(t *testing.T) {
	cases := []struct {
		name  string
		patch map[string]any
	}{
		{
			name: "yaml spelling, as a slice of maps",
			patch: map[string]any{
				"automation.maintenanceWindows": []map[string]any{{
					"name": "overnight-utc", "days": []string{"tuesday"},
					"startUtc": "02:00", "durationMinutes": 240,
					"environments": []string{"staging"},
				}},
			},
		},
		{
			name: "yaml spelling, as a slice of any",
			patch: map[string]any{
				"automation.maintenanceWindows": []any{map[string]any{
					"name": "overnight-utc", "days": []any{"tuesday"},
					"startUtc": "02:00", "durationMinutes": 240,
					"environments": []any{"staging"},
				}},
			},
		},
		{
			name: "json spelling still works, so an existing caller is not broken",
			patch: map[string]any{
				"automation.maintenanceWindows": []map[string]any{{
					"name": "overnight-utc", "days": []string{"tuesday"},
					"start_utc": "02:00", "duration_minutes": 240,
					"environments": []string{"staging"},
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := applyDottedPatch(baseSpec(), tc.patch)
			require.NoError(t, err)
			require.Len(t, out.Automation.MaintenanceWindows, 1)
			w := out.Automation.MaintenanceWindows[0]
			assert.Equal(t, "overnight-utc", w.Name)
			assert.Equal(t, []string{"tuesday"}, w.Days)
			assert.Equal(t, "02:00", w.StartUTC, "a window with no start time can never be entered")
			assert.Equal(t, 240, w.DurationMinutes, "a zero-duration window can never be entered")
			assert.Equal(t, []string{"staging"}, w.Environments)
		})
	}
}

// TestApplyDottedPatch_UnknownObjectKeyIsStillIgnored: key translation only
// rewrites keys it can resolve against the target struct, so an unrecognised
// one reaches json.Unmarshal and is dropped exactly as it was before — the
// fix tightens nothing it was not asked to tighten.
func TestApplyDottedPatch_UnknownObjectKeyIsStillIgnored(t *testing.T) {
	out, err := applyDottedPatch(baseSpec(), map[string]any{
		"aws.cur": map[string]any{"bucket": "b", "notAField": "ignored"},
	})
	require.NoError(t, err)
	require.NotNil(t, out.AWS.CUR)
	assert.Equal(t, "b", out.AWS.CUR.Bucket)
}
