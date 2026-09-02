package simulation

import (
	"fmt"
	"math"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// This file holds every scenarioHandler besides handleTrafficChange (which,
// being the specification's one explicitly-detailed scenario, lives beside
// its shared plumbing in counterfactual.go). Each handler follows the same
// shape: find the resources the scenario concerns, reject with a clear error
// when there is nothing in scope to model (never guess an effect against an
// empty scope), and otherwise return a scenarioOutcome built from real
// pricing-catalog lookups plus stated Assumptions for anything unobservable.

// --- platform change --------------------------------------------------

// handlePlatformChange answers "what if this compute moved to Fargate / to
// Lambda?" — a smaller, StateProjection-shaped sibling of the mutation
// engine's consolidate_to_ecs_fargate and containerize_to_serverless
// patterns. It reuses their capacity-summing approach but skips the
// eight-dimension scoring a Candidate needs, since a Counterfactual answers
// one direct question rather than ranking alternatives.
func handlePlatformChange(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	target := paramString(sc.Parameters, "target", "ecs_fargate")
	compute := inv.OfKinds(cloud.KindEC2Instance, cloud.KindEKSNodeGroup, cloud.KindECSService, cloud.KindK8sWorkload)
	if len(compute) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: platform_change found no EC2/EKS/ECS/Kubernetes compute in scope to move to %q", target)
	}

	overrides := map[core.ID]core.Money{}
	for _, r := range compute {
		overrides[r.ID] = core.ZeroUSD()
	}

	totalVCPU, totalMemGiB := 0.0, 0.0
	for _, r := range compute {
		u := units(r)
		vcpu, mem := r.Capacity.VCPU, r.Capacity.MemoryGiB
		if vcpu == 0 || mem == 0 {
			if spec, ok := pricing.InstanceSpec(r.InstanceType); ok {
				vcpu, mem = spec.VCPU, spec.MemoryGiB
			}
		}
		if vcpu == 0 {
			vcpu = 1 // conservative fallback so an unsized resource still contributes something rather than vanishing from the total
		}
		if mem == 0 {
			mem = 2
		}
		totalVCPU += vcpu * u
		totalMemGiB += mem * u
	}

	var added []addedComponent
	var confidence core.Confidence
	var performanceDelta string

	switch target {
	case "ecs_fargate", "fargate":
		vcpuPrice, ok1 := pricing.ServicePrice(region, "fargate", "vcpu_hour")
		gbPrice, ok2 := pricing.ServicePrice(region, "fargate", "gb_hour")
		if !ok1 || !ok2 {
			return scenarioOutcome{}, fmt.Errorf("simulation: no fargate pricing available for region %s", region)
		}
		fargateCost := monthlyFromHourly(vcpuPrice.Scale(totalVCPU).MustAdd(gbPrice.Scale(totalMemGiB)))
		added = append(added, addedComponent{
			name: fmt.Sprintf("ECS Fargate (%.1f vCPU / %.1f GiB)", totalVCPU, totalMemGiB),
			kind: "fargate", unit: "task", quantity: totalVCPU, monthlyCost: fargateCost,
		})
		confidence = 0.6
		performanceDelta = "comparable to the source once tasks are sized correctly; no bin-packing contention with other tenants' pods/instances"
	case "lambda", "serverless":
		const invocationsPerVCPU, avgDurationS, memGB = 2_000_000.0, 0.3, 0.5
		invocations := totalVCPU * invocationsPerVCPU
		lambdaCost := core.ZeroUSD()
		if gbSecondPrice, ok := pricing.ServicePrice(region, "lambda", "gb_second"); ok {
			lambdaCost = gbSecondPrice.Scale(memGB * avgDurationS * invocations)
			if reqPrice, ok := pricing.ServicePrice(region, "lambda", "request"); ok {
				lambdaCost = lambdaCost.MustAdd(reqPrice.Scale(invocations / 1000))
			}
		}
		added = append(added, addedComponent{
			name: "AWS Lambda (usage-modelled)", kind: "lambda", unit: "invocation", quantity: invocations, monthlyCost: lambdaCost,
		})
		if reqPrice, ok := pricing.ServicePrice(region, "api_gateway", "http_request"); ok {
			added = append(added, addedComponent{
				name: "API Gateway HTTP API", kind: "api_gateway", unit: "request", quantity: invocations, monthlyCost: reqPrice.Scale(invocations / 1000),
			})
		}
		confidence = 0.45
		performanceDelta = "cold starts add latency on infrequently invoked paths; a full rewrite is required, not a lift-and-shift"
	default:
		return scenarioOutcome{}, fmt.Errorf("simulation: platform_change target %q is not supported (expected \"ecs_fargate\" or \"lambda\")", target)
	}

	proposed := buildProposedState(fmt.Sprintf("compute on %s", target), inv, overrides, added)
	return scenarioOutcome{
		Question:         fmt.Sprintf("What would it cost to move this compute to %s?", target),
		Proposed:         proposed,
		PerformanceDelta: performanceDelta,
		ReliabilityDelta: "the target platform manages the underlying capacity; no nodes or clusters to patch or drain",
		Risk:             core.RiskMedium,
		Confidence:       confidence,
		Assumptions: []simulate.Assumption{{
			Key: "platform_change_target", Label: "Target platform", Value: target, Provenance: core.ProvenanceConfirmed,
			Note: "the scenario's own input parameter",
		}},
		Caveats: []string{"this is a cost projection only; it does not account for the engineering effort or timeline of the underlying platform migration"},
		Narrative: fmt.Sprintf(
			"the %d compute resources in scope are removed and replaced with a single %s cost sized from their summed vCPU/memory allocation — a capacity-based estimate, not a measured one",
			len(compute), target),
	}, nil
}

