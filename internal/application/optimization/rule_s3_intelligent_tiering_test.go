package optimization

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// TestS3IntelligentTieringBreakEvenGiB checks the break-even formula
// (monitoring charge per object / storage saving per GiB) directly, and its
// explicit refusal on a non-positive storage delta — a tenant somehow
// pricing IA above Standard must never produce a break-even that looks
// achievable.
func TestS3IntelligentTieringBreakEvenGiB(t *testing.T) {
	cases := []struct {
		name                string
		monitoringPerObject core.Money
		storageDeltaPerGiB  core.Money
		wantBreakEvenGiB    float64
		wantValid           bool
	}{
		{
			// Both inputs land on exact micro-dollar boundaries so the
			// expected ratio can be checked without absorbing core.Money's
			// own rounding at sub-micro-dollar precision.
			name:                "typical Standard-to-IA delta",
			monitoringPerObject: core.USDollars(0.000003), // 3 micro-dollars/object
			storageDeltaPerGiB:  core.USDollars(0.012),    // 12,000 micro-dollars/GiB
			wantBreakEvenGiB:    0.000003 / 0.012,
			wantValid:           true,
		},
		{
			name:                "zero storage delta is invalid",
			monitoringPerObject: core.USDollars(0.000003),
			storageDeltaPerGiB:  core.Money{},
			wantBreakEvenGiB:    0,
			wantValid:           false,
		},
		{
			name:                "negative storage delta (IA priced above Standard) is invalid",
			monitoringPerObject: core.USDollars(0.000003),
			storageDeltaPerGiB:  core.USDollars(-0.01),
			wantBreakEvenGiB:    0,
			wantValid:           false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := S3IntelligentTieringBreakEvenGiB(tc.monitoringPerObject, tc.storageDeltaPerGiB)
			assert.Equal(t, tc.wantValid, valid)
			if tc.wantValid {
				assert.InDelta(t, tc.wantBreakEvenGiB, got, 1e-9)
			}
		})
	}
}

// TestS3IntelligentTieringBreakEvenGiB_AgainstRealCatalog cross-checks the
// pure formula against the actual catalog prices the rule uses, so a
// pricebook change that silently flips the sign of the Standard/IA delta is
// caught here.
func TestS3IntelligentTieringBreakEvenGiB_AgainstRealCatalog(t *testing.T) {
	cat := newTestCatalog(t)
	monitoringPer1M, ok := cat.ServicePrice(regionUSEast1, "s3", "monitoring_per_million_objects")
	assert.True(t, ok)
	standard, ok := cat.StoragePrice(regionUSEast1, "standard")
	assert.True(t, ok)
	ia, ok := cat.StoragePrice(regionUSEast1, "standard_ia")
	assert.True(t, ok)

	delta, err := standard.Sub(ia)
	assert.NoError(t, err)

	breakEven, valid := S3IntelligentTieringBreakEvenGiB(monitoringPer1M.Div(1_000_000), delta)
	assert.True(t, valid, "Standard must be priced above IA in the shipped catalog")
	assert.Greater(t, breakEven, 0.0)
}
