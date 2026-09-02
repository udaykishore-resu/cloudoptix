package compiler

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// secondsPerMonth is HoursPerMonth expressed in seconds, used by Lambda
// provisioned-concurrency pricing, which bills per GB-second held ready
// regardless of invocation.
const secondsPerMonth = core.HoursPerMonth * 3600

func priceEC2Instance(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	instanceType := a.Str("instance_type", "")
	if instanceType == "" {
		return unpricedOutcome("%s has no instance_type", r.Address)
	}
	spot := false
	if mo := a.FirstMap("instance_market_options"); mo != nil {
		if Attrs(mo).Str("market_type", "") == "spot" {
			spot = true
		}
	}
	var hourly core.Money
	var ok bool
	basis := "on_demand"
	if spot {
		hourly, ok = pc.pricing.SpotPrice(r.Region, instanceType)
		basis = "spot_avg"
	} else {
		platform := a.Str("platform", "")
		hourly, ok = pc.pricing.InstancePrice(r.Region, instanceType, platform)
	}
	if !ok {
		return unpricedOutcome("no pricing data for instance type %q in region %s", instanceType, r.Region)
	}
	monthly := monthlyFromHourly(hourly)
	comps := []simulate.PriceComponent{{
		Name: "instance hours", Unit: "hour", Quantity: core.HoursPerMonth,
		UnitPrice: hourly, Monthly: monthly, PriceBasis: basis,
	}}

	addVolume := func(name string, size, iops, throughput float64, volType string) {
		if size <= 0 {
			return
		}
		if volType == "" {
			volType = "gp3"
		}
		if sp, ok := pc.pricing.StoragePrice(r.Region, volType); ok {
			c := sp.Scale(size)
			monthly = monthly.MustAdd(c)
			comps = append(comps, simulate.PriceComponent{Name: name + " storage", Unit: "GiB-month", Quantity: size, UnitPrice: sp, Monthly: c, PriceBasis: "on_demand"})
		}
		if iops > 0 {
			if ip, ok := pc.pricing.IOPSPrice(r.Region, volType); ok {
				c := ip.Scale(iops)
				monthly = monthly.MustAdd(c)
				comps = append(comps, simulate.PriceComponent{Name: name + " provisioned IOPS", Unit: "IOPS-month", Quantity: iops, UnitPrice: ip, Monthly: c, PriceBasis: "on_demand"})
			}
		}
		if throughput > 0 {
			if tp, ok := pc.pricing.ThroughputPrice(r.Region, volType); ok {
				c := tp.Scale(throughput)
				monthly = monthly.MustAdd(c)
				comps = append(comps, simulate.PriceComponent{Name: name + " provisioned throughput", Unit: "MiBps-month", Quantity: throughput, UnitPrice: tp, Monthly: c, PriceBasis: "on_demand"})
			}
		}
	}
	if root := a.FirstMap("root_block_device"); root != nil {
		ra := Attrs(root)
		addVolume("root volume", ra.Float("volume_size", 8), ra.Float("iops", 0), ra.Float("throughput", 0), ra.Str("volume_type", "gp3"))
	}
	for _, dev := range a.List("ebs_block_device") {
		if dm, ok := dev.(map[string]any); ok {
			da := Attrs(dm)
			addVolume("additional volume", da.Float("volume_size", 0), da.Float("iops", 0), da.Float("throughput", 0), da.Str("volume_type", "gp3"))
		}
	}
	return priceOutcome{Monthly: monthly, Components: comps}
}

