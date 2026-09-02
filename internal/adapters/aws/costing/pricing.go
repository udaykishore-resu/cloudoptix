// This file wraps the AWS Price List Query API (the "pricing" SDK client)
// to refresh internal/adapters/pricing's embedded price book.
//
// That package's own doc comment explains why pricing is a static, embedded
// JSON file rather than a live API call on the hot path: the Price List API
// is neither fast nor stable enough to sit under the cost compiler's inner
// loop. This file is the other half of that design — the offline refresh
// procedure, run by a human on a schedule (quarterly is reasonable; AWS
// revises SKUs continuously but not the headline on-demand rates that
// often), never by the running service against itself. Concretely:
//
//  1. Run FetchEC2OnDemand (and, following the identical GetProducts +
//     Filters pattern with a different ServiceCode/attribute set, the
//     equivalent RDS and ElastiCache queries) against any AWS account —
//     Price List data is public and identical for every account, so no
//     customer credentials are involved in a refresh.
//  2. Use ComputeRegionMultipliers to turn the per-region on-demand prices
//     for one reference instance type into the ratios
//     internal/adapters/pricing/pricebook.json's region_multipliers field
//     stores (that file prices one base region outright and every other
//     region as a multiplier of it, rather than storing every price for
//     every region — see that package's book struct).
//  3. Diff the result against the currently committed pricebook.json by
//     hand and commit the merge as a normal, reviewed code change.
//
// This wrapper deliberately never writes to pricebook.json itself: that
// file lives in internal/adapters/pricing, outside this package's target
// directory, and a step that materially changes every cost figure the
// platform reports belongs behind code review, not inside a runtime path.
//
// Only the EC2 on-demand case is implemented end-to-end here, as the
// reference for the pattern; RDS and ElastiCache would follow it exactly
// (ServiceCode "AmazonRDS"/"AmazonElastiCache", their own attribute filters)
// but are left undone rather than guessed at without the ability to test a
// live GetProducts call against those service codes' actual attribute
// vocabulary in this environment.
package costing

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	awssts "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/sts"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// priceListRegion is where the Price List Query API endpoint itself lives
// (us-east-1, plus ap-south-1 as an alternate — either works and returns
// identical global data), which is unrelated to which region's prices a
// query asks about: that is selected through the "location" attribute
// filter, which the Price List API names with a human region name ("US
// East (N. Virginia)"), not a region code.
const priceListRegion = core.Region("us-east-1")

type priceListAPI interface {
	GetProducts(ctx context.Context, in *pricing.GetProductsInput, optFns ...func(*pricing.Options)) (*pricing.GetProductsOutput, error)
}

// PriceListClient wraps the AWS Price List Query API for the offline
// pricebook refresh procedure documented above.
type PriceListClient struct {
	newClient func(aws.Config) priceListAPI
}

// NewPriceListClient builds the Price List client.
func NewPriceListClient() *PriceListClient {
	return &PriceListClient{newClient: func(cfg aws.Config) priceListAPI { return pricing.NewFromConfig(cfg) }}
}

// EC2OnDemandPrice is one parsed EC2 on-demand SKU.
type EC2OnDemandPrice struct {
	InstanceType string  `json:"instance_type"`
	RegionName   string  `json:"region_name"` // Price List's human region name, e.g. "US East (N. Virginia)"
	USDPerHour   float64 `json:"usd_per_hour"`
}