// --- database change ----------------------------------------------------

// handleDatabaseChange re-prices RDS instances against a target engine —
// typically an Aurora-compatible target via auroraEquivalentEngine (shared
// with the mutation engine's managed_data_migration pattern), or an
// explicit target_engine parameter for any other engine the catalog prices.
func handleDatabaseChange(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	rds := inv.OfKind(cloud.KindRDSInstance)
	if len(rds) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: database_change found no RDS instance in scope")
	}
	targetEngine := paramString(sc.Parameters, "target_engine", "")

	overrides := map[core.ID]core.Money{}
	var resolvedEngine string
	for _, db := range rds {
		engine := targetEngine
		if engine == "" {
			engine = auroraEquivalentEngine(db.Engine)
		}
		if engine == "" {
			continue
		}
		hourly, ok := pricing.DatabasePrice(region, db.InstanceType, engine, db.AttrBool("multi_az", false))
		if !ok {
			continue
		}
		overrides[db.ID] = monthlyFromHourly(hourly)
		resolvedEngine = engine
	}
	if len(overrides) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: no priced target engine available for the RDS instances in scope (target_engine=%q)", targetEngine)
	}

	proposed := buildProposedState(fmt.Sprintf("database on %s", resolvedEngine), inv, overrides, nil)
	return scenarioOutcome{
		Question:         fmt.Sprintf("What would it cost to move this database to %s?", resolvedEngine),
		Proposed:         proposed,
		PerformanceDelta: "engine-dependent: an Aurora target typically improves write throughput and failover time over provisioned RDS storage",
		ReliabilityDelta: "an Aurora target adds automated storage repair and sub-minute failover; a non-Aurora engine change carries its own reliability profile that this model does not characterize",
		Risk:             core.RiskMedium,
		Confidence:       0.5,
		Assumptions: []simulate.Assumption{{
			Key: "database_change_target_engine", Label: "Target database engine", Value: resolvedEngine, Provenance: core.ProvenanceConfirmed,
			Note: "resolved from the target_engine parameter or, when absent, the Aurora-compatible mapping for the source engine",
		}},
		Caveats: []string{"assumes the same instance class carries over to the target engine; a real migration may need a different class for comparable performance"},
		Narrative: fmt.Sprintf(
			"repriced %d of %d RDS instances in scope against %s at their current instance class; instances whose engine has no priced target are left at their current cost", len(overrides), len(rds), resolvedEngine),
	}, nil
}

// --- add cache -----------------------------------------------------------