// priceASG prices an autoscaling group by its desired capacity times the
// hourly rate of the launch template or launch configuration it references.
// TF plan JSON's "after" object shows launch_template as a partially-computed
// reference block (id/name/version), not the referenced resource's own
// attributes, so this compiler cannot follow the reference precisely; it
// instead looks the referenced resource up among the launch
// templates/configurations parsed from the same change set, by name when one
// is given, or — when the change set contains exactly one — uses it
// unambiguously and says so in a warning. Ambiguous or unresolved references
// are Unpriced rather than guessed.
func priceASG(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	desired := a.Float("desired_capacity", a.Float("min_size", 1))
	if desired <= 0 {
		desired = 1
	}
	var lt Attrs
	var warn string
	if ltRef := a.FirstMap("launch_template"); ltRef != nil {
		if name := Attrs(ltRef).Str("name", ""); name != "" {
			lt = pc.launchTemplates[name]
		}
	}
	if lt == nil {
		if name := a.Str("launch_configuration", ""); name != "" {
			lt = pc.launchTemplates[name]
		}
	}
	if lt == nil {
		if len(pc.launchTemplates) == 1 {
			for _, only := range pc.launchTemplates {
				lt = only
			}
			warn = "instance type resolved from the only launch template/configuration present in this change set; verify the group actually references it"
		} else {
			return unpricedOutcome("cannot resolve the launch template or launch configuration this autoscaling group references (found %d candidates in the change set)", len(pc.launchTemplates))
		}
	}
	instanceType := lt.Str("instance_type", "")
	if instanceType == "" {
		return unpricedOutcome("the resolved launch template/configuration has no instance_type")
	}
	hourly, ok := pc.pricing.InstancePrice(r.Region, instanceType, lt.Str("platform", ""))
	if !ok {
		return unpricedOutcome("no pricing data for instance type %q in region %s", instanceType, r.Region)
	}
	monthly := monthlyFromHourly(hourly).Scale(desired)
	comps := []simulate.PriceComponent{{
		Name: "desired-capacity instance hours", Unit: "hour", Quantity: core.HoursPerMonth * desired,
		UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand",
	}}
	out := priceOutcome{Monthly: monthly, Components: comps}
	if warn != "" {
		out.Warnings = []string{warn}
	}
	return out
}

