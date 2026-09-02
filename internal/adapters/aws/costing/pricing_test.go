package costing

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePriceList struct {
	pages [][]string
	call  int
}

func (f *fakePriceList) GetProducts(context.Context, *pricing.GetProductsInput, ...func(*pricing.Options)) (*pricing.GetProductsOutput, error) {
	if f.call >= len(f.pages) {
		return &pricing.GetProductsOutput{}, nil
	}
	page := f.pages[f.call]
	f.call++
	out := &pricing.GetProductsOutput{PriceList: page}
	if f.call < len(f.pages) {
		out.NextToken = aws.String("more")
	}
	return out, nil
}

const fixturePriceListEntry = `{
  "product": {
    "productFamily": "Compute Instance",
    "attributes": {"instanceType": "m5.large", "location": "US East (N. Virginia)"}
  },
  "terms": {
    "OnDemand": {
      "SKU1.JRTCKXETXF": {
        "priceDimensions": {
          "SKU1.JRTCKXETXF.6YS6EN2CT7": {
            "unit": "Hrs",
            "pricePerUnit": {"USD": "0.0960000000"}
          }
        }
      }
    }
  }
}`

const fixturePriceListEntryOhio = `{
  "product": {
    "productFamily": "Compute Instance",
    "attributes": {"instanceType": "m5.large", "location": "US East (Ohio)"}
  },
  "terms": {
    "OnDemand": {
      "SKU2.ABCDEF": {
        "priceDimensions": {
          "SKU2.ABCDEF.RATE1": {
            "unit": "Hrs",
            "pricePerUnit": {"USD": "0.0864000000"}
          }
        }
      }
    }
  }
}`

func TestPriceListClient_FetchEC2OnDemand_ParsesAndPaginates(t *testing.T) {
	f := &fakePriceList{pages: [][]string{{fixturePriceListEntry}, {fixturePriceListEntryOhio}}}
	c := &PriceListClient{newClient: func(aws.Config) priceListAPI { return f }}

	out, err := c.FetchEC2OnDemand(context.Background(), testSession())
	require.NoError(t, err)
	require.Len(t, out, 2)

	assert.Equal(t, "m5.large", out[0].InstanceType)
	assert.Equal(t, "US East (N. Virginia)", out[0].RegionName)
	assert.InDelta(t, 0.096, out[0].USDPerHour, 0.0001)

	assert.Equal(t, "US East (Ohio)", out[1].RegionName)
	assert.InDelta(t, 0.0864, out[1].USDPerHour, 0.0001)
}

func TestParseEC2OnDemandEntry_MissingPriceIsSkipped(t *testing.T) {
	_, ok := parseEC2OnDemandEntry(`{"product":{"attributes":{"instanceType":"m5.large","location":"US East (N. Virginia)"}},"terms":{"OnDemand":{}}}`)
	assert.False(t, ok)
}

func TestParseEC2OnDemandEntry_MalformedJSONIsSkipped(t *testing.T) {
	_, ok := parseEC2OnDemandEntry(`not json`)
	assert.False(t, ok)
}

func TestComputeRegionMultipliers(t *testing.T) {
	prices := []EC2OnDemandPrice{
		{InstanceType: "m5.large", RegionName: "US East (N. Virginia)", USDPerHour: 0.096},
		{InstanceType: "m5.large", RegionName: "US East (Ohio)", USDPerHour: 0.0864},
		{InstanceType: "m5.xlarge", RegionName: "US East (N. Virginia)", USDPerHour: 0.192}, // different type, must not pollute the ratio
	}
	mult := ComputeRegionMultipliers(prices, "US East (N. Virginia)", "m5.large")
	require.NotNil(t, mult)
	assert.InDelta(t, 1.0, mult["US East (N. Virginia)"], 0.0001)
	assert.InDelta(t, 0.9, mult["US East (Ohio)"], 0.0001)
	_, ok := mult["m5.xlarge"]
	assert.False(t, ok)
}

func TestComputeRegionMultipliers_MissingBaseRegionReturnsNil(t *testing.T) {
	mult := ComputeRegionMultipliers([]EC2OnDemandPrice{{InstanceType: "m5.large", RegionName: "EU (Ireland)", USDPerHour: 0.1}}, "US East (N. Virginia)", "m5.large")
	assert.Nil(t, mult)
}
