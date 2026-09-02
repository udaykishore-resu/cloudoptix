package optimization

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

// TestDecideNATVPCEndpoint_ExactSaving checks the saving is computed exactly
// as the S3/DynamoDB share of processed bytes times the catalog's real
// per-GB NAT processing rate — not an estimate, and not the full NAT bill.
func TestDecideNATVPCEndpoint_ExactSaving(t *testing.T) {
	r := mkResource(cloud.KindNATGateway, "")
	r.State = cloud.StateAvailable
	r.Attributes = map[string]string{
		"gb_processed_month":           "1000",
		"s3_dynamodb_traffic_fraction": "0.4",
	}
	inv := cloud.NewInventory([]cloud.Resource{r})
	ctx := testEvalContext(inv, nil, nil, testSpec())

	gbPrice, found := ctx.Pricing.ServicePrice(r.Region, "nat_gateway", "gb_processed")
	require.True(t, found)

	totalGB, fraction, saving, ok := decideNATVPCEndpoint(ctx, r)
	require.True(t, ok)
	assert.Equal(t, 1000.0, totalGB)
	assert.Equal(t, 0.4, fraction)

	wantSaving := gbPrice.Scale(1000.0 * 0.4)
	assert.Equal(t, wantSaving.Micros(), saving.Micros(), "saving must be exactly the S3/DynamoDB share of bytes times the NAT per-GB rate")
}

// TestDecideNATVPCEndpoint_Guards checks the rule declines rather than
// guesses when the signal is missing or below the material-share threshold.
func TestDecideNATVPCEndpoint_Guards(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]string
	}{
		{name: "no attributes at all (no signal)", attrs: nil},
		{name: "missing fraction attribute", attrs: map[string]string{"gb_processed_month": "1000"}},
		{name: "fraction below the material threshold", attrs: map[string]string{"gb_processed_month": "1000", "s3_dynamodb_traffic_fraction": "0.05"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := mkResource(cloud.KindNATGateway, "")
			r.State = cloud.StateAvailable
			r.Attributes = tc.attrs
			inv := cloud.NewInventory([]cloud.Resource{r})
			ctx := testEvalContext(inv, nil, nil, testSpec())
			_, _, _, ok := decideNATVPCEndpoint(ctx, r)
			assert.False(t, ok)
		})
	}
}
