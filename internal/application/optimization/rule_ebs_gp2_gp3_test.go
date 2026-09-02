package optimization

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGP2ToGP3Target locks down the baseline-preserving mapping: gp3 must
// never be sized below gp3's own free baseline, and never below what gp2's
// derived IOPS (3/GiB, capped at 16,000) or the volume's observed throughput
// already deliver.
func TestGP2ToGP3Target(t *testing.T) {
	cases := []struct {
		name                string
		sizeGiB             float64
		observedThroughput  float64
		wantIOPS            int64
		wantThroughputMiBps float64
	}{
		{
			name:                "small volume floors at gp3's free baseline",
			sizeGiB:             100, // derived gp2 IOPS = 300, well under gp3's 3,000 baseline
			observedThroughput:  40,  // under gp3's 125 MiB/s baseline
			wantIOPS:            3000,
			wantThroughputMiBps: 125,
		},
		{
			name:                "large volume's derived gp2 IOPS exceeds gp3's baseline",
			sizeGiB:             2000, // 2000*3 = 6,000 IOPS
			observedThroughput:  60,
			wantIOPS:            6000,
			wantThroughputMiBps: 125,
		},
		{
			name:                "derived gp2 IOPS caps at gp2's own 16,000 ceiling",
			sizeGiB:             10000, // 10000*3 = 30,000, capped to 16,000
			observedThroughput:  100,
			wantIOPS:            16000,
			wantThroughputMiBps: 125,
		},
		{
			name:                "observed throughput above gp3 baseline is preserved",
			sizeGiB:             500,
			observedThroughput:  300,
			wantIOPS:            3000,
			wantThroughputMiBps: 300,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotIOPS, gotThroughput := GP2ToGP3Target(tc.sizeGiB, tc.observedThroughput)
			assert.Equal(t, tc.wantIOPS, gotIOPS)
			assert.Equal(t, tc.wantThroughputMiBps, gotThroughput)
		})
	}
}
