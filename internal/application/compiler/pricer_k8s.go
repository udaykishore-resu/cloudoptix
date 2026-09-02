package compiler

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// priceK8sWorkload prices a Deployment, StatefulSet or DaemonSet by its
// summed container resource requests against the Fargate on-demand
// vCPU-hour/GB-hour rate — see ParseKubernetesManifest's doc comment for why
// that basis was chosen and what it does and does not claim about the actual
// node bill.
func priceK8sWorkload(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	vcpu := a.Float("vcpu_request", 0)
	memGiB := a.Float("memory_gib_request", 0)
	if vcpu <= 0 && memGiB <= 0 {
		return unpricedOutcome("%s: no container declares resources.requests", r.Address)
	}
	vcpuPrice, ok1 := pc.pricing.ServicePrice(r.Region, "fargate", "vcpu_hour")
	gbPrice, ok2 := pc.pricing.ServicePrice(r.Region, "fargate", "gb_hour")
	if !ok1 || !ok2 {
		return unpricedOutcome("no fargate-equivalent capacity pricing for region %s", r.Region)
	}

	kind := a.Str("kind", "")
	var assumptions []simulate.Assumption
	usageDependent := false
	replicas := a.Float("replicas", 1)

	if kind == "DaemonSet" {
		nodeCount, overridden := pc.resolveAssumption(r.Address, "k8s_cluster_node_count", 3)
		replicas = nodeCount
		assumptions = append(assumptions, usageAssumption("k8s_cluster_node_count", "Cluster node count", nodeCount, "nodes", overridden,
			"A DaemonSet runs one pod per matching node; the manifest names no cluster, so the node count is an assumption."))
		usageDependent = true
	}
	if minR := a.Float("hpa_min_replicas", -1); minR >= 0 {
		maxR := a.Float("hpa_max_replicas", minR)
		replicas = (minR + maxR) / 2
		assumptions = append(assumptions, simulate.Assumption{
			Key: "hpa_replica_estimate", Label: "Replica count used for pricing", Value: fmt.Sprintf("%g", replicas), Unit: "pods",
			Provenance: core.ProvenanceInferred,
			Note:       fmt.Sprintf("an autoscaler targets this workload with min=%g max=%g; priced at the midpoint — actual replica count, and therefore cost, varies with traffic within that range", minR, maxR),
		})
		usageDependent = true
	}
	if replicas <= 0 {
		replicas = 1
	}

	perPodHourly := vcpuPrice.Scale(vcpu).MustAdd(gbPrice.Scale(memGiB))
	monthly := monthlyFromHourly(perPodHourly).Scale(replicas)
	comps := []simulate.PriceComponent{
		{Name: "requested vCPU capacity", Unit: "vCPU-hour", Quantity: vcpu * core.HoursPerMonth * replicas, UnitPrice: vcpuPrice, Monthly: monthlyFromHourly(vcpuPrice.Scale(vcpu)).Scale(replicas), PriceBasis: "on_demand"},
		{Name: "requested memory capacity", Unit: "GB-hour", Quantity: memGiB * core.HoursPerMonth * replicas, UnitPrice: gbPrice, Monthly: monthlyFromHourly(gbPrice.Scale(memGiB)).Scale(replicas), PriceBasis: "on_demand"},
	}
	assumptions = append(assumptions, simulate.Assumption{
		Key: "k8s_pricing_basis", Label: "Node cost basis", Value: "fargate_equivalent",
		Provenance: core.ProvenanceInferred,
		Note:       "priced as Fargate-equivalent vCPU/GB-hour capacity; actual EKS/self-managed node cost depends on instance selection and bin-packing efficiency",
	})
	return priceOutcome{Monthly: monthly, UsageDependent: usageDependent, Components: comps, Assumptions: assumptions}
}