// handleAddCache models a Redis cache's effect on database load: the
// database's currently-attributed cost is reduced by the fraction of it
// assumed attributable to reads, discounted further by the assumed cache
// hit rate — a database that spends readShare of its cost serving reads,
// hitRate of which a cache now absorbs, sheds readShare*hitRate of its cost.
// Both readShare and hitRate are stated Assumptions because neither is
// observable from the resource model alone.
func handleAddCache(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	dbs := inv.OfKinds(cloud.KindRDSInstance, cloud.KindRDSCluster)
	if len(dbs) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: add_cache found no database in scope to front with a cache")
	}
	hitRate := clamp01(paramFloat(sc.Parameters, "hit_rate", 0.6))
	readShare := clamp01(paramFloat(sc.Parameters, "db_read_share", 0.6))

	overrides := map[core.ID]core.Money{}
	for _, db := range dbs {
		reduction := db.MonthlyCost.Scale(readShare * hitRate)
		overrides[db.ID] = db.MonthlyCost.MustSub(reduction)
	}

	const nodeType, engine, nodeCount = "cache.r6g.large", "redis", 2.0
	var added []addedComponent
	if hourly, ok := pricing.CachePrice(region, nodeType, engine); ok {
		added = append(added, addedComponent{
			name: fmt.Sprintf("ElastiCache Redis (%s x%d)", nodeType, int(nodeCount)),
			kind: "elasticache", unit: "node", quantity: nodeCount,
			monthlyCost: monthlyFromHourly(hourly).Scale(nodeCount),
		})
	}

	proposed := buildProposedState("database + cache", inv, overrides, added)
	return scenarioOutcome{
		Question:         "What happens to database load and cost if a cache is added in front of it?",
		Proposed:         proposed,
		PerformanceDelta: fmt.Sprintf("read paths hitting the cache (an assumed %.0f%% of reads, %.0f%% of the time) return in sub-millisecond time instead of a database round trip", readShare*100, hitRate*100),
		ReliabilityDelta: "a cache outage should degrade to the database rather than fail requests outright; that fallback path must be built, it is not automatic",
		Risk:             core.RiskLow,
		Confidence:       0.5,
		Assumptions: []simulate.Assumption{
			{Key: "cache_hit_rate", Label: "Assumed cache hit rate", Value: fmt.Sprintf("%g", hitRate), Unit: "ratio", Provenance: core.ProvenanceInferred, Note: "not measured; override once real hit-rate telemetry exists"},
			{Key: "db_read_share", Label: "Assumed share of database cost attributable to reads", Value: fmt.Sprintf("%g", readShare), Unit: "ratio", Provenance: core.ProvenanceInferred, Note: "the resource model does not separately track read vs. write cost; this is a stated approximation"},
		},
		Narrative: fmt.Sprintf(
			"database cost falls by readShare x hitRate = %.0f%% of its current run-rate (%.0f%% of reads assumed cacheable, %.0f%% of those assumed to hit), offset by the new cache cluster's own always-on cost", readShare*hitRate*100, readShare*100, hitRate*100),
	}, nil
}

// --- remove NAT ------------------------------------------------------------

func handleRemoveNAT(inv *cloud.Inventory, _ ports.PricingCatalog, _ core.Region, _ simulate.Scenario) (scenarioOutcome, error) {
	nats := inv.OfKind(cloud.KindNATGateway)
	if len(nats) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: remove_nat found no NAT gateway in scope")
	}
	overrides := map[core.ID]core.Money{}
	for _, n := range nats {
		overrides[n.ID] = core.ZeroUSD()
	}
	proposed := buildProposedState("NAT removed", inv, overrides, nil)
	return scenarioOutcome{
		Question:         "What would removing NAT gateway(s) save, and what breaks?",
		Proposed:         proposed,
		PerformanceDelta: "no change for AWS-service traffic already reachable another way; internet-bound traffic with no remaining path fails outright rather than degrading",
		ReliabilityDelta: "any private-subnet resource whose only route to the internet or to a non-endpoint-served AWS API was this NAT gateway loses connectivity",
		Risk:             core.RiskHigh,
		Confidence:       0.7,
		Caveats: []string{
			"this scenario prices NAT removal in isolation; it does not verify a replacement path (VPC endpoints, a public subnet) exists — pair it with add_vpc_endpoint before acting on it",
		},
		Narrative: fmt.Sprintf("removes the full attributed cost of %d NAT gateway(s); nothing else in the topology changes, so any traffic that genuinely depended on NAT for internet egress has no replacement path in this projection", len(nats)),
	}, nil
}

// --- add VPC endpoint ------------------------------------------------------

