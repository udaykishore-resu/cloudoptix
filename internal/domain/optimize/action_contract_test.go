package optimize

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParameterContractSatisfied exercises the three shapes a contract can
// express, and in particular the failure modes the parameter-shape defect
// walked past: a key that is absent, a key that is present but empty, and a
// pair of keys that only make sense together.
func TestParameterContractSatisfied(t *testing.T) {
	cases := []struct {
		name     string
		contract ParameterContract
		params   map[string]any
		want     bool
	}{
		{
			name:     "required key present",
			contract: ParameterContract{Required: []string{"instance_type"}},
			params:   map[string]any{"instance_type": "m5.large"},
			want:     true,
		},
		{
			name:     "required key absent",
			contract: ParameterContract{Required: []string{"instance_type"}},
			params:   map[string]any{"target_instance_type": "m5.large"},
			want:     false,
		},
		{
			name:     "required key present but empty is not a value",
			contract: ParameterContract{Required: []string{"schedule"}},
			params:   map[string]any{"schedule": "   "},
			want:     false,
		},
		{
			name:     "no parameters at all fails a required contract",
			contract: ParameterContract{Required: []string{"desired_size"}},
			params:   nil,
			want:     false,
		},
		{
			name:     "an empty contract accepts anything, including nothing",
			contract: ParameterContract{},
			params:   nil,
			want:     true,
		},
		{
			name:     "any-of satisfied by one member",
			contract: ParameterContract{AnyOf: []string{"allocated_storage_gb", "storage_type", "iops"}},
			params:   map[string]any{"storage_type": "gp3"},
			want:     true,
		},
		{
			name:     "any-of satisfied by none",
			contract: ParameterContract{AnyOf: []string{"allocated_storage_gb", "storage_type", "iops"}},
			params:   map[string]any{"target_storage_type": "gp3"},
			want:     false,
		},
		{
			name:     "together: both halves present",
			contract: ParameterContract{Together: [][]string{{"transition_days", "transition_storage_class"}}},
			params:   map[string]any{"transition_days": 30, "transition_storage_class": "STANDARD_IA"},
			want:     true,
		},
		{
			name:     "together: a day count with no destination class",
			contract: ParameterContract{Together: [][]string{{"transition_days", "transition_storage_class"}}},
			params:   map[string]any{"transition_days": 30},
			want:     false,
		},
		{
			name:     "together: neither half is fine, the pair is optional",
			contract: ParameterContract{Together: [][]string{{"transition_days", "transition_storage_class"}}},
			params:   map[string]any{"noncurrent_expiration_days": 30},
			want:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := tc.contract.Satisfied(ActionResizeInstance, tc.params)
			assert.Equal(t, tc.want, got)
			if !tc.want {
				assert.NotEmpty(t, why, "a failure must explain itself; the message is the whole point")
			}
		})
	}
}

// TestApplyS3LifecycleContractRejectsAClauselessRule is the specific case
// that used to succeed while doing nothing: a lifecycle recommendation
// naming a destination class in the rule's own vocabulary and no clause the
// executor reads produced an enabled, empty lifecycle rule and a reported
// saving that never materialised.
func TestApplyS3LifecycleContractRejectsAClauselessRule(t *testing.T) {
	contract, ok := ParameterContractFor(ActionApplyS3Lifecycle)
	require.True(t, ok)

	satisfied, why := contract.Satisfied(ActionApplyS3Lifecycle, map[string]any{
		"bucket": "shopfleet-assets", "target_class": "standard_ia",
	})
	assert.False(t, satisfied)
	assert.Contains(t, why, "at least one of")

	satisfied, _ = contract.Satisfied(ActionApplyS3Lifecycle, map[string]any{
		"bucket": "shopfleet-assets", "rule_id": "cloudoptix-s3-no-lifecycle-policy",
		"transition_days": 30, "transition_storage_class": "STANDARD_IA",
	})
	assert.True(t, satisfied)
}

// TestParameterContractForAdvisory: advisory recommendations have no
// executor and therefore no parameter contract to satisfy — their
// parameters are prose for a human, not input for a machine.
func TestParameterContractForAdvisory(t *testing.T) {
	_, ok := ParameterContractFor(ActionAdvisoryOnly)
	assert.False(t, ok)
}
