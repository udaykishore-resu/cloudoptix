package compiler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// DetectRisks finds the structural cost hazards a headline dollar delta
// hides: hazards that are true regardless of whether the total went up or
// down, because they are about how the cost is shaped, not how large it is.
func DetectRisks(raws []RawResource, changes []simulate.PricedChange, req ports.CompileRequest) []simulate.CostRisk {
	var risks []simulate.CostRisk
	risks = append(risks, detectNATFanout(changes)...)
	risks = append(risks, detectProvisionedIOPSNoEvidence(raws)...)
	risks = append(risks, detectCrossRegionReplica(raws)...)
	risks = append(risks, detectFanoutExpansion(changes)...)
	risks = append(risks, detectUntaggedResources(raws, changes)...)
	risks = append(risks, detectInfiniteLogRetention(raws)...)
	risks = append(risks, detectPublicEgressHeavy(changes)...)
	sort.SliceStable(risks, func(i, j int) bool {
		if risks[i].Severity.Order() != risks[j].Severity.Order() {
			return risks[i].Severity.Order() > risks[j].Severity.Order()
		}
		return risks[i].Code < risks[j].Code
	})
	return risks
}

// detectNATFanout flags multiple NAT gateways created in one change set — the
// textbook "one per AZ" pattern, which is often intentional (AZ-isolated
// egress) but is also the single most common source of an unexpectedly large
// networking bill, so it is always worth a reviewer's explicit sign-off.
func detectNATFanout(changes []simulate.PricedChange) []simulate.CostRisk {
	var nats []simulate.PricedChange
	for _, c := range changes {
		if c.ResourceType == "aws_nat_gateway" && (c.Action == simulate.ChangeCreate || c.Action == simulate.ChangeReplace) {
			nats = append(nats, c)
		}
	}
	if len(nats) < 2 {
		return nil
	}
	total := core.ZeroUSD()
	addrs := make([]string, 0, len(nats))
	for _, n := range nats {
		total = total.MustAdd(n.AfterMonthly)
		addrs = append(addrs, n.Address)
	}
	sev := core.SeverityMedium
	if len(nats) >= 3 {
		sev = core.SeverityHigh
	}
	return []simulate.CostRisk{{
		Code: "nat_gateway_fanout", Severity: sev,
		Summary:       fmt.Sprintf("%d NAT gateways added (likely one per availability zone)", len(nats)),
		Detail:        strings.Join(addrs, ", "),
		MonthlyImpact: total,
		Remediation:   "confirm each AZ genuinely needs its own NAT gateway; per-AZ NAT is usually the right call for production but multiplies both the hourly fee and the data-processing charge by the AZ count",
	}}
}

// detectProvisionedIOPSNoEvidence flags every explicitly provisioned-IOPS
// volume or database, because a compile-time review has, by definition, no
// runtime utilisation evidence to justify the number — that evidence lives in
// the rightsizing engine, not here.
func detectProvisionedIOPSNoEvidence(raws []RawResource) []simulate.CostRisk {
	var out []simulate.CostRisk
	for _, r := range raws {
		if r.Type != "aws_ebs_volume" && r.Type != "aws_db_instance" {
			continue
		}
		a := r.Effective()
		iops := a.Float("iops", 0)
		if iops <= 0 {
			continue
		}
		volType := a.Str("type", a.Str("storage_type", ""))
		if r.Type == "aws_ebs_volume" && volType == "gp3" && iops <= gp3BaselineIOPS {
			continue // within the free baseline; nothing provisioned to question
		}
		out = append(out, simulate.CostRisk{
			Code: "provisioned_iops_no_evidence", Severity: core.SeverityMedium, Address: r.Address,
			Summary:     "provisioned IOPS declared with no workload evidence available at compile time",
			Detail:      fmt.Sprintf("%g IOPS requested on a %s volume", iops, volTypeOrStorage(volType)),
			Remediation: "confirm this IOPS/throughput requirement against a measured workload before deploying; gp3's 3,000-IOPS baseline covers most workloads without a provisioned surcharge",
		})
	}
	return out
}

func volTypeOrStorage(v string) string {
	if v == "" {
		return "unspecified-type"
	}
	return v
}

// detectCrossRegionReplica flags an RDS instance whose replicate_source_db
// names a source in a different region, which layers inter-region data
// transfer on top of the replica's own instance cost.
func detectCrossRegionReplica(raws []RawResource) []simulate.CostRisk {
	var out []simulate.CostRisk
	for _, r := range raws {
		if r.Type != "aws_db_instance" {
			continue
		}
		a := r.Effective()
		src := a.Str("replicate_source_db", "")
		if src == "" {
			continue
		}
		parts := strings.Split(src, ":")
		if len(parts) < 4 || parts[0] != "arn" {
			continue // not an ARN; cannot determine the source region
		}
		srcRegion := parts[3]
		if srcRegion == "" || core.Region(srcRegion) == r.Region {
			continue
		}
		out = append(out, simulate.CostRisk{
			Code: "cross_region_replica", Severity: core.SeverityMedium, Address: r.Address,
			Summary:     fmt.Sprintf("read replica of a database in %s, replicated into %s", srcRegion, r.Region),
			Remediation: "cross-region replication adds inter-region data transfer charges on top of the replica's own instance cost; confirm this is an intentional disaster-recovery or read-locality design",
		})
	}
	return out
}