// handleAddVPCEndpoint offsets a share of NAT gateways' cost (the traffic
// assumed diverted to the new endpoint) and adds the endpoint's own cost —
// free for a Gateway endpoint (S3/DynamoDB), hourly plus per-GB for an
// Interface endpoint. The 30% default share mirrors the same
// assumedS3ShareOfNATTraffic figure the compiler's missing-gateway-endpoint
// opportunity detector uses, for consistency between the two engines'
// answers to a related question.
func handleAddVPCEndpoint(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	kindParam := paramString(sc.Parameters, "endpoint_type", "gateway")
	share := clamp01(paramFloat(sc.Parameters, "nat_traffic_offloaded", 0.30))

	nats := inv.OfKind(cloud.KindNATGateway)
	overrides := map[core.ID]core.Money{}
	for _, n := range nats {
		overrides[n.ID] = n.MonthlyCost.MustSub(n.MonthlyCost.Scale(share))
	}

	var added []addedComponent
	switch kindParam {
	case "gateway":
		added = append(added, addedComponent{name: "S3/DynamoDB Gateway Endpoint", kind: "vpc_endpoint", unit: "endpoint", quantity: 1, monthlyCost: core.ZeroUSD()})
	case "interface":
		count := paramFloat(sc.Parameters, "interface_count", 3)
		hourly, ok := pricing.ServicePrice(region, "vpc_endpoint", "hour")
		if !ok {
			return scenarioOutcome{}, fmt.Errorf("simulation: no interface VPC endpoint pricing available for region %s", region)
		}
		added = append(added, addedComponent{
			name: fmt.Sprintf("%d Interface VPC Endpoint(s)", int(count)), kind: "vpc_endpoint", unit: "endpoint", quantity: count,
			monthlyCost: monthlyFromHourly(hourly).Scale(count),
		})
	default:
		return scenarioOutcome{}, fmt.Errorf("simulation: add_vpc_endpoint endpoint_type %q is not supported (expected \"gateway\" or \"interface\")", kindParam)
	}
	if len(nats) == 0 && len(added) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: add_vpc_endpoint found nothing to add or offset in scope")
	}

	proposed := buildProposedState("with VPC endpoint", inv, overrides, added)
	return scenarioOutcome{
		Question:         fmt.Sprintf("What would adding a %s VPC endpoint save on NAT-routed traffic?", kindParam),
		Proposed:         proposed,
		PerformanceDelta: "endpoint-routed traffic to AWS services has lower latency than a NAT hop",
		ReliabilityDelta: "removes NAT as a bottleneck for the diverted traffic; does not affect NAT's role for anything not diverted",
		SecurityDelta:    "diverted traffic never traverses the public internet",
		Risk:             core.RiskLow,
		Confidence:       0.5,
		Assumptions: []simulate.Assumption{{
			Key: "nat_traffic_offloaded", Label: "Share of NAT-routed traffic assumed diverted to the endpoint", Value: fmt.Sprintf("%g", share), Unit: "ratio",
			Provenance: core.ProvenanceInferred, Note: "not measured from VPC Flow Logs; override with an observed per-service traffic share for a tighter estimate",
		}},
		Narrative: fmt.Sprintf("offsets %.0f%% of NAT gateway cost (the assumed share of traffic moving to the endpoint) against the new endpoint's own cost", share*100),
	}, nil
}

// --- Spot adoption -----------------------------------------------------

