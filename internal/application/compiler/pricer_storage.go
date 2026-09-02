package compiler

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// gp3BaselineIOPS and gp3BaselineThroughput are the IOPS and throughput every
// gp3 volume gets for free; only usage above these baselines is billed
// separately, matching AWS's actual gp3 pricing model.
const (
	gp3BaselineIOPS       = 3000.0
	gp3BaselineThroughput = 125.0
)

func priceEBSVolume(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	size := a.Float("size", 0)
	if size <= 0 {
		return unpricedOutcome("%s has no size", r.Address)
	}
	volType := a.Str("type", "gp3")
	sp, ok := pc.pricing.StoragePrice(r.Region, volType)
	if !ok {
		return unpricedOutcome("no pricing data for EBS volume type %q in region %s", volType, r.Region)
	}
	monthly := sp.Scale(size)
	comps := []simulate.PriceComponent{{Name: "storage", Unit: "GiB-month", Quantity: size, UnitPrice: sp, Monthly: monthly, PriceBasis: "on_demand"}}

	iops := a.Float("iops", 0)
	throughput := a.Float("throughput", 0)
	if volType == "gp3" {
		if iops > gp3BaselineIOPS {
			if ip, ok := pc.pricing.IOPSPrice(r.Region, "gp3"); ok {
				extra := iops - gp3BaselineIOPS
				c := ip.Scale(extra)
				monthly = monthly.MustAdd(c)
				comps = append(comps, simulate.PriceComponent{Name: "provisioned IOPS above 3,000 baseline", Unit: "IOPS-month", Quantity: extra, UnitPrice: ip, Monthly: c, PriceBasis: "on_demand"})
			}
		}
		if throughput > gp3BaselineThroughput {
			if tp, ok := pc.pricing.ThroughputPrice(r.Region, "gp3"); ok {
				extra := throughput - gp3BaselineThroughput
				c := tp.Scale(extra)
				monthly = monthly.MustAdd(c)
				comps = append(comps, simulate.PriceComponent{Name: "provisioned throughput above 125 MiBps baseline", Unit: "MiBps-month", Quantity: extra, UnitPrice: tp, Monthly: c, PriceBasis: "on_demand"})
			}
		}
	} else if iops > 0 {
		if ip, ok := pc.pricing.IOPSPrice(r.Region, volType); ok {
			c := ip.Scale(iops)
			monthly = monthly.MustAdd(c)
			comps = append(comps, simulate.PriceComponent{Name: "provisioned IOPS", Unit: "IOPS-month", Quantity: iops, UnitPrice: ip, Monthly: c, PriceBasis: "on_demand"})
		}
	}
	return priceOutcome{Monthly: monthly, Components: comps}
}

// priceS3Bucket is always usage-dependent: a bucket resource declares no size
// or request rate — those are entirely runtime facts — so this always prices
// from assumptions, never from a fixed attribute.
func priceS3Bucket(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	sp, ok := pc.pricing.StoragePrice(r.Region, "standard")
	if !ok {
		return unpricedOutcome("no S3 standard storage pricing for region %s", r.Region)
	}
	storageGB, storageOverridden := pc.resolveAssumption(r.Address, "s3_storage_gb", 100)
	getReq, getOverridden := pc.resolveAssumption(r.Address, "s3_get_requests_month", 100_000)
	putReq, putOverridden := pc.resolveAssumption(r.Address, "s3_put_requests_month", 20_000)

	monthly := sp.Scale(storageGB)
	comps := []simulate.PriceComponent{{Name: "stored data", Unit: "GB-month", Quantity: storageGB, UnitPrice: sp, Monthly: monthly, PriceBasis: "on_demand"}}
	if getPrice, ok := pc.pricing.ServicePrice(r.Region, "s3", "get_request_per_1k"); ok {
		c := getPrice.Scale(getReq / 1000)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "GET requests", Unit: "1K requests", Quantity: getReq / 1000, UnitPrice: getPrice, Monthly: c, PriceBasis: "on_demand"})
	}
	if putPrice, ok := pc.pricing.ServicePrice(r.Region, "s3", "put_request_per_1k"); ok {
		c := putPrice.Scale(putReq / 1000)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "PUT requests", Unit: "1K requests", Quantity: putReq / 1000, UnitPrice: putPrice, Monthly: c, PriceBasis: "on_demand"})
	}
	assumptions := []simulate.Assumption{
		usageAssumption("s3_storage_gb", "Stored data volume", storageGB, "GB", storageOverridden, "A bucket declares no size; this is entirely a runtime fact."),
		usageAssumption("s3_get_requests_month", "Monthly GET requests", getReq, "requests/month", getOverridden, ""),
		usageAssumption("s3_put_requests_month", "Monthly PUT requests", putReq, "requests/month", putOverridden, ""),
	}
	return priceOutcome{Monthly: monthly, UsageDependent: true, Components: comps, Assumptions: assumptions}
}

// priceLogGroup is usage-dependent (ingested and stored log volume are
// runtime facts). Whether the group's retention is finite or infinite is a
// separate CostRisk (see risks.go); pricing it here would double-count the
// concern as a fabricated cost multiplier instead of a flagged hazard.
func priceLogGroup(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	ingestPrice, ok := pc.pricing.ServicePrice(r.Region, "cloudwatch", "log_ingest_gb")
	if !ok {
		return unpricedOutcome("no cloudwatch log pricing for region %s", r.Region)
	}
	ingestGB, ingestOverridden := pc.resolveAssumption(r.Address, "log_ingest_gb_month", 10)
	monthly := ingestPrice.Scale(ingestGB)
	comps := []simulate.PriceComponent{{Name: "log ingestion", Unit: "GB", Quantity: ingestGB, UnitPrice: ingestPrice, Monthly: monthly, PriceBasis: "on_demand"}}
	assumptions := []simulate.Assumption{
		usageAssumption("log_ingest_gb_month", "Monthly log ingestion", ingestGB, "GB/month", ingestOverridden, ""),
	}
	if storagePrice, ok := pc.pricing.ServicePrice(r.Region, "cloudwatch", "log_storage_gb"); ok {
		storedGB, storedOverridden := pc.resolveAssumption(r.Address, "log_storage_gb", ingestGB)
		c := storagePrice.Scale(storedGB)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "log storage", Unit: "GB", Quantity: storedGB, UnitPrice: storagePrice, Monthly: c, PriceBasis: "on_demand"})
		assumptions = append(assumptions, usageAssumption("log_storage_gb", "Retained log volume", storedGB, "GB", storedOverridden,
			"Defaults to one month's ingestion; a longer retention_in_days retains proportionally more and should be overridden."))
	}
	_ = a
	return priceOutcome{Monthly: monthly, UsageDependent: true, Components: comps, Assumptions: assumptions}
}