func priceEKSCluster(pc *pricerCtx, r RawResource, _ Attrs) priceOutcome {
	hourly, ok := pc.pricing.ServicePrice(r.Region, "eks", "cluster_hour")
	if !ok {
		return unpricedOutcome("no eks cluster_hour pricing for region %s", r.Region)
	}
	monthly := monthlyFromHourly(hourly)
	return priceOutcome{Monthly: monthly, Components: []simulate.PriceComponent{
		{Name: "control plane hours", Unit: "hour", Quantity: core.HoursPerMonth, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"},
	}}
}

func priceEKSNodeGroup(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	types := a.List("instance_types")
	if len(types) == 0 {
		return unpricedOutcome("%s has no instance_types", r.Address)
	}
	instanceType, _ := asString(types[0])
	if instanceType == "" {
		return unpricedOutcome("%s has no usable instance_types entry", r.Address)
	}
	desired := 1.0
	if sc := a.FirstMap("scaling_config"); sc != nil {
		desired = Attrs(sc).Float("desired_size", 1)
	}
	hourly, ok := pc.pricing.InstancePrice(r.Region, instanceType, "")
	if !ok {
		return unpricedOutcome("no pricing data for instance type %q in region %s", instanceType, r.Region)
	}
	monthly := monthlyFromHourly(hourly).Scale(desired)
	return priceOutcome{Monthly: monthly, Components: []simulate.PriceComponent{
		{Name: "desired-size node hours", Unit: "hour", Quantity: core.HoursPerMonth * desired, UnitPrice: hourly, Monthly: monthly, PriceBasis: "on_demand"},
	}}
}

// priceECSService prices a Fargate-launch-type ECS service from the cpu and
// memory of the task definition it runs. An EC2-launch-type service is
// Unpriced because its cost is entirely carried by the cluster's underlying
// EC2 capacity, which this compiler prices separately as ordinary aws_instance
// or aws_autoscaling_group resources — pricing the service too would double
// count.
func priceECSService(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	launchType := a.Str("launch_type", "EC2")
	if launchType != "FARGATE" {
		return unpricedOutcome("EC2-launch-type ECS service cost is carried by its underlying EC2 capacity, priced separately")
	}
	desired := a.Float("desired_count", 1)
	if desired <= 0 {
		desired = 1
	}
	var td Attrs
	var warn string
	if name := a.Str("task_definition", ""); name != "" {
		td = pc.taskDefs[name]
	}
	if td == nil {
		if len(pc.taskDefs) == 1 {
			for _, only := range pc.taskDefs {
				td = only
			}
			warn = "task cpu/memory resolved from the only aws_ecs_task_definition present in this change set; verify the service actually references it"
		} else {
			return unpricedOutcome("cannot resolve the task definition this Fargate service runs (found %d candidates in the change set)", len(pc.taskDefs))
		}
	}
	cpuUnits := td.Float("cpu", 0)
	memMB := td.Float("memory", 0)
	if cpuUnits <= 0 || memMB <= 0 {
		return unpricedOutcome("the resolved task definition has no usable cpu/memory")
	}
	vcpu := cpuUnits / 1024
	gib := memMB / 1024
	vcpuPrice, ok1 := pc.pricing.ServicePrice(r.Region, "fargate", "vcpu_hour")
	gbPrice, ok2 := pc.pricing.ServicePrice(r.Region, "fargate", "gb_hour")
	if !ok1 || !ok2 {
		return unpricedOutcome("no fargate pricing for region %s", r.Region)
	}
	perTaskHourly := vcpuPrice.Scale(vcpu).MustAdd(gbPrice.Scale(gib))
	monthly := monthlyFromHourly(perTaskHourly).Scale(desired)
	comps := []simulate.PriceComponent{
		{Name: "fargate vCPU-hours", Unit: "vCPU-hour", Quantity: vcpu * core.HoursPerMonth * desired, UnitPrice: vcpuPrice, Monthly: monthlyFromHourly(vcpuPrice.Scale(vcpu)).Scale(desired), PriceBasis: "on_demand"},
		{Name: "fargate GB-hours", Unit: "GB-hour", Quantity: gib * core.HoursPerMonth * desired, UnitPrice: gbPrice, Monthly: monthlyFromHourly(gbPrice.Scale(gib)).Scale(desired), PriceBasis: "on_demand"},
	}
	out := priceOutcome{Monthly: monthly, Components: comps}
	if warn != "" {
		out.Warnings = []string{warn}
	}
	return out
}

// priceLambdaFunction is always usage-dependent: the request rate and
// average duration determine essentially all of a Lambda function's cost,
// and neither is knowable from the function's declaration. Defaults are
// deliberately conservative round numbers, stated as Assumptions and
// overridable per function via CompileRequest.Assumptions.
func priceLambdaFunction(pc *pricerCtx, r RawResource, a Attrs) priceOutcome {
	memMB := a.Float("memory_size", 128)
	if memMB <= 0 {
		memMB = 128
	}
	arch := "x86_64"
	if archs := a.List("architectures"); len(archs) > 0 {
		if s, ok := asString(archs[0]); ok && s != "" {
			arch = s
		}
	}
	gbSecondDim := "gb_second"
	if arch == "arm64" {
		gbSecondDim = "arm_gb_second"
	}
	gbSecondPrice, ok := pc.pricing.ServicePrice(r.Region, "lambda", gbSecondDim)
	if !ok {
		return unpricedOutcome("no lambda pricing for region %s", r.Region)
	}
	requestPrice, _ := pc.pricing.ServicePrice(r.Region, "lambda", "request")

	invocations, invOverridden := pc.resolveAssumption(r.Address, "lambda_invocations_month", 1_000_000)
	avgMS, durOverridden := pc.resolveAssumption(r.Address, "lambda_avg_duration_ms", 200)

	memGB := memMB / 1024
	durationS := avgMS / 1000
	computeCost := gbSecondPrice.Scale(memGB * durationS * invocations)
	requestCost := requestPrice.Scale(invocations / 1000)
	monthly := computeCost.MustAdd(requestCost)

	comps := []simulate.PriceComponent{
		{Name: "compute (GB-seconds)", Unit: "GB-second", Quantity: memGB * durationS * invocations, UnitPrice: gbSecondPrice, Monthly: computeCost, PriceBasis: "on_demand"},
		{Name: "requests", Unit: "1K requests", Quantity: invocations / 1000, UnitPrice: requestPrice, Monthly: requestCost, PriceBasis: "on_demand"},
	}
	assumptions := []simulate.Assumption{
		usageAssumption("lambda_invocations_month", "Monthly invocations", invocations, "invocations/month", invOverridden,
			"Lambda's dominant cost driver; not knowable from the function's declaration alone."),
		usageAssumption("lambda_avg_duration_ms", "Average invocation duration", avgMS, "ms", durOverridden, ""),
	}

	if pcc := a.FirstMap("provisioned_concurrency_config"); pcc != nil {
		concurrency := Attrs(pcc).Float("provisioned_concurrent_executions", 0)
		if concurrency > 0 {
			if pcPrice, ok := pc.pricing.ServicePrice(r.Region, "lambda", "provisioned_concurrency_gb_second"); ok {
				fixed := pcPrice.Scale(memGB * concurrency * secondsPerMonth)
				monthly = monthly.MustAdd(fixed)
				comps = append(comps, simulate.PriceComponent{
					Name: "provisioned concurrency (held capacity, billed whether invoked or not)", Unit: "GB-second",
					Quantity: memGB * concurrency * secondsPerMonth, UnitPrice: pcPrice, Monthly: fixed, PriceBasis: "on_demand",
				})
			}
		}
	}

	return priceOutcome{Monthly: monthly, UsageDependent: true, Components: comps, Assumptions: assumptions}
}
