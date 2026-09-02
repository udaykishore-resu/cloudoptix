package simulation

import (
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func sumCost(resources []cloud.Resource) core.Money {
	total := core.ZeroUSD()
	for _, r := range resources {
		total = total.MustAdd(r.MonthlyCost)
	}
	return total
}

func monthlyFromHourly(hourly core.Money) core.Money { return hourly.Scale(core.HoursPerMonth) }

func units(r cloud.Resource) float64 {
	n := r.Capacity.DesiredCount
	if n == 0 {
		n = r.Capacity.InstanceCount
	}
	if n == 0 {
		n = 1
	}
	return float64(n)
}

// --- Pattern 1: containerize-to-serverless -------------------------------

func buildContainerizeToServerless(inv *cloud.Inventory, _ *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult {
	const name, pattern = "Containerize to Serverless", "containerize_to_serverless"
	compute := inv.OfKinds(cloud.KindEKSNodeGroup, cloud.KindECSService, cloud.KindK8sWorkload)
	if len(compute) == 0 {
		return blockedPattern(name, pattern, "no EKS/ECS/Kubernetes compute found in scope to serverless-ify")
	}
	for _, r := range compute {
		if strings.EqualFold(r.Attr("workload_kind", ""), "StatefulSet") {
			return blockedPattern(name, pattern, fmt.Sprintf(
				"%s is a StatefulSet; Lambda's serverless model has no persistent local state, so a stateful workload cannot move to it without a separate data-migration plan",
				r.DisplayName()))
		}
	}

	var changes []simulate.ComponentChange
	for _, r := range compute {
		changes = append(changes, simulate.ComponentChange{
			Action: "remove", From: string(r.Kind), Component: r.DisplayName(),
			MonthlyDelta: r.MonthlyCost.Scale(-1), Rationale: "replaced by Lambda functions",
		})
	}
	for _, c := range inv.OfKinds(cloud.KindEKSCluster, cloud.KindECSCluster) {
		changes = append(changes, simulate.ComponentChange{
			Action: "remove", Component: c.DisplayName(), MonthlyDelta: c.MonthlyCost.Scale(-1),
			Rationale: "no cluster control plane needed once compute moves to Lambda",
		})
	}

	totalVCPU := 0.0
	for _, r := range compute {
		if r.Capacity.VCPU > 0 {
			totalVCPU += r.Capacity.VCPU * units(r)
		} else {
			totalVCPU += units(r) // conservative 1-vCPU-equivalent fallback per instance
		}
	}
	const invocationsPerVCPU = 2_000_000.0
	invocations := totalVCPU * invocationsPerVCPU
	const avgDurationS, memGB = 0.3, 0.5

	lambdaCost := core.ZeroUSD()
	if gbSecondPrice, ok := pricing.ServicePrice(region, "lambda", "gb_second"); ok {
		lambdaCost = gbSecondPrice.Scale(memGB * avgDurationS * invocations)
		if reqPrice, ok := pricing.ServicePrice(region, "lambda", "request"); ok {
			lambdaCost = lambdaCost.MustAdd(reqPrice.Scale(invocations / 1000))
		}
	}
	changes = append(changes, simulate.ComponentChange{
		Action: "add", To: "AWS Lambda", Component: "serverless compute", MonthlyDelta: lambdaCost,
		Rationale: "replaces containerized compute; billed per invocation instead of per provisioned instance-hour",
	})
	if reqPrice, ok := pricing.ServicePrice(region, "api_gateway", "http_request"); ok {
		apigwCost := reqPrice.Scale(invocations / 1000)
		changes = append(changes, simulate.ComponentChange{
			Action: "add", To: "API Gateway HTTP API", Component: "request routing", MonthlyDelta: apigwCost,
			Rationale: "replaces the load balancer/ingress in front of the containerized services",
		})
	}

	if dbs := inv.OfKind(cloud.KindRDSInstance); len(dbs) > 0 {
		db := dbs[0]
		changes = append(changes, simulate.ComponentChange{
			Action: "remove", From: db.InstanceType, Component: db.DisplayName(),
			MonthlyDelta: db.MonthlyCost.Scale(-1), Rationale: "replaced by DynamoDB",
		})
		readPrice, _ := pricing.ServicePrice(region, "dynamodb", "on_demand_read")
		writePrice, _ := pricing.ServicePrice(region, "dynamodb", "on_demand_write")
		storagePrice, _ := pricing.ServicePrice(region, "dynamodb", "storage_gb_month")
		const reads, writes = 2_000_000.0, 500_000.0
		storageGB := db.Capacity.StorageGiB
		if storageGB == 0 {
			storageGB = 20
		}
		dynamoCost := readPrice.Scale(reads / 1000).MustAdd(writePrice.Scale(writes / 1000)).MustAdd(storagePrice.Scale(storageGB))
		changes = append(changes, simulate.ComponentChange{
			Action: "add", To: "DynamoDB", Component: "managed NoSQL datastore", MonthlyDelta: dynamoCost,
			Rationale: "on-demand table sized to a comparable read/write assumption, not measured access patterns",
		})
	}

	assumptions := []simulate.Assumption{{
		Key: "lambda_invocations_per_vcpu_month", Label: "Estimated Lambda invocations per removed vCPU",
		Value: fmt.Sprintf("%g", invocationsPerVCPU), Unit: "invocations/month", Provenance: core.ProvenanceInferred,
		Note: "derived from the removed compute's vCPU allocation, not measured request volume; override with real traffic figures before committing to this estimate",
	}}

	return patternResult{
		Name: name, Pattern: pattern, Applicable: true,
		Summary: "Replace EKS/ECS/Kubernetes compute with Lambda behind API Gateway, and migrate the primary relational datastore to DynamoDB.",
		Changes: changes, Assumptions: assumptions,
		Scores: []simulate.DimensionScore{
			dimScore(simulate.DimCost, 70, 0, "usage-based billing typically undercuts always-on containers for spiky or low-average-utilization traffic", 0.55),
			dimScore(simulate.DimPerformance, 55, 0, "cold starts add latency on infrequently invoked paths; high-throughput paths perform comparably", 0.6),
			dimScore(simulate.DimReliability, 65, 0, "no cluster or node group to keep healthy; Lambda's own availability is AWS-managed", 0.6),
			dimScore(simulate.DimScalability, 85, 0, "scales per-request with no capacity planning", 0.7),
			dimScore(simulate.DimSecurity, 60, 0, "smaller OS-patching surface; IAM-per-function replaces broader pod-level access", 0.55),
			dimScore(simulate.DimOperability, 70, 0, "no cluster upgrades or node patching; function packaging and cold-start tuning become the new operational surface", 0.55),
			dimScore(simulate.DimMigrationEffort, 25, 0, "a full rewrite of the compute and data layer; the highest-effort pattern in this catalog", 0.7),
			dimScore(simulate.DimRisk, 40, 0, "a request-path rewrite plus a data-store migration carries real regression risk", 0.5),
		},
		Risks: []string{
			"cold-start latency on infrequently invoked paths",
			"DynamoDB's access patterns require query redesign away from SQL joins",
		},
		MigrationSteps: []string{
			"extract each service's request handlers into independently deployable functions",
			"stand up API Gateway routes mirroring the current ingress",
			"dual-write to DynamoDB during a transition window before cutting over reads",
			"decommission the cluster only after a full traffic-shifted validation period",
		},
		Confidence: 0.5,
	}
}

// --- Pattern 2: consolidate-to-ECS-Fargate -------------------------------

func buildConsolidateToFargate(inv *cloud.Inventory, _ *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult {
	const name, pattern = "Consolidate to ECS Fargate", "consolidate_to_ecs_fargate"
	compute := inv.OfKinds(cloud.KindEKSNodeGroup, cloud.KindEC2Instance)
	if len(compute) == 0 {
		return blockedPattern(name, pattern, "no EKS node group or EC2 compute found in scope to consolidate onto Fargate")
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
		totalVCPU += vcpu * u
		totalMemGiB += mem * u
	}
	if totalVCPU == 0 || totalMemGiB == 0 {
		return blockedPattern(name, pattern, "compute in scope carries no capacity data (vCPU/memory) or recognizable instance type to size Fargate tasks from")
	}
	vcpuPrice, ok1 := pricing.ServicePrice(region, "fargate", "vcpu_hour")
	gbPrice, ok2 := pricing.ServicePrice(region, "fargate", "gb_hour")
	if !ok1 || !ok2 {
		return blockedPattern(name, pattern, fmt.Sprintf("no fargate pricing available for region %s", region))
	}

	var changes []simulate.ComponentChange
	for _, r := range compute {
		changes = append(changes, simulate.ComponentChange{
			Action: "remove", From: r.InstanceType, Component: r.DisplayName(), MonthlyDelta: r.MonthlyCost.Scale(-1),
			Rationale: "replaced by Fargate tasks; no nodes to patch or bin-pack",
		})
	}
	for _, c := range inv.OfKind(cloud.KindEKSCluster) {
		changes = append(changes, simulate.ComponentChange{
			Action: "remove", Component: c.DisplayName(), MonthlyDelta: c.MonthlyCost.Scale(-1),
			Rationale: "ECS clusters carry no control-plane hourly charge",
		})
	}
	fargateCost := monthlyFromHourly(vcpuPrice.Scale(totalVCPU).MustAdd(gbPrice.Scale(totalMemGiB)))
	changes = append(changes, simulate.ComponentChange{
		Action: "add", To: "ECS Fargate", Component: "consolidated compute", MonthlyDelta: fargateCost,
		Rationale: fmt.Sprintf("%.1f vCPU / %.1f GiB of requested capacity carried at Fargate on-demand rates", totalVCPU, totalMemGiB),
	})

	return patternResult{
		Name: name, Pattern: pattern, Applicable: true,
		Summary: "Move EKS-node-group or bare EC2 compute onto ECS Fargate, eliminating node management and the EKS control-plane fee.",
		Changes: changes,
		Scores: []simulate.DimensionScore{
			dimScore(simulate.DimCost, 60, 0, "no EKS control-plane fee and no over-provisioned node headroom, offset by Fargate's per-task pricing premium over raw EC2", 0.6),
			dimScore(simulate.DimPerformance, 60, 0, "comparable to the source once tasks are sized correctly; no bin-packing contention", 0.6),
			dimScore(simulate.DimReliability, 65, 0, "AWS manages the underlying capacity; no node failures to detect and drain", 0.6),
			dimScore(simulate.DimScalability, 70, 0, "task-level autoscaling without cluster capacity planning", 0.6),
			dimScore(simulate.DimSecurity, 65, 0, "no shared node OS between tasks; smaller patchable surface than self-managed nodes", 0.55),
			dimScore(simulate.DimOperability, 75, 0, "no node group upgrades, no Kubernetes control plane to operate", 0.6),
			dimScore(simulate.DimMigrationEffort, 55, 0, "a platform change, not a rewrite: container images generally carry over unchanged", 0.65),
			dimScore(simulate.DimRisk, 65, 0, "lower-risk than a serverless rewrite; the deployable unit does not change", 0.6),
		},
		MigrationSteps: []string{
			"convert Kubernetes manifests or launch configurations to ECS task definitions",
			"validate task-level IAM roles replace node-level instance profile permissions",
			"shift traffic service by service behind the existing load balancer",
		},
		Confidence: 0.6,
	}
}

// --- Pattern 3: managed-data migration -----------------------------------

func auroraEquivalentEngine(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", "postgresql":
		return "aurora-postgresql"
	case "mysql":
		return "aurora-mysql"
	default:
		return ""
	}
}

func buildManagedDataMigration(inv *cloud.Inventory, _ *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult {
	const name, pattern = "Managed Data Migration", "managed_data_migration"
	rds := inv.OfKind(cloud.KindRDSInstance)
	if len(rds) == 0 {
		return blockedPattern(name, pattern, "no RDS instance found in scope to migrate to a managed alternative")
	}
	var changes []simulate.ComponentChange
	for _, db := range rds {
		auroraEngine := auroraEquivalentEngine(db.Engine)
		if auroraEngine == "" {
			continue
		}
		auroraHourly, ok := pricing.DatabasePrice(region, db.InstanceType, auroraEngine, false)
		if !ok {
			continue
		}
		auroraCost := monthlyFromHourly(auroraHourly)
		changes = append(changes, simulate.ComponentChange{
			Action: "replace", From: db.Engine, To: auroraEngine, Component: db.DisplayName(),
			MonthlyDelta: auroraCost.MustSub(db.MonthlyCost),
			Rationale:    "Aurora adds storage auto-scaling, sub-minute failover and cheap read-replica elasticity over self-managed provisioned RDS; its per-instance rate itself is not always lower",
		})
	}
	if len(changes) == 0 {
		return blockedPattern(name, pattern, "no RDS engine in scope has an Aurora-compatible target (postgres/mysql only) or priced instance class")
	}
	return patternResult{
		Name: name, Pattern: pattern, Applicable: true,
		Summary: "Migrate self-managed-topology RDS instances to Aurora for elastic storage, faster failover and cheap read replicas.",
		Changes: changes,
		Scores: []simulate.DimensionScore{
			dimScore(simulate.DimCost, 40, 0, "Aurora's engine multiplier runs above equivalent RDS in this catalog; the saving is operational, not on the sticker price", 0.55),
			dimScore(simulate.DimPerformance, 65, 0, "Aurora's storage layer typically outperforms RDS's provisioned EBS for write-heavy and failover-sensitive workloads", 0.5),
			dimScore(simulate.DimReliability, 80, 0, "sub-minute automated failover and six-way storage replication across AZs", 0.65),
			dimScore(simulate.DimScalability, 75, 0, "storage grows automatically; read replicas add in minutes, not a maintenance-window resize", 0.6),
			dimScore(simulate.DimSecurity, 55, 0, "unchanged access model; same IAM/VPC posture as RDS", 0.5),
			dimScore(simulate.DimOperability, 70, 0, "no manual storage provisioning or failover runbooks", 0.55),
			dimScore(simulate.DimMigrationEffort, 60, 0, "a snapshot-and-restore or DMS-based migration; the engine wire protocol is unchanged", 0.6),
			dimScore(simulate.DimRisk, 55, 0, "a data-tier migration always carries cutover risk; a read-replica-first approach limits it", 0.5),
		},
		MigrationSteps: []string{
			"create an Aurora read replica of the source RDS instance via native replication",
			"validate application query performance against the Aurora replica under shadow load",
			"promote the Aurora replica and cut connection strings over during a maintenance window",
		},
		Confidence: 0.55,
	}
}

// --- Pattern 4: network-cost-elimination ---------------------------------

func buildNetworkCostElimination(inv *cloud.Inventory, _ *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult {
	const name, pattern = "Network Cost Elimination", "network_cost_elimination"
	nats := inv.OfKind(cloud.KindNATGateway)
	if len(nats) == 0 {
		return blockedPattern(name, pattern, "no NAT gateway found in scope to eliminate")
	}
	var changes []simulate.ComponentChange
	for _, n := range nats {
		changes = append(changes, simulate.ComponentChange{
			Action: "remove", Component: n.DisplayName(), MonthlyDelta: n.MonthlyCost.Scale(-1),
			Rationale: "replaced by VPC endpoints for AWS-service traffic",
		})
	}
	hasS3Endpoint := false
	for _, e := range inv.OfKind(cloud.KindVPCEndpoint) {
		if strings.Contains(strings.ToLower(e.Attr("service", "")), "s3") {
			hasS3Endpoint = true
		}
	}
	if !hasS3Endpoint {
		changes = append(changes, simulate.ComponentChange{
			Action: "add", To: "S3/DynamoDB Gateway Endpoints", Component: "AWS-service routing", MonthlyDelta: core.ZeroUSD(),
			Rationale: "gateway endpoints are free and remove S3/DynamoDB traffic from NAT entirely",
		})
	}
	const interfaceEndpointCount = 3.0
	if hourly, ok := pricing.ServicePrice(region, "vpc_endpoint", "hour"); ok {
		ifaceCost := monthlyFromHourly(hourly).Scale(interfaceEndpointCount)
		changes = append(changes, simulate.ComponentChange{
			Action: "add", To: "Interface VPC Endpoints", MonthlyDelta: ifaceCost,
			Component: fmt.Sprintf("%d interface endpoints (ECR, CloudWatch Logs, Secrets Manager)", int(interfaceEndpointCount)),
			Rationale: "replaces NAT-routed calls to these AWS services with private, in-VPC routing",
		})
	}

	return patternResult{
		Name: name, Pattern: pattern, Applicable: true,
		Summary: "Remove NAT gateways in favor of VPC endpoints for AWS-service traffic, eliminating both the hourly NAT fee and its per-GB processing charge for that traffic.",
		Changes: changes,
		Scores: []simulate.DimensionScore{
			dimScore(simulate.DimCost, 80, 0, "NAT's per-GB processing charge, usually its largest cost component, is fully eliminated for AWS-service traffic", 0.6),
			dimScore(simulate.DimPerformance, 60, 0, "endpoint-routed traffic to AWS services has lower latency than a NAT hop", 0.5),
			dimScore(simulate.DimReliability, 55, 0, "removes a NAT gateway as a potential bottleneck, but genuine internet egress needs a fallback path", 0.45),
			dimScore(simulate.DimScalability, 55, 0, "endpoints scale with no throughput ceiling comparable to NAT's per-gateway bandwidth limit", 0.5),
			dimScore(simulate.DimSecurity, 70, 0, "AWS-service traffic never traverses the public internet", 0.6),
			dimScore(simulate.DimOperability, 60, 0, "one-time endpoint provisioning; no ongoing NAT capacity management", 0.55),
			dimScore(simulate.DimMigrationEffort, 75, 0, "additive infrastructure change; no application code changes required", 0.7),
			dimScore(simulate.DimRisk, 45, 0, "any workload with genuine public-internet dependencies breaks if NAT is removed before that path is replaced", 0.5),
		},
		Risks: []string{
			"any traffic to a non-AWS destination (a third-party API, the open internet) still needs a NAT gateway or a public subnet; verify no such dependency exists before removing NAT entirely",
		},
		MigrationSteps: []string{
			"add gateway endpoints for S3 and DynamoDB (free, low risk)",
			"add interface endpoints for every AWS service still reached through NAT",
			"audit VPC Flow Logs for remaining NAT traffic and confirm nothing is bound for the open internet",
			"remove the NAT gateway(s) only after a full traffic audit shows zero remaining dependency",
		},
		Confidence: 0.55,
	}
}

// --- Pattern 5: commitment-and-Spot restructuring ------------------------

func buildCommitmentAndSpot(inv *cloud.Inventory, _ *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult {
	const name, pattern = "Commitment & Spot Restructuring", "commitment_and_spot"
	const commitmentFraction, spotFraction = 0.6, 0.4

	onDemand := inv.Filter(func(r cloud.Resource) bool {
		return (r.Kind == cloud.KindEC2Instance || r.Kind == cloud.KindEKSNodeGroup) &&
			r.Purchase == cloud.PurchaseOnDemand && r.InstanceType != ""
	})
	if len(onDemand) == 0 {
		return blockedPattern(name, pattern, "no on-demand EC2/node-group capacity found in scope to restructure")
	}
	var changes []simulate.ComponentChange
	for _, r := range onDemand {
		commitHourly, ok1 := pricing.CommitmentPrice(region, r.InstanceType, "1yr", "savings_plan_no_upfront")
		spotHourly, ok2 := pricing.SpotPrice(region, r.InstanceType)
		if !ok1 && !ok2 {
			continue
		}
		u := units(r)
		blended := core.ZeroUSD()
		if ok1 {
			blended = blended.MustAdd(monthlyFromHourly(commitHourly).Scale(u * commitmentFraction))
		} else {
			blended = blended.MustAdd(r.MonthlyCost.Scale(commitmentFraction))
		}
		if ok2 {
			blended = blended.MustAdd(monthlyFromHourly(spotHourly).Scale(u * spotFraction))
		} else {
			blended = blended.MustAdd(r.MonthlyCost.Scale(spotFraction))
		}
		changes = append(changes, simulate.ComponentChange{
			Action: "replace", From: "on_demand", To: "60% 1yr Savings Plan / 40% Spot", Component: r.DisplayName(),
			MonthlyDelta: blended.MustSub(r.MonthlyCost),
			Rationale:    "a committed baseline covers steady-state capacity; Spot covers interruption-tolerant headroom",
		})
	}
	if len(changes) == 0 {
		return blockedPattern(name, pattern, "no commitment or spot pricing available for the instance types in scope")
	}
	return patternResult{
		Name: name, Pattern: pattern, Applicable: true,
		Summary: "Blend a 1-year Savings Plan baseline with Spot capacity for interruption-tolerant headroom, replacing pure on-demand pricing.",
		Changes: changes,
		Scores: []simulate.DimensionScore{
			dimScore(simulate.DimCost, 85, 0, "Savings Plans and Spot both discount materially against on-demand for steady, predictable capacity", 0.65),
			dimScore(simulate.DimPerformance, 50, 0, "identical instance types and capacity; no performance change", 0.7),
			dimScore(simulate.DimReliability, 40, 0, "the Spot-covered fraction can be reclaimed with two minutes' notice; only interruption-tolerant workloads belong there", 0.5),
			dimScore(simulate.DimScalability, 50, 0, "no change to how capacity scales, only how it is purchased", 0.6),
			dimScore(simulate.DimSecurity, 50, 0, "no change to the security posture", 0.6),
			dimScore(simulate.DimOperability, 45, 0, "Spot interruption handling (checkpointing, graceful drain) becomes an operational requirement", 0.5),
			dimScore(simulate.DimMigrationEffort, 80, 0, "a purchasing change; no application or infrastructure code changes required", 0.7),
			dimScore(simulate.DimRisk, 55, 0, "a 1-year commitment is a real financial obligation even if the workload shrinks; the Spot fraction adds interruption risk", 0.5),
		},
		Risks: []string{
			"a 1-year Savings Plan commitment does not refund if the workload's baseline capacity shrinks",
			"Spot capacity can be reclaimed with two minutes' notice; only stateless or checkpointed work belongs on it",
		},
		MigrationSteps: []string{
			"identify the steady-state minimum capacity to cover with a Savings Plan",
			"add Spot interruption handling (SIGTERM handling, checkpointing, or a stateless retry path) before shifting any fraction to Spot",
			"purchase the commitment only after the Spot-tolerant fraction is validated in production",
		},
		Confidence: 0.6,
	}
}

// --- Pattern 6: Graviton migration ---------------------------------------

var gravitonFamilyMap = map[string]string{
	"c4": "c7g", "c5": "c7g", "c6i": "c7g",
	"m4": "m6g", "m5": "m6g", "m5a": "m6g", "m6i": "m6g", "m7i": "m6g",
	"r4": "r6g", "r5": "r6g", "r6i": "r6g", "r7i": "r6g",
	"t2": "t4g", "t3": "t4g", "t3a": "t4g",
}

func gravitonEquivalent(instanceType string) (string, bool) {
	parts := strings.SplitN(instanceType, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	target, ok := gravitonFamilyMap[parts[0]]
	if !ok {
		return "", false
	}
	return target + "." + parts[1], true
}

func buildGravitonMigration(inv *cloud.Inventory, _ *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult {
	const name, pattern = "Graviton Migration", "graviton_migration"
	candidates := inv.Filter(func(r cloud.Resource) bool {
		if r.Kind != cloud.KindEC2Instance && r.Kind != cloud.KindEKSNodeGroup {
			return false
		}
		spec, ok := pricing.InstanceSpec(r.InstanceType)
		return ok && spec.Architecture != "arm64"
	})
	if len(candidates) == 0 {
		return blockedPattern(name, pattern, "no x86 EC2/node-group instance type found in scope with a Graviton family mapping")
	}
	var changes []simulate.ComponentChange
	for _, r := range candidates {
		target, ok := gravitonEquivalent(r.InstanceType)
		if !ok {
			continue
		}
		newHourly, ok := pricing.InstancePrice(region, target, "")
		if !ok {
			continue
		}
		newCost := monthlyFromHourly(newHourly).Scale(units(r))
		changes = append(changes, simulate.ComponentChange{
			Action: "replace", From: r.InstanceType, To: target, Component: r.DisplayName(),
			MonthlyDelta: newCost.MustSub(r.MonthlyCost),
			Rationale:    "Graviton instances typically price 10-20% below their x86 equivalent at comparable or better performance-per-dollar",
		})
	}
	if len(changes) == 0 {
		return blockedPattern(name, pattern, "candidates were found but no priced Graviton equivalent exists in the catalog for this region")
	}
	return patternResult{
		Name: name, Pattern: pattern, Applicable: true,
		Summary: "Move x86 EC2/node-group capacity to its Graviton (arm64) equivalent instance family.",
		Changes: changes,
		Scores: []simulate.DimensionScore{
			dimScore(simulate.DimCost, 75, 0, "Graviton's list price runs below the equivalent x86 family at the same size", 0.65),
			dimScore(simulate.DimPerformance, 60, 0, "generally equal or better throughput per dollar; single-threaded workloads should be validated", 0.5),
			dimScore(simulate.DimReliability, 50, 0, "no reliability change; same managed EC2 platform", 0.6),
			dimScore(simulate.DimScalability, 50, 0, "no scalability change", 0.6),
			dimScore(simulate.DimSecurity, 50, 0, "no security posture change", 0.6),
			dimScore(simulate.DimOperability, 50, 0, "a second CPU architecture in the build/CI matrix adds a small ongoing operational cost", 0.5),
			dimScore(simulate.DimMigrationEffort, 55, 0, "requires an arm64 build of every container image and any compiled dependency", 0.55),
			dimScore(simulate.DimRisk, 55, 0, "architecture-specific bugs (native extensions, JIT behavior) surface rarely but do occur", 0.5),
		},
		Risks: []string{"any compiled dependency, native extension or third-party agent without an arm64 build blocks this migration"},
		MigrationSteps: []string{
			"add an arm64 build target to the CI pipeline and produce multi-arch container images",
			"canary a fraction of capacity on Graviton and compare error rates and latency",
			"roll forward once the canary shows parity",
		},
		Confidence: 0.6,
	}
}

// --- Pattern 7: caching-layer introduction --------------------------------

func buildCachingLayerIntroduction(inv *cloud.Inventory, _ *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult {
	const name, pattern = "Caching Layer Introduction", "caching_layer_introduction"
	dbs := inv.OfKinds(cloud.KindRDSInstance, cloud.KindRDSCluster)
	if len(dbs) == 0 {
		return blockedPattern(name, pattern, "no database found in scope to front with a cache")
	}
	if existing := inv.OfKind(cloud.KindElastiCache); len(existing) > 0 {
		return blockedPattern(name, pattern, "a cache layer (ElastiCache) is already present in scope")
	}
	const nodeType, engine = "cache.r6g.large", "redis"
	hourly, ok := pricing.CachePrice(region, nodeType, engine)
	if !ok {
		return blockedPattern(name, pattern, fmt.Sprintf("no ElastiCache pricing available for %s in region %s", nodeType, region))
	}
	const nodeCount = 2.0 // primary + replica for availability
	cacheCost := monthlyFromHourly(hourly).Scale(nodeCount)
	changes := []simulate.ComponentChange{{
		Action: "add", To: fmt.Sprintf("ElastiCache Redis (%s x%d)", nodeType, int(nodeCount)),
		Component: "caching layer", MonthlyDelta: cacheCost,
		Rationale: "absorbs read traffic from the primary datastore; the database's own compute cost is unchanged here — see the add_cache counterfactual scenario for a hit-rate-adjusted database load projection",
	}}
	return patternResult{
		Name: name, Pattern: pattern, Applicable: true,
		Summary: "Introduce a Redis caching layer in front of the primary datastore to absorb read traffic and reduce database load and tail latency.",
		Changes: changes,
		Scores: []simulate.DimensionScore{
			dimScore(simulate.DimCost, 30, 0, "adds a new always-on component with no automatic database-side saving at compile time", 0.6),
			dimScore(simulate.DimPerformance, 80, 0, "cache hits return in sub-millisecond time versus a database round trip", 0.55),
			dimScore(simulate.DimReliability, 45, 0, "a new stateful component to operate; a cache outage should degrade to the database, not fail requests", 0.45),
			dimScore(simulate.DimScalability, 65, 0, "read scalability improves without adding database read replicas", 0.55),
			dimScore(simulate.DimSecurity, 50, 0, "no material change; the cache sits inside the existing VPC boundary", 0.55),
			dimScore(simulate.DimOperability, 45, 0, "cache invalidation and warm-up strategy become an ongoing concern", 0.5),
			dimScore(simulate.DimMigrationEffort, 60, 0, "additive; requires application-level cache-aside logic on the hot read paths", 0.55),
			dimScore(simulate.DimRisk, 55, 0, "stale-cache bugs are a real, if manageable, class of production incident", 0.5),
		},
		MigrationSteps: []string{
			"identify the highest-read-volume, cache-tolerant queries",
			"add cache-aside logic (read-through on miss, write-invalidate on update)",
			"monitor hit rate and database load before and after rollout",
		},
		Confidence: 0.55,
	}
}

// --- Pattern 8: storage tiering -------------------------------------------

func buildStorageTiering(inv *cloud.Inventory, _ *cloud.Topology, pricing ports.PricingCatalog, region core.Region) patternResult {
	const name, pattern = "Storage Tiering", "storage_tiering"
	volumes := inv.Filter(func(r cloud.Resource) bool {
		return r.Kind == cloud.KindEBSVolume && strings.EqualFold(r.InstanceType, "gp2")
	})
	buckets := inv.OfKind(cloud.KindS3Bucket)
	if len(volumes) == 0 && len(buckets) == 0 {
		return blockedPattern(name, pattern, "no gp2 EBS volume or S3 bucket found in scope to retier")
	}

	var changes []simulate.ComponentChange
	if gp2, ok1 := pricing.StoragePrice(region, "gp2"); ok1 {
		if gp3, ok2 := pricing.StoragePrice(region, "gp3"); ok2 {
			for _, v := range volumes {
				size := v.Capacity.StorageGiB
				if size <= 0 {
					continue
				}
				newCost := gp3.Scale(size)
				changes = append(changes, simulate.ComponentChange{
					Action: "replace", From: "gp2", To: "gp3", Component: v.DisplayName(),
					MonthlyDelta: newCost.MustSub(v.MonthlyCost),
					Rationale:    "gp3 is cheaper per GiB and includes a 3,000 IOPS / 125 MiBps baseline gp2 does not",
				})
			}
			_ = gp2
		}
	}
	if std, ok1 := pricing.StoragePrice(region, "standard"); ok1 {
		if ia, ok2 := pricing.StoragePrice(region, "standard_ia"); ok2 {
			for _, b := range buckets {
				sizeGB := 500.0 // default assumption; a bucket carries no size attribute of its own
				if !std.IsZero() && !b.MonthlyCost.IsZero() {
					sizeGB = b.MonthlyCost.Ratio(std)
				}
				newCost := ia.Scale(sizeGB)
				oldCost := std.Scale(sizeGB)
				changes = append(changes, simulate.ComponentChange{
					Action: "replace", From: "STANDARD", To: "STANDARD_IA (lifecycle rule, objects >30d old)", Component: b.DisplayName(),
					MonthlyDelta: newCost.MustSub(oldCost),
					Rationale:    "infrequently-accessed data costs materially less in a cooler storage class; a lifecycle rule automates the transition",
				})
			}
		}
	}
	if len(changes) == 0 {
		return blockedPattern(name, pattern, "no priced storage-class transition available for the resources in scope")
	}
	return patternResult{
		Name: name, Pattern: pattern, Applicable: true,
		Summary: "Move gp2 EBS volumes to gp3 and add S3 lifecycle rules tiering infrequently-accessed objects to a cooler storage class.",
		Changes: changes,
		Scores: []simulate.DimensionScore{
			dimScore(simulate.DimCost, 75, 0, "both transitions reduce per-GiB storage cost with no capacity change", 0.65),
			dimScore(simulate.DimPerformance, 55, 0, "gp3 raises baseline IOPS/throughput over gp2; S3 IA has a small first-byte latency penalty on cold reads", 0.55),
			dimScore(simulate.DimReliability, 50, 0, "no durability change; both classes carry the same underlying redundancy", 0.6),
			dimScore(simulate.DimScalability, 50, 0, "no scalability change", 0.6),
			dimScore(simulate.DimSecurity, 50, 0, "no security posture change", 0.6),
			dimScore(simulate.DimOperability, 60, 0, "a one-time volume-type change and a set-and-forget lifecycle rule", 0.6),
			dimScore(simulate.DimMigrationEffort, 85, 0, "gp2->gp3 is a live, zero-downtime modify-volume call; S3 lifecycle rules require no application change", 0.7),
			dimScore(simulate.DimRisk, 75, 0, "low risk; both transitions are reversible and widely used", 0.65),
		},
		MigrationSteps: []string{
			"modify each gp2 volume to gp3 in place (no downtime, no data movement)",
			"add an S3 lifecycle rule transitioning objects to Standard-IA after 30 days of inactivity",
		},
		Confidence: 0.6,
	}
}

func blockedPattern(name, pattern, blocker string) patternResult {
	return patternResult{Name: name, Pattern: pattern, Applicable: false, Blocker: blocker}
}