// detectFanoutExpansion flags a Terraform count/for_each family — several
// resource_changes entries sharing one base address — whose members are
// individually priced, so the reviewer sees the total blast radius of the
// multiplier in one line instead of discovering it by counting rows.
func detectFanoutExpansion(changes []simulate.PricedChange) []simulate.CostRisk {
	groups := map[string][]simulate.PricedChange{}
	for _, c := range changes {
		if c.Unpriced {
			continue
		}
		base := BaseAddress(c.Address)
		if base == c.Address {
			continue
		}
		groups[base] = append(groups[base], c)
	}
	bases := make([]string, 0, len(groups))
	for base := range groups {
		bases = append(bases, base)
	}
	sort.Strings(bases)

	var out []simulate.CostRisk
	for _, base := range bases {
		group := groups[base]
		if len(group) < 2 {
			continue
		}
		total := core.ZeroUSD()
		for _, g := range group {
			total = total.MustAdd(g.AfterMonthly)
		}
		out = append(out, simulate.CostRisk{
			Code: "count_expansion", Severity: core.SeverityLow, Address: base,
			Summary:       fmt.Sprintf("count/for_each expands %s into %d separately priced instances", base, len(group)),
			MonthlyImpact: total,
			Remediation:   "verify the multiplier is intended; a runaway count/for_each is a common source of surprise cost",
		})
	}
	return out
}

// detectUntaggedResources flags a priced, non-free, non-deleted resource with
// no tags — the exact condition under which its cost will be invisible to
// the economics engine's attribution once it is deployed.
func detectUntaggedResources(raws []RawResource, changes []simulate.PricedChange) []simulate.CostRisk {
	byAddr := make(map[string]RawResource, len(raws))
	for _, r := range raws {
		byAddr[r.Address] = r
	}
	var out []simulate.CostRisk
	for _, c := range changes {
		if c.Unpriced || c.Action == simulate.ChangeDelete || c.AfterMonthly.IsZero() {
			continue
		}
		r, ok := byAddr[c.Address]
		if !ok || len(r.Tags) > 0 {
			continue
		}
		out = append(out, simulate.CostRisk{
			Code: "untagged_resource", Severity: core.SeverityLow, Address: c.Address,
			Summary:       "priced resource has no tags and will be unattributable cost once deployed",
			MonthlyImpact: c.AfterMonthly,
			Remediation:   "add cost-attribution tags (owner, application, environment) before merging",
		})
	}
	return out
}

// detectInfiniteLogRetention flags a CloudWatch log group with no
// retention_in_days, which defaults to never expiring — a slow, compounding
// storage cost that never trips a single-month cost regression check because
// each individual month's addition is small.
func detectInfiniteLogRetention(raws []RawResource) []simulate.CostRisk {
	var out []simulate.CostRisk
	for _, r := range raws {
		if r.Type != "aws_cloudwatch_log_group" {
			continue
		}
		a := r.Effective()
		if a.Has("retention_in_days") && a.Float("retention_in_days", 0) > 0 {
			continue
		}
		out = append(out, simulate.CostRisk{
			Code: "infinite_log_retention", Severity: core.SeverityLow, Address: r.Address,
			Summary:     "log group has no retention_in_days set and will retain data forever",
			Remediation: "set an explicit retention_in_days; unbounded log retention is a common source of slow, compounding storage cost",
		})
	}
	return out
}

// detectPublicEgressHeavy sums the assumed GB/month behind every
// data-transfer-metered component this change adds (NAT processing, ALB/NLB
// LCU-driven transfer, CloudFront, transit gateway and interface VPC
// endpoints) and flags the change when that total is large — a signal to
// double-check the usage assumptions and consider caching, endpoints or a
// smaller NAT footprint, independent of whether the headline dollar delta
// looks acceptable.
func detectPublicEgressHeavy(changes []simulate.PricedChange) []simulate.CostRisk {
	const thresholdGB = 5000.0
	egressResourceTypes := map[string]bool{
		"aws_nat_gateway": true, "aws_lb": true, "aws_cloudfront_distribution": true,
		"aws_transit_gateway_vpc_attachment": true, "aws_vpc_endpoint": true,
	}
	var totalGB float64
	var addrs []string
	for _, c := range changes {
		if !egressResourceTypes[c.ResourceType] {
			continue
		}
		for _, comp := range c.PriceComponents {
			if comp.Unit == "GB" {
				totalGB += comp.Quantity
				addrs = append(addrs, c.Address)
			}
		}
	}
	if totalGB < thresholdGB {
		return nil
	}
	return []simulate.CostRisk{{
		Code: "public_egress_heavy", Severity: core.SeverityLow,
		Summary:     fmt.Sprintf("assumed public/inter-service data transfer across this change totals %.0f GB/month", totalGB),
		Detail:      strings.Join(addrs, ", "),
		Remediation: "verify the usage assumptions behind this figure; VPC endpoints, CloudFront caching or a smaller NAT footprint often reduce egress-driven cost materially",
	}}
}
