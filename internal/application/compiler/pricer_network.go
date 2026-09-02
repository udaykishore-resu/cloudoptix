package compiler

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

func priceNATGateway(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	hourly, ok := pc.pricing.ServicePrice(r.Region, "nat_gateway", "hours")
	if !ok {
		return unpricedOutcome("no nat_gateway pricing for region %s", r.Region)
	}
	monthly := monthlyFromHourly(hourly)
	comps := []simulate.PriceComponent{{Name: "hourly charge", Unit: "hour", Quantity: core.HoursPerMonth, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"}}
	assumptions := []simulate.Assumption(nil)
	usageDependent := false
	if gbPrice, ok := pc.pricing.ServicePrice(r.Region, "nat_gateway", "gb_processed"); ok {
		gb, overridden := pc.resolveAssumption(r.Address, "nat_gb_processed_month", 100)
		c := gbPrice.Scale(gb)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "data processed", Unit: "GB", Quantity: gb, UnitPrice: gbPrice, Monthly: c, PriceBasis: "on_demand"})
		assumptions = append(assumptions, usageAssumption("nat_gb_processed_month", "Monthly data processed", gb, "GB/month", overridden,
			"In production the data-processing charge, not the hourly fee, usually dominates a NAT gateway's bill."))
		usageDependent = true
	}
	return priceOutcome{Monthly: monthly, UsageDependent: usageDependent, Components: comps, Assumptions: assumptions}
}

func priceLB(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	svc := "alb"
	if a.Str("load_balancer_type", "application") == "network" {
		svc = "nlb"
	}
	hourly, ok := pc.pricing.ServicePrice(r.Region, svc, "hours")
	if !ok {
		return unpricedOutcome("no %s pricing for region %s", svc, r.Region)
	}
	monthly := monthlyFromHourly(hourly)
	comps := []simulate.PriceComponent{{Name: "hourly charge", Unit: "hour", Quantity: core.HoursPerMonth, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"}}
	assumptions := []simulate.Assumption(nil)
	usageDependent := false
	if lcuPrice, ok := pc.pricing.ServicePrice(r.Region, svc, "lcu_hour"); ok {
		lcu, overridden := pc.resolveAssumption(r.Address, "lb_avg_lcu", 5)
		c := monthlyFromHourly(lcuPrice.Scale(lcu))
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "LCU-hours", Unit: "LCU-hour", Quantity: lcu * core.HoursPerMonth, UnitPrice: lcuPrice, Monthly: c, PriceBasis: "on_demand"})
		assumptions = append(assumptions, usageAssumption("lb_avg_lcu", "Average consumed LCUs", lcu, "LCU", overridden, ""))
		usageDependent = true
	}
	return priceOutcome{Monthly: monthly, UsageDependent: usageDependent, Components: comps, Assumptions: assumptions}
}

func priceCloudFront(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	gbPrice, ok1 := pc.pricing.ServicePrice(r.Region, "cloudfront", "gb_out")
	reqPrice, ok2 := pc.pricing.ServicePrice(r.Region, "cloudfront", "requests")
	if !ok1 && !ok2 {
		return unpricedOutcome("no cloudfront pricing for region %s", r.Region)
	}
	gbOut, gbOverridden := pc.resolveAssumption(r.Address, "cloudfront_gb_out_month", 500)
	reqs, reqOverridden := pc.resolveAssumption(r.Address, "cloudfront_requests_month", 1_000_000)
	monthly := core.ZeroUSD()
	var comps []simulate.PriceComponent
	if ok1 {
		c := gbPrice.Scale(gbOut)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "data transferred out", Unit: "GB", Quantity: gbOut, UnitPrice: gbPrice, Monthly: c, PriceBasis: "on_demand"})
	}
	if ok2 {
		c := reqPrice.Scale(reqs / 1000)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "requests", Unit: "1K requests", Quantity: reqs / 1000, UnitPrice: reqPrice, Monthly: c, PriceBasis: "on_demand"})
	}
	assumptions := []simulate.Assumption{
		usageAssumption("cloudfront_gb_out_month", "Monthly data transferred out", gbOut, "GB/month", gbOverridden, "A distribution has no cost of its own; every dollar is usage."),
		usageAssumption("cloudfront_requests_month", "Monthly requests", reqs, "requests/month", reqOverridden, ""),
	}
	return priceOutcome{Monthly: monthly, UsageDependent: true, Components: comps, Assumptions: assumptions}
}

func priceAPIGatewayV2(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	return priceAPIGatewayRequests(pc, r, "http_request", "apigw_http_requests_month")
}

func priceAPIGatewayREST(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	return priceAPIGatewayRequests(pc, r, "rest_request", "apigw_rest_requests_month")
}