func handleSpotAdoption(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	fraction := clamp01(paramFloat(sc.Parameters, "spot_fraction", 0.5))
	onDemand := inv.Filter(func(r cloud.Resource) bool {
		return (r.Kind == cloud.KindEC2Instance || r.Kind == cloud.KindEKSNodeGroup) &&
			r.Purchase == cloud.PurchaseOnDemand && r.InstanceType != ""
	})
	if len(onDemand) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: spot_adoption found no on-demand EC2/node-group capacity in scope")
	}

	overrides := map[core.ID]core.Money{}
	for _, r := range onDemand {
		spotHourly, ok := pricing.SpotPrice(region, r.InstanceType)
		if !ok {
			continue
		}
		u := units(r)
		odPortion := r.MonthlyCost.Scale(1 - fraction)
		spotPortion := monthlyFromHourly(spotHourly).Scale(u * fraction)
		overrides[r.ID] = odPortion.MustAdd(spotPortion)
	}
	if len(overrides) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: no spot pricing available for the instance types in scope")
	}

	proposed := buildProposedState(fmt.Sprintf("%.0f%% Spot", fraction*100), inv, overrides, nil)
	return scenarioOutcome{
		Question:         fmt.Sprintf("What would shifting %.0f%% of on-demand capacity to Spot save?", fraction*100),
		Proposed:         proposed,
		PerformanceDelta: "no change to instance type or capacity; identical performance while running",
		ReliabilityDelta: "the Spot-covered fraction can be reclaimed with two minutes' notice; only interruption-tolerant work belongs there",
		Risk:             core.RiskMedium,
		Confidence:       0.55,
		Assumptions: []simulate.Assumption{{
			Key: "spot_fraction", Label: "Fraction of capacity shifted to Spot", Value: fmt.Sprintf("%g", fraction), Unit: "ratio", Provenance: core.ProvenanceConfirmed,
			Note: "the scenario's own input parameter",
		}},
		Caveats:   []string{"assumes Spot interruption handling (checkpointing, graceful drain) is already in place; adopting Spot without it risks dropped work, not just reclaimed capacity"},
		Narrative: fmt.Sprintf("blends %.0f%% on-demand with %.0f%% Spot pricing across %d instance(s)/node-group(s) with available Spot pricing", (1-fraction)*100, fraction*100, len(overrides)),
	}, nil
}

// --- region change -----------------------------------------------------

// handleRegionChange re-prices every resource kind the pricing catalog has a
// direct lookup for (compute, database, cache, EBS storage) against a target
// region, and is explicit — via a Caveat, never a silent omission — about
// every other kind whose cost is carried forward unchanged.
func handleRegionChange(inv *cloud.Inventory, pricing ports.PricingCatalog, _ core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	target := core.Region(paramString(sc.Parameters, "target_region", ""))
	if target == "" {
		return scenarioOutcome{}, fmt.Errorf("simulation: region_change requires a \"target_region\" parameter")
	}

	overrides := map[core.ID]core.Money{}
	unrepriced := 0
	for _, r := range inv.All() {
		switch r.Kind {
		case cloud.KindEC2Instance:
			if hourly, ok := pricing.InstancePrice(target, r.InstanceType, string(r.Purchase)); ok {
				overrides[r.ID] = monthlyFromHourly(hourly).Scale(units(r))
				continue
			}
		case cloud.KindRDSInstance:
			if hourly, ok := pricing.DatabasePrice(target, r.InstanceType, r.Engine, r.AttrBool("multi_az", false)); ok {
				overrides[r.ID] = monthlyFromHourly(hourly)
				continue
			}
		case cloud.KindElastiCache:
			if hourly, ok := pricing.CachePrice(target, r.InstanceType, r.Engine); ok {
				overrides[r.ID] = monthlyFromHourly(hourly).Scale(units(r))
				continue
			}
		case cloud.KindEBSVolume:
			if perGiB, ok := pricing.StoragePrice(target, r.InstanceType); ok {
				overrides[r.ID] = perGiB.Scale(r.Capacity.StorageGiB)
				continue
			}
		default:
			continue // not one of the kinds this handler re-prices; not counted as "unrepriced" below since it was never in scope for repricing
		}
		unrepriced++
	}
	if len(overrides) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: no resource in scope could be repriced for target region %s", target)
	}

	proposed := buildProposedState(fmt.Sprintf("region: %s", target), inv, overrides, nil)
	var caveats []string
	if unrepriced > 0 {
		caveats = append(caveats, fmt.Sprintf(
			"%d resource(s) of a kind this scenario does not yet reprice (compute, database, cache and EBS storage are covered; messaging, serverless and network services are not) kept their current-region cost, understating the true regional delta", unrepriced))
	}
	return scenarioOutcome{
		Question:         fmt.Sprintf("What would this footprint cost in %s instead of its current region?", target),
		Proposed:         proposed,
		PerformanceDelta: "cross-region moves add latency for any client or dependency outside the new region; not modelled here",
		ReliabilityDelta: "no reliability change from the move itself; regional service availability differs by AWS region and is not modelled here",
		Risk:             core.RiskMedium,
		Confidence:       0.45,
		Assumptions: []simulate.Assumption{{
			Key: "region_change_target", Label: "Target region", Value: string(target), Provenance: core.ProvenanceConfirmed, Note: "the scenario's own input parameter",
		}},
		Caveats: caveats,
		Narrative: fmt.Sprintf(
			"repriced %d resource(s) against %s catalog rates for their existing instance type/class; data-transfer cost of the migration itself is not included", len(overrides), target),
	}, nil
}

