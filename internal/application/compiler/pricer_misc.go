package compiler

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

func priceKMSKey(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	monthly, ok := pc.pricing.ServicePrice(r.Region, "kms", "key_month")
	if !ok {
		return unpricedOutcome("no kms pricing for region %s", r.Region)
	}
	return priceOutcome{Monthly: monthly, Components: []simulate.PriceComponent{
		{Name: "customer master key", Unit: "month", Quantity: 1, UnitPrice: monthly, Monthly: monthly, PriceBasis: "on_demand"},
	}}
}

func priceSecret(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	monthly, ok := pc.pricing.ServicePrice(r.Region, "secretsmanager", "secret_month")
	if !ok {
		return unpricedOutcome("no secretsmanager pricing for region %s", r.Region)
	}
	return priceOutcome{Monthly: monthly, Components: []simulate.PriceComponent{
		{Name: "secret", Unit: "month", Quantity: 1, UnitPrice: monthly, Monthly: monthly, PriceBasis: "on_demand"},
	}}
}

func priceSQSQueue(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	price, ok := pc.pricing.ServicePrice(r.Region, "sqs", "requests")
	if !ok {
		return unpricedOutcome("no sqs pricing for region %s", r.Region)
	}
	reqs, overridden := pc.resolveAssumption(r.Address, "sqs_requests_month", 1_000_000)
	monthly := price.Scale(reqs / 1000)
	return priceOutcome{
		Monthly: monthly, UsageDependent: true,
		Components:  []simulate.PriceComponent{{Name: "requests", Unit: "1K requests", Quantity: reqs / 1000, UnitPrice: price, Monthly: monthly, PriceBasis: "on_demand"}},
		Assumptions: []simulate.Assumption{usageAssumption("sqs_requests_month", "Monthly API requests", reqs, "requests/month", overridden, "A queue has no cost of its own; every dollar is request volume.")},
	}
}

// priceMSKCluster is always Unpriced: Kafka broker instance types
// (kafka.m5.large and similar) are a distinct pricing dimension the catalog
// does not carry — the EC2 instance_types table only covers EC2's own
// families — and there is no "msk" entry in the services table either. This
// is deliberately never approximated against an EC2 instance type of similar
// specs, because AWS's actual MSK broker pricing is not identical to EC2's
// on-demand rate for the same instance family.
func priceMSKCluster(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	return unpricedOutcome("MSK broker instance pricing is not available in the pricing catalog")
}