func priceAPIGatewayRequests(pc *pricerCtx, r RawResource, dimension, assumptionKey string) priceOutcome {
	price, ok := pc.pricing.ServicePrice(r.Region, "api_gateway", dimension)
	if !ok {
		return unpricedOutcome("no api_gateway %s pricing for region %s", dimension, r.Region)
	}
	reqs, overridden := pc.resolveAssumption(r.Address, assumptionKey, 1_000_000)
	monthly := price.Scale(reqs / 1000)
	return priceOutcome{
		Monthly: monthly, UsageDependent: true,
		Components:  []simulate.PriceComponent{{Name: "requests", Unit: "1K requests", Quantity: reqs / 1000, UnitPrice: price, Monthly: monthly, PriceBasis: "on_demand"}},
		Assumptions: []simulate.Assumption{usageAssumption(assumptionKey, "Monthly API requests", reqs, "requests/month", overridden, "An API has no cost of its own; every dollar is request volume.")},
	}
}

// priceVPCEndpoint is the compiler's clearest example of "free" versus
// "unpriced": a Gateway endpoint (S3, DynamoDB) genuinely has no hourly or
// data-processing charge on AWS's own price list, so it prices at exactly
// zero with Unpriced left false; only an Interface endpoint has a bill.
func priceVPCEndpoint(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	if a.Str("vpc_endpoint_type", "Gateway") == "Gateway" {
		return priceOutcome{Monthly: core.ZeroUSD(), Warnings: []string{
			"Gateway VPC endpoints (S3, DynamoDB) carry no hourly or data-processing charge",
		}}
	}
	hourly, ok := pc.pricing.ServicePrice(r.Region, "vpc_endpoint", "hour")
	if !ok {
		return unpricedOutcome("no vpc_endpoint pricing for region %s", r.Region)
	}
	monthly := monthlyFromHourly(hourly)
	comps := []simulate.PriceComponent{{Name: "hourly charge", Unit: "hour", Quantity: core.HoursPerMonth, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"}}
	usageDependent := false
	var assumptions []simulate.Assumption
	if gbPrice, ok := pc.pricing.ServicePrice(r.Region, "vpc_endpoint", "gb_processed"); ok {
		gb, overridden := pc.resolveAssumption(r.Address, "vpc_endpoint_gb_processed_month", 50)
		c := gbPrice.Scale(gb)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "data processed", Unit: "GB", Quantity: gb, UnitPrice: gbPrice, Monthly: c, PriceBasis: "on_demand"})
		assumptions = append(assumptions, usageAssumption("vpc_endpoint_gb_processed_month", "Monthly data processed", gb, "GB/month", overridden, ""))
		usageDependent = true
	}
	return priceOutcome{Monthly: monthly, UsageDependent: usageDependent, Components: comps, Assumptions: assumptions}
}

// priceEIP prices only an unattached (idle) Elastic IP: an EIP associated
// with a running instance or network interface carries no hourly charge on
// AWS's price list, so it is genuinely free, not merely un-costed.
func priceEIP(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	attached := a.Has("instance") || a.Has("network_interface") || a.Str("associate_with_private_ip", "") != ""
	if attached {
		return priceOutcome{Monthly: core.ZeroUSD(), Warnings: []string{"attached to a running instance/interface: no idle charge"}}
	}
	idle, ok := pc.pricing.ServicePrice(r.Region, "elastic_ip", "idle_hour")
	if !ok {
		return unpricedOutcome("no elastic_ip pricing for region %s", r.Region)
	}
	monthly := monthlyFromHourly(idle)
	return priceOutcome{
		Monthly: monthly, Warnings: []string{"no instance/network_interface reference found on this resource; priced as unattached (idle) — verify"},
		Components: []simulate.PriceComponent{{Name: "idle hourly charge", Unit: "hour", Quantity: core.HoursPerMonth, UnitPrice: idle, Monthly: monthly, PriceBasis: "on_demand"}},
	}
}

func priceTGWAttachment(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	hourly, ok := pc.pricing.ServicePrice(r.Region, "transit_gateway", "attachment_hour")
	if !ok {
		return unpricedOutcome("no transit_gateway pricing for region %s", r.Region)
	}
	monthly := monthlyFromHourly(hourly)
	comps := []simulate.PriceComponent{{Name: "attachment hourly charge", Unit: "hour", Quantity: core.HoursPerMonth, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"}}
	usageDependent := false
	var assumptions []simulate.Assumption
	if gbPrice, ok := pc.pricing.ServicePrice(r.Region, "transit_gateway", "gb_processed"); ok {
		gb, overridden := pc.resolveAssumption(r.Address, "tgw_gb_processed_month", 100)
		c := gbPrice.Scale(gb)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "data processed", Unit: "GB", Quantity: gb, UnitPrice: gbPrice, Monthly: c, PriceBasis: "on_demand"})
		assumptions = append(assumptions, usageAssumption("tgw_gb_processed_month", "Monthly data processed", gb, "GB/month", overridden, ""))
		usageDependent = true
	}
	return priceOutcome{Monthly: monthly, UsageDependent: usageDependent, Components: comps, Assumptions: assumptions}
}