// --- commitment purchase ---------------------------------------------------

func handleCommitmentPurchase(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	term := paramString(sc.Parameters, "term", "1yr")
	payment := paramString(sc.Parameters, "payment", "savings_plan_no_upfront")
	onDemand := inv.Filter(func(r cloud.Resource) bool {
		return (r.Kind == cloud.KindEC2Instance || r.Kind == cloud.KindEKSNodeGroup) &&
			r.Purchase == cloud.PurchaseOnDemand && r.InstanceType != ""
	})
	if len(onDemand) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: commitment_purchase found no on-demand EC2/node-group capacity in scope")
	}

	overrides := map[core.ID]core.Money{}
	for _, r := range onDemand {
		hourly, ok := pricing.CommitmentPrice(region, r.InstanceType, term, payment)
		if !ok {
			continue
		}
		overrides[r.ID] = monthlyFromHourly(hourly).Scale(units(r))
	}
	if len(overrides) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: no commitment pricing available for term=%q payment=%q on the instance types in scope", term, payment)
	}

	proposed := buildProposedState(fmt.Sprintf("%s commitment", term), inv, overrides, nil)
	return scenarioOutcome{
		Question:         fmt.Sprintf("What would a %s %s commitment save on current on-demand capacity?", term, payment),
		Proposed:         proposed,
		PerformanceDelta: "no change; identical instance types and capacity",
		ReliabilityDelta: "no change; a purchasing decision, not an infrastructure change",
		Risk:             core.RiskMedium,
		Confidence:       0.65,
		Assumptions: []simulate.Assumption{
			{Key: "commitment_term", Label: "Commitment term", Value: term, Provenance: core.ProvenanceConfirmed, Note: "the scenario's own input parameter"},
			{Key: "commitment_payment", Label: "Commitment payment option", Value: payment, Provenance: core.ProvenanceConfirmed, Note: "the scenario's own input parameter"},
		},
		Caveats:   []string{"a commitment is a real financial obligation for its full term regardless of whether the underlying capacity keeps running"},
		Narrative: fmt.Sprintf("repriced %d on-demand instance(s)/node-group(s) at the %s %s rate", len(overrides), term, payment),
	}, nil
}

// --- storage class change ---------------------------------------------------

func handleStorageClassChange(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	target := paramString(sc.Parameters, "target_class", "gp3")
	volumes := inv.OfKind(cloud.KindEBSVolume)
	if len(volumes) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: storage_class_change found no EBS volume in scope")
	}
	targetPrice, ok := pricing.StoragePrice(region, target)
	if !ok {
		return scenarioOutcome{}, fmt.Errorf("simulation: no pricing for storage class %q in region %s", target, region)
	}

	overrides := map[core.ID]core.Money{}
	for _, v := range volumes {
		if strings.EqualFold(v.InstanceType, target) {
			continue
		}
		if v.Capacity.StorageGiB <= 0 {
			continue
		}
		overrides[v.ID] = targetPrice.Scale(v.Capacity.StorageGiB)
	}
	if len(overrides) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: every EBS volume in scope is already %q, or none carries a known size", target)
	}

	proposed := buildProposedState(fmt.Sprintf("volumes on %s", target), inv, overrides, nil)
	return scenarioOutcome{
		Question:         fmt.Sprintf("What would moving EBS volumes to %s save?", target),
		Proposed:         proposed,
		PerformanceDelta: "gp3 raises baseline IOPS/throughput over gp2 at no extra charge; a colder class trades some latency for cost",
		ReliabilityDelta: "no durability change; both classes carry the same underlying redundancy",
		Risk:             core.RiskLow,
		Confidence:       0.7,
		Assumptions: []simulate.Assumption{{
			Key: "storage_class_change_target", Label: "Target storage class", Value: target, Provenance: core.ProvenanceConfirmed, Note: "the scenario's own input parameter",
		}},
		Narrative: fmt.Sprintf("repriced %d of %d EBS volume(s) in scope at the %s per-GiB rate; a volume already on that class or missing size data is left unchanged", len(overrides), len(volumes), target),
	}, nil
}

// --- replica change ---------------------------------------------------------

