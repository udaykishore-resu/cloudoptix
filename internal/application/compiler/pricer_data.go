package compiler

import (
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// rdsStorageKey maps an RDS storage_type attribute onto the "rds_"-prefixed
// storage class the pricing package bridges RDS storage rates onto (see
// pricing.bridgeRDSStorage). gp2 is the RDS default when unset.
func rdsStorageKey(storageType string) string {
	switch strings.ToLower(strings.TrimSpace(storageType)) {
	case "io1", "io2":
		return "rds_io1"
	case "gp3":
		return "rds_gp3"
	default:
		return "rds_gp2"
	}
}

func priceRDSInstance(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	instanceClass := a.Str("instance_class", "")
	engine := a.Str("engine", "")
	if instanceClass == "" || engine == "" {
		return unpricedOutcome("%s has no instance_class/engine", r.Address)
	}
	multiAZ := a.Bool("multi_az", false)
	hourly, ok := pc.pricing.DatabasePrice(r.Region, instanceClass, engine, multiAZ)
	if !ok {
		return unpricedOutcome("no pricing data for RDS instance class %q engine %q in region %s", instanceClass, engine, r.Region)
	}
	monthly := monthlyFromHourly(hourly)
	comps := []simulate.PriceComponent{{Name: "instance hours", Unit: "hour", Quantity: core.HoursPerMonth, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"}}

	storageGB := a.Float("allocated_storage", 20)
	storageKey := rdsStorageKey(a.Str("storage_type", ""))
	if sp, ok := pc.pricing.StoragePrice(r.Region, storageKey); ok {
		c := sp.Scale(storageGB)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "allocated storage", Unit: "GiB-month", Quantity: storageGB, UnitPrice: sp, Monthly: c, PriceBasis: "on_demand"})
	}
	if iops := a.Float("iops", 0); iops > 0 && storageKey == "rds_io1" {
		if ip, ok := pc.pricing.IOPSPrice(r.Region, "rds_io1"); ok {
			c := ip.Scale(iops)
			monthly = monthly.MustAdd(c)
			comps = append(comps, simulate.PriceComponent{Name: "provisioned IOPS", Unit: "IOPS-month", Quantity: iops, UnitPrice: ip, Monthly: c, PriceBasis: "on_demand"})
		}
	}
	return priceOutcome{Monthly: monthly, Components: comps}
}

// priceRDSCluster prices only the cluster resource itself, which for Aurora
// carries no instances of its own (those are separate aws_rds_cluster_instance
// resources, priced by priceRDSClusterInstance) and whose own bill is Aurora's
// elastic storage and I/O — a per-GB-month and per-million-I/O pricing model
// the catalog has no entry for at all, and Aurora Serverless v1/v2's ACU-hour
// pricing has none either. Both are honestly Unpriced rather than approximated
// against an unrelated storage class.
func priceRDSCluster(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	engineMode := a.Str("engine_mode", "provisioned")
	if engineMode == "serverless" || a.Has("serverlessv2_scaling_configuration") {
		return unpricedOutcome("Aurora Serverless capacity-unit pricing is not in the pricing catalog")
	}
	return unpricedOutcome("Aurora cluster storage/IO pricing is not in the pricing catalog (instance cost is priced separately on each aws_rds_cluster_instance)")
}

func priceRDSClusterInstance(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	instanceClass := a.Str("instance_class", "")
	engine := a.Str("engine", "aurora-postgresql")
	if instanceClass == "" {
		return unpricedOutcome("%s has no instance_class", r.Address)
	}
	hourly, ok := pc.pricing.DatabasePrice(r.Region, instanceClass, engine, false)
	if !ok {
		return unpricedOutcome("no pricing data for Aurora instance class %q engine %q in region %s", instanceClass, engine, r.Region)
	}
	monthly := monthlyFromHourly(hourly)
	return priceOutcome{Monthly: monthly, Components: []simulate.PriceComponent{
		{Name: "instance hours", Unit: "hour", Quantity: core.HoursPerMonth, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"},
	}}
}

func cacheNodeCount(a Attrs, isReplicationGroup bool) float64 {
	if !isReplicationGroup {
		return a.Float("num_cache_nodes", 1)
	}
	if groups := a.Float("num_node_groups", 0); groups > 0 {
		replicas := a.Float("replicas_per_node_group", 0)
		return groups * (1 + replicas)
	}
	if n := a.Float("num_cache_clusters", 0); n > 0 {
		return n
	}
	return 2 // AWS's own default for a replication group: one primary, one replica
}

func priceElastiCacheCluster(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	return priceElastiCache(pc, r, a, false)
}

func priceElastiCacheReplicationGroup(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	return priceElastiCache(pc, r, a, true)
}

func priceElastiCache(pc *pricerCtx, r RawResource, a Attrs, isReplicationGroup bool) priceOutcome {
	nodeType := a.Str("node_type", "")
	if nodeType == "" {
		return unpricedOutcome("%s has no node_type", r.Address)
	}
	engine := a.Str("engine", "redis")
	nodes := cacheNodeCount(a, isReplicationGroup)
	hourly, ok := pc.pricing.CachePrice(r.Region, nodeType, engine)
	if !ok {
		return unpricedOutcome("no pricing data for ElastiCache node type %q engine %q in region %s", nodeType, engine, r.Region)
	}
	monthly := monthlyFromHourly(hourly).Scale(nodes)
	return priceOutcome{Monthly: monthly, Components: []simulate.PriceComponent{
		{Name: "node hours", Unit: "hour", Quantity: core.HoursPerMonth * nodes, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"},
	}}
}

// priceDynamoDBTable splits deterministic capacity cost (provisioned mode's
// RCU/WCU, a config value) from usage-dependent cost (on-demand read/write
// units, always usage-dependent because request volume cannot be known from
// the table's declaration; and stored data volume, usage-dependent in either
// billing mode because a table's size is a runtime fact).
func priceDynamoDBTable(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	storageGB, storageOverridden := pc.resolveAssumption(r.Address, "dynamodb_storage_gb", 10)
	storagePrice, storageKnown := pc.pricing.ServicePrice(r.Region, "dynamodb", "storage_gb_month")

	billingMode := a.Str("billing_mode", "PROVISIONED")
	var monthly core.Money
	var comps []simulate.PriceComponent
	var assumptions []simulate.Assumption
	usageDependent := false

	if billingMode == "PAY_PER_REQUEST" {
		reads, readsOverridden := pc.resolveAssumption(r.Address, "dynamodb_reads_month", 1_000_000)
		writes, writesOverridden := pc.resolveAssumption(r.Address, "dynamodb_writes_month", 500_000)
		readPrice, ok1 := pc.pricing.ServicePrice(r.Region, "dynamodb", "on_demand_read")
		writePrice, ok2 := pc.pricing.ServicePrice(r.Region, "dynamodb", "on_demand_write")
		if !ok1 || !ok2 {
			return unpricedOutcome("no dynamodb on-demand pricing for region %s", r.Region)
		}
		readCost := readPrice.Scale(reads / 1000)
		writeCost := writePrice.Scale(writes / 1000)
		monthly = readCost.MustAdd(writeCost)
		comps = append(comps,
			simulate.PriceComponent{Name: "on-demand reads", Unit: "1K reads", Quantity: reads / 1000, UnitPrice: readPrice, Monthly: readCost, PriceBasis: "on_demand"},
			simulate.PriceComponent{Name: "on-demand writes", Unit: "1K writes", Quantity: writes / 1000, UnitPrice: writePrice, Monthly: writeCost, PriceBasis: "on_demand"},
		)
		assumptions = append(assumptions,
			usageAssumption("dynamodb_reads_month", "Monthly read requests", reads, "reads/month", readsOverridden, "On-demand billing has no fixed capacity component; this is the whole read-side cost driver."),
			usageAssumption("dynamodb_writes_month", "Monthly write requests", writes, "writes/month", writesOverridden, ""),
		)
		usageDependent = true
	} else {
		rcu := a.Float("read_capacity", 5)
		wcu := a.Float("write_capacity", 5)
		rcuPrice, ok1 := pc.pricing.ServicePrice(r.Region, "dynamodb", "rcu_hour")
		wcuPrice, ok2 := pc.pricing.ServicePrice(r.Region, "dynamodb", "wcu_hour")
		if !ok1 || !ok2 {
			return unpricedOutcome("no dynamodb provisioned-capacity pricing for region %s", r.Region)
		}
		readCost := monthlyFromHourly(rcuPrice.Scale(rcu))
		writeCost := monthlyFromHourly(wcuPrice.Scale(wcu))
		monthly = readCost.MustAdd(writeCost)
		comps = append(comps,
			simulate.PriceComponent{Name: "provisioned read capacity", Unit: "RCU-hour", Quantity: rcu * core.HoursPerMonth, UnitPrice: rcuPrice, Monthly: readCost, PriceBasis: "on_demand"},
			simulate.PriceComponent{Name: "provisioned write capacity", Unit: "WCU-hour", Quantity: wcu * core.HoursPerMonth, UnitPrice: wcuPrice, Monthly: writeCost, PriceBasis: "on_demand"},
		)
	}

	if storageKnown {
		c := storagePrice.Scale(storageGB)
		monthly = monthly.MustAdd(c)
		comps = append(comps, simulate.PriceComponent{Name: "stored data", Unit: "GB-month", Quantity: storageGB, UnitPrice: storagePrice, Monthly: c, PriceBasis: "on_demand"})
		assumptions = append(assumptions, usageAssumption("dynamodb_storage_gb", "Stored data volume", storageGB, "GB", storageOverridden,
			"A table's stored data size is a runtime fact, not a declared configuration value, in either billing mode."))
		usageDependent = true
	}

	return priceOutcome{Monthly: monthly, UsageDependent: usageDependent, Components: comps, Assumptions: assumptions}
}