// FetchEC2OnDemand queries every Linux, shared-tenancy, no-preinstalled-
// software on-demand EC2 SKU (the same dimension awssim and pricebook.json
// price on-demand EC2 against) and returns one EC2OnDemandPrice per
// (instance type, region) pair the API returns.
func (c *PriceListClient) FetchEC2OnDemand(ctx context.Context, session ports.AWSSession) ([]EC2OnDemandPrice, error) {
	cfg, err := awssts.FromSession(session, priceListRegion)
	if err != nil {
		return nil, err
	}
	client := c.newClient(cfg)

	filters := []pricingtypes.Filter{
		{Type: pricingtypes.FilterTypeTermMatch, Field: aws.String("operatingSystem"), Value: aws.String("Linux")},
		{Type: pricingtypes.FilterTypeTermMatch, Field: aws.String("tenancy"), Value: aws.String("Shared")},
		{Type: pricingtypes.FilterTypeTermMatch, Field: aws.String("capacitystatus"), Value: aws.String("Used")},
		{Type: pricingtypes.FilterTypeTermMatch, Field: aws.String("preInstalledSw"), Value: aws.String("NA")},
	}

	var out []EC2OnDemandPrice
	var nextToken *string
	for {
		resp, err := client.GetProducts(ctx, &pricing.GetProductsInput{
			ServiceCode: aws.String("AmazonEC2"), Filters: filters,
			FormatVersion: aws.String("aws_v1"), NextToken: nextToken,
		})
		if err != nil {
			return nil, awserr.Translate(err, "pricing", "GetProducts", "pricing:GetProducts")
		}
		for _, raw := range resp.PriceList {
			if p, ok := parseEC2OnDemandEntry(raw); ok {
				out = append(out, p)
			}
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			break
		}
		nextToken = resp.NextToken
	}
	return out, nil
}

// priceListEntry is the JSON shape of one string in GetProductsOutput's
// PriceList — the AWS Price List Query API's stable, publicly documented
// per-SKU product+pricing document. Both nesting levels under "terms" key
// on IDs generated per SKU/offer-term/rate-code, which is why they are
// modeled as maps rather than named fields: this code only ever needs "the
// one OnDemand price this SKU has", never a specific rate code.
type priceListEntry struct {
	Product struct {
		ProductFamily string            `json:"productFamily"`
		Attributes    map[string]string `json:"attributes"`
	} `json:"product"`
	Terms struct {
		OnDemand map[string]struct {
			PriceDimensions map[string]struct {
				Unit         string            `json:"unit"`
				PricePerUnit map[string]string `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

func parseEC2OnDemandEntry(raw string) (EC2OnDemandPrice, bool) {
	var e priceListEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return EC2OnDemandPrice{}, false
	}
	instanceType := e.Product.Attributes["instanceType"]
	region := e.Product.Attributes["location"]
	if instanceType == "" || region == "" {
		return EC2OnDemandPrice{}, false
	}
	usd, ok := firstOnDemandUSD(e.Terms.OnDemand)
	if !ok {
		return EC2OnDemandPrice{}, false
	}
	return EC2OnDemandPrice{InstanceType: instanceType, RegionName: region, USDPerHour: usd}, true
}

func firstOnDemandUSD(onDemand map[string]struct {
	PriceDimensions map[string]struct {
		Unit         string            `json:"unit"`
		PricePerUnit map[string]string `json:"pricePerUnit"`
	} `json:"priceDimensions"`
}) (float64, bool) {
	for _, term := range onDemand {
		for _, dim := range term.PriceDimensions {
			usdStr, ok := dim.PricePerUnit["USD"]
			if !ok {
				continue
			}
			usd, err := strconv.ParseFloat(usdStr, 64)
			if err != nil || usd == 0 {
				continue
			}
			return usd, true
		}
	}
	return 0, false
}

// ComputeRegionMultipliers turns a set of per-region on-demand prices for
// one reference instance type into the ratio-to-base-region multipliers
// pricebook.json's region_multipliers field stores. referenceInstanceType
// should be a widely-available, stably-priced type (m5.large is what
// pricebook.json currently uses) so the ratio reflects regional cost
// variance rather than availability quirks of an unusual type.
func ComputeRegionMultipliers(prices []EC2OnDemandPrice, baseRegionName, referenceInstanceType string) map[string]float64 {
	byRegion := make(map[string]float64)
	for _, p := range prices {
		if p.InstanceType == referenceInstanceType {
			byRegion[p.RegionName] = p.USDPerHour
		}
	}
	base, ok := byRegion[baseRegionName]
	if !ok || base == 0 {
		return nil
	}
	multipliers := make(map[string]float64, len(byRegion))
	for region, price := range byRegion {
		multipliers[region] = price / base
	}
	return multipliers
}