// handleReplicaChange prices the addition or removal of N read replicas of
// the primary database in scope. It adds/removes a component priced at the
// primary's own instance-class rate rather than hunting for separately
// tagged replica resources in the inventory, because Resource carries no
// primary-vs-replica discriminator of its own — only a ReadReplicas count on
// the primary's Capacity. That is stated as a Caveat rather than silently
// assumed away.
func handleReplicaChange(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	delta := paramFloat(sc.Parameters, "delta", 1)
	if delta == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: replica_change requires a non-zero \"delta\" parameter")
	}
	dbs := inv.OfKind(cloud.KindRDSInstance)
	if len(dbs) == 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: replica_change found no RDS instance in scope")
	}
	db := dbs[0]
	hourly, ok := pricing.DatabasePrice(region, db.InstanceType, db.Engine, false)
	if !ok {
		return scenarioOutcome{}, fmt.Errorf("simulation: no pricing available for %s (%s) to size a replica from", db.InstanceType, db.Engine)
	}
	perReplica := monthlyFromHourly(hourly)
	currentReplicas := db.Capacity.ReadReplicas
	newReplicas := currentReplicas + int(delta)
	if newReplicas < 0 {
		newReplicas = 0
	}
	appliedDelta := newReplicas - currentReplicas

	added := []addedComponent{{
		name: fmt.Sprintf("%+d read replica(s) of %s", appliedDelta, db.DisplayName()),
		kind: "rds", unit: "replica", quantity: float64(appliedDelta), monthlyCost: perReplica.Scale(float64(appliedDelta)),
	}}
	proposed := buildProposedState(fmt.Sprintf("%d read replicas", newReplicas), inv, nil, added)

	risk := core.RiskLow
	if appliedDelta < 0 {
		risk = core.RiskMedium // removing read capacity can push read load back onto the primary
	}
	return scenarioOutcome{
		Question: fmt.Sprintf("What would changing %s's read replica count by %+d cost?", db.DisplayName(), int(delta)),
		Proposed: proposed,
		PerformanceDelta: func() string {
			if appliedDelta > 0 {
				return "additional read capacity available for read-heavy query paths"
			}
			return "read traffic previously served by removed replicas falls back to the primary or to remaining replicas"
		}(),
		ReliabilityDelta: "replica count affects read-path failover options; the primary's own availability is unchanged",
		Risk:             risk,
		Confidence:       0.6,
		Assumptions: []simulate.Assumption{{
			Key: "replica_change_delta", Label: "Read replica count change", Value: fmt.Sprintf("%d", int(delta)), Provenance: core.ProvenanceConfirmed, Note: "the scenario's own input parameter, clamped so the replica count cannot go negative",
		}},
		Caveats:   []string{"priced at the primary instance's own class; the resource model has no separate record of each replica's individual instance class, so a fleet with heterogeneous replica sizing is only approximated"},
		Narrative: fmt.Sprintf("%d replica(s) at the primary's %s rate; replica count moves from %d to %d", int(math.Abs(float64(appliedDelta))), db.InstanceType, currentReplicas, newReplicas),
	}, nil
}

// --- custom ------------------------------------------------------------

// handleCustom is the counterfactual engine's own instance of the
// compiler's central discipline: an input CloudOptix cannot price is
// answered "no model exists", never a fabricated number. A custom scenario
// by definition carries no fixed parameter schema, so there is no honest way
// to project a cost effect from it; the proposed state is returned identical
// to current, at zero confidence, with a Caveat explaining why.
func handleCustom(inv *cloud.Inventory, _ ports.PricingCatalog, _ core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	proposed := buildProposedState("proposed (unmodelled)", inv, nil, nil)
	label := sc.Label
	if label == "" {
		label = "custom scenario"
	}
	return scenarioOutcome{
		Question:   fmt.Sprintf("What is the cost effect of %q?", label),
		Proposed:   proposed,
		Risk:       core.RiskNone,
		Confidence: 0,
		Caveats: []string{
			"custom scenarios carry no fixed parameter schema, so CloudOptix has no cost model to apply to one — this mirrors the Cost Compiler's unpriced discipline: an unmodelled input is reported as unmodelled, never guessed at",
			"price this change instead through the Cost Compiler (an IaC diff) or through one of the named scenario types this engine does support",
		},
		Narrative: "no built-in model exists for an arbitrary custom scenario; the proposed state is returned unchanged from current rather than fabricating a delta",
	}, nil
}
