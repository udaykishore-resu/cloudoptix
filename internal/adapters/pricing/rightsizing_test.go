package pricing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmallerCandidates_OrderedClosestFirst(t *testing.T) {
	c := New()
	spec, ok := c.InstanceSpec("m5.4xlarge")
	require.True(t, ok)

	smaller := c.SmallerCandidates(spec)
	require.NotEmpty(t, smaller)
	assert.Equal(t, "m5.2xlarge", smaller[0].Type, "closest smaller size must come first")
	for _, s := range smaller {
		assert.Less(t, s.VCPU, spec.VCPU)
	}

	// Strictly decreasing as we walk away from spec.
	for i := 1; i < len(smaller); i++ {
		assert.LessOrEqual(t, smaller[i].VCPU, smaller[i-1].VCPU)
	}
}

func TestSmallerCandidates_SmallestSizeHasNone(t *testing.T) {
	c := New()
	spec, ok := c.InstanceSpec("m5.large")
	require.True(t, ok)
	assert.Empty(t, c.SmallerCandidates(spec))
}

func TestLargerCandidates_OrderedClosestFirst(t *testing.T) {
	c := New()
	spec, ok := c.InstanceSpec("c5.large")
	require.True(t, ok)

	larger := c.LargerCandidates(spec)
	require.NotEmpty(t, larger)
	assert.Equal(t, "c5.xlarge", larger[0].Type)
	for _, s := range larger {
		assert.Greater(t, s.VCPU, spec.VCPU)
	}
}

func TestLargerCandidates_LargestSizeHasNone(t *testing.T) {
	c := New()
	family := c.InstanceFamily("m5.large")
	require.NotEmpty(t, family)
	largest, ok := c.InstanceSpec(family[len(family)-1])
	require.True(t, ok)
	assert.Empty(t, c.LargerCandidates(largest))
}

func TestGravitonEquivalent(t *testing.T) {
	c := New()
	tests := []struct {
		from, want string
		ok         bool
	}{
		{"m5.large", "m6g.large", true},
		{"m5.2xlarge", "m6g.2xlarge", true},
		{"c5.large", "c7g.large", true},
		{"t3.micro", "t4g.micro", true},
		{"r5.large", "r6g.large", true},
	}
	for _, tt := range tests {
		got, ok := c.GravitonEquivalent(tt.from)
		assert.Equal(t, tt.ok, ok, tt.from)
		if tt.ok {
			assert.Equal(t, tt.want, got, tt.from)
		}
	}
}

func TestGravitonEquivalent_AlreadyGraviton(t *testing.T) {
	c := New()
	_, ok := c.GravitonEquivalent("m6g.large")
	assert.False(t, ok, "an arm64 instance has no graviton equivalent to move to")
}

func TestGravitonEquivalent_SizeNotOffered(t *testing.T) {
	c := New()
	// m6g in this catalog tops out below m5's largest size; verify the
	// function refuses rather than fabricating an oversized/undersized swap.
	family := c.InstanceFamily("m5.large")
	require.NotEmpty(t, family)
	largestM5 := family[len(family)-1]
	gravitonFamilyList := c.InstanceFamily("m6g.large")
	largestM6g := gravitonFamilyList[len(gravitonFamilyList)-1]
	if largestM5 == "m5."+largestM6g[len("m6g."):] {
		t.Skip("catalog sizes happen to align; nothing to assert here")
	}
	_, ok := c.GravitonEquivalent(largestM5)
	assert.False(t, ok)
}

func TestGravitonEquivalent_UnknownInstance(t *testing.T) {
	c := New()
	_, ok := c.GravitonEquivalent("does.not.exist")
	assert.False(t, ok)
}
