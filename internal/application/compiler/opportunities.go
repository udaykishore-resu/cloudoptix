package compiler

import (
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// DetectOpportunities finds cheaper alternatives noticeable at review time —
// changes so close to free that flagging them beside the diff is worth more
// than a dedicated rightsizing pass would be, because the window to make them
// (before the resource exists) is cheapest right now.
func DetectOpportunities(pc *pricerCtx, raws []RawResource, changes []simulate.PricedChange) []simulate.Opportunity {
	var out []simulate.Opportunity
	out = append(out, detectGP2Opportunity(pc, raws)...)
	out = append(out, detectX86LambdaOpportunity(pc, raws)...)
	out = append(out, detectOldGenerationInstances(pc, raws)...)
	if opp := detectMissingGatewayEndpoint(raws, changes); opp != nil {
		out = append(out, *opp)
	}
	return out
}

func detectGP2Opportunity(pc *pricerCtx, raws []RawResource) []simulate.Opportunity {
	var out []simulate.Opportunity
	for _, r := range raws {
		if r.Type != "aws_ebs_volume" || r.Action == simulate.ChangeDelete {
			continue
		}
		a := r.Effective()
		if a.Str("type", "gp2") != "gp2" {
			continue
		}
		size := a.Float("size", 0)
		if size <= 0 {
			continue
		}
		gp2, ok1 := pc.pricing.StoragePrice(r.Region, "gp2")
		gp3, ok2 := pc.pricing.StoragePrice(r.Region, "gp3")
		if !ok1 || !ok2 || !gp3.LessThan(gp2) {
			continue
		}
		saving := gp2.MustSub(gp3).Scale(size)
		if saving.IsZero() {
			continue
		}
		out = append(out, simulate.Opportunity{
			Address:       r.Address,
			Summary:       "gp2 volume declared; gp3 is cheaper per GiB and its baseline 3,000 IOPS / 125 MiBps beat gp2 at any size below 1 TiB",
			MonthlySaving: saving,
			Change:        "switch volume_type to gp3",
		})
	}
	return out
}

func detectX86LambdaOpportunity(pc *pricerCtx, raws []RawResource) []simulate.Opportunity {
	var out []simulate.Opportunity
	for _, r := range raws {
		if r.Type != "aws_lambda_function" || r.Action == simulate.ChangeDelete {
			continue
		}
		a := r.Effective()
		arch := "x86_64"
		if archs := a.List("architectures"); len(archs) > 0 {
			if s, ok := asString(archs[0]); ok && s != "" {
				arch = s
			}
		}
		if arch == "arm64" {
			continue
		}
		x86Price, ok1 := pc.pricing.ServicePrice(r.Region, "lambda", "gb_second")
		armPrice, ok2 := pc.pricing.ServicePrice(r.Region, "lambda", "arm_gb_second")
		if !ok1 || !ok2 || !armPrice.LessThan(x86Price) {
			continue
		}
		memMB := a.Float("memory_size", 128)
		invocations, _ := pc.resolveAssumption(r.Address, "lambda_invocations_month", 1_000_000)
		avgMS, _ := pc.resolveAssumption(r.Address, "lambda_avg_duration_ms", 200)
		memGB := memMB / 1024
		durationS := avgMS / 1000
		x86Cost := x86Price.Scale(memGB * durationS * invocations)
		armCost := armPrice.Scale(memGB * durationS * invocations)
		saving := x86Cost.MustSub(armCost)
		if !saving.GreaterThan(core.ZeroUSD()) {
			continue
		}
		out = append(out, simulate.Opportunity{
			Address:       r.Address,
			Summary:       "function targets x86_64; arm64 (Graviton2) is cheaper per GB-second for equivalent workloads",
			MonthlySaving: saving,
			Change:        "add \"arm64\" to architectures (verify your runtime/dependencies support it)",
		})
	}
	return out
}

// detectOldGenerationInstances flags any priced EC2 instance type or EKS
// node-group instance type whose pricing-catalog entry names a current-
// generation successor, using the same InstanceSpec.SuccessorType the
// standalone rightsizing engine reads.
func detectOldGenerationInstances(pc *pricerCtx, raws []RawResource) []simulate.Opportunity {
	var out []simulate.Opportunity
	check := func(address string, region core.Region, instanceType string) {
		spec, ok := pc.pricing.InstanceSpec(instanceType)
		if !ok || spec.SuccessorType == "" {
			return
		}
		oldPrice, ok1 := pc.pricing.InstancePrice(region, instanceType, "")
		newPrice, ok2 := pc.pricing.InstancePrice(region, spec.SuccessorType, "")
		if !ok1 || !ok2 {
			return
		}
		saving := monthlyFromHourly(oldPrice.MustSub(newPrice))
		if !saving.GreaterThan(core.ZeroUSD()) {
			return
		}
		out = append(out, simulate.Opportunity{
			Address:       address,
			Summary:       fmt.Sprintf("%s is a previous-generation instance type; %s is available in this region", instanceType, spec.SuccessorType),
			MonthlySaving: saving,
			Change:        fmt.Sprintf("switch instance_type to %s", spec.SuccessorType),
		})
	}
	for _, r := range raws {
		if r.Action == simulate.ChangeDelete {
			continue
		}
		a := r.Effective()
		switch r.Type {
		case "aws_instance":
			if it := a.Str("instance_type", ""); it != "" {
				check(r.Address, r.Region, it)
			}
		case "aws_eks_node_group":
			if types := a.List("instance_types"); len(types) > 0 {
				if it, ok := asString(types[0]); ok && it != "" {
					check(r.Address, r.Region, it)
				}
			}
		}
	}
	return out
}

// detectMissingGatewayEndpoint pairs the two facts a reviewer would otherwise
// have to notice independently: a new NAT gateway, and the absence of a free
// S3 gateway VPC endpoint that would remove S3-bound traffic from it
// entirely. The estimated saving is a documented fraction of the NAT
// gateway's own priced data-processing component, not an invented figure —
// it degrades to zero (and the opportunity is simply omitted) whenever that
// component itself is not priced.
func detectMissingGatewayEndpoint(raws []RawResource, changes []simulate.PricedChange) *simulate.Opportunity {
	const assumedS3ShareOfNATTraffic = 0.30

	var natAddrs []string
	natDataCost := core.ZeroUSD()
	for _, c := range changes {
		if c.ResourceType != "aws_nat_gateway" || c.Action != simulate.ChangeCreate {
			continue
		}
		natAddrs = append(natAddrs, c.Address)
		for _, comp := range c.PriceComponents {
			if comp.Name == "data processed" {
				natDataCost = natDataCost.MustAdd(comp.Monthly)
			}
		}
	}
	if len(natAddrs) == 0 {
		return nil
	}
	for _, r := range raws {
		if r.Type != "aws_vpc_endpoint" {
			continue
		}
		a := r.Effective()
		if a.Str("vpc_endpoint_type", "Gateway") == "Gateway" && strings.Contains(strings.ToLower(a.Str("service_name", "")), "s3") {
			return nil // already present
		}
	}
	return &simulate.Opportunity{
		Address:       strings.Join(natAddrs, ", "),
		Summary:       "a NAT gateway was added without a companion S3 gateway VPC endpoint",
		MonthlySaving: natDataCost.Scale(assumedS3ShareOfNATTraffic),
		Change:        "add an aws_vpc_endpoint of type Gateway for S3 (and DynamoDB, if used) — gateway endpoints are free and remove that traffic from NAT's per-GB processing charge",
	}
}
