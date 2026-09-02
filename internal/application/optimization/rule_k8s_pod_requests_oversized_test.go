package optimization

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPodRequestReclaimableNodes checks the reclaim formula: it must never
// reclaim below the headroom-adjusted need, must never reclaim the whole
// group down to zero nodes, and must reclaim nothing when the ratio is
// already at or under the headroom multiplier.
func TestPodRequestReclaimableNodes(t *testing.T) {
	cases := []struct {
		name                string
		currentNodes        int
		requestedOverActual float64
		headroom            float64
		want                int
	}{
		{
			name:         "no over-request (ratio at headroom) reclaims nothing",
			currentNodes: 10, requestedOverActual: 1.2, headroom: 1.2,
			want: 0,
		},
		{
			name:         "requests double actual usage with 1.2x headroom reclaims proportionally",
			currentNodes: 10, requestedOverActual: 2.0, headroom: 1.2,
			// neededNodes = ceil(10 * 1.2 / 2.0) = ceil(6) = 6; reclaim = 4
			want: 4,
		},
		{
			name:         "extreme over-request never reclaims the last node",
			currentNodes: 5, requestedOverActual: 20.0, headroom: 1.2,
			// neededNodes = ceil(5*1.2/20) = ceil(0.3) = 1; reclaim = 4, capped at currentNodes-1 = 4
			want: 4,
		},
		{
			name:         "zero or negative inputs are guarded, not divided-by-zero",
			currentNodes: 0, requestedOverActual: 2.0, headroom: 1.2,
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PodRequestReclaimableNodes(tc.currentNodes, tc.requestedOverActual, tc.headroom)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, got, maxIntZero(tc.currentNodes-1))
		})
	}
}

func maxIntZero(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
