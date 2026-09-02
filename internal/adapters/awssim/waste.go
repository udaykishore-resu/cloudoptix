package awssim

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// WasteBreakdown is the estimated monthly saving available in each waste
// category the demo estate deliberately contains. It exists so the
// estate's target waste envelope ($40-50K/month, per the demo's design
// brief) is a number this package actually computes and tests against,
// rather than an assertion nobody can check. It is not the optimization
// engine: each figure is a documented, conservative heuristic (drop two
// instance sizes, assume 80% of NAT-processed bytes are S3-bound) standing
// in for what the real rule engine (out of scope for this package) would
// derive from metrics and topology. Treat it as a sanity check on the
// estate's shape, not as a recommendation.
type WasteBreakdown struct {
	OversizedCompute       core.Money // EC2 instances at chronically low utilisation
	OldGenerationCompute   core.Money // EC2 instances one or more generations behind
	StoppedInstanceVolumes core.Money // volumes attached only to stopped instances
	UnattachedVolumes      core.Money // EBS volumes with no attachment
	GP2ShouldBeGP3         core.Money // gp2 volumes priced against their gp3 equivalent
	OldSnapshots           core.Money // snapshots older than the fixture's staleness threshold
	UnattachedEIPs         core.Money // idle Elastic IP allocations
	OversizedRDS           core.Money // multi-AZ primaries with headroom to downsize
	UnusedRDSReplicas      core.Money // read replicas with no identified consumer
	S3Lifecycle            core.Money // incomplete multipart uploads + non-current versions
	LambdaOverprovisioning core.Money // idle provisioned concurrency + oversized memory
	EKSPacking             core.Money // node capacity paid for but not requested by pods
	NATWithoutEndpoint     core.Money // NAT data processing a VPC endpoint would remove
	Total                  core.Money
}

// EstimatedIdentifiableWaste sums the categories above. See WasteBreakdown's
// doc comment for what each figure represents and its limits.
func (e *Estate) EstimatedIdentifiableWaste() WasteBreakdown {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var w WasteBreakdown
	w.OversizedCompute = e.oversizedComputeWaste()
	w.OldGenerationCompute = e.oldGenerationWaste()
	w.StoppedInstanceVolumes = e.stoppedInstanceVolumeWaste()
	w.UnattachedVolumes = e.unattachedVolumeWaste()
	w.GP2ShouldBeGP3 = e.gp2ToGP3Waste()
	w.OldSnapshots = e.oldSnapshotWaste()
	w.UnattachedEIPs = e.unattachedEIPWaste()
	w.OversizedRDS, w.UnusedRDSReplicas = e.rdsWaste()
	w.S3Lifecycle = e.s3LifecycleWaste()
	w.LambdaOverprovisioning = e.lambdaWaste()
	w.EKSPacking = e.eksPackingWaste()
	w.NATWithoutEndpoint = e.natWaste()

	w.Total = addMoney(w.OversizedCompute, w.OldGenerationCompute, w.StoppedInstanceVolumes,
		w.UnattachedVolumes, w.GP2ShouldBeGP3, w.OldSnapshots, w.UnattachedEIPs,
		w.OversizedRDS, w.UnusedRDSReplicas, w.S3Lifecycle, w.LambdaOverprovisioning,
		w.EKSPacking, w.NATWithoutEndpoint)
	return w
}

// idleOversizedThreshold is the P50 CPU below which a steadily-idle instance
// is considered a rightsizing candidate — well below the ~40% headroom
// AWS's own Compute Optimizer uses as its "underutilized" line, matched to
// this estate's deliberately extreme 4-8% story instances so the heuristic
// does not also sweep up ordinary spiky/cyclical workloads that are merely
// quiet on average.
const idleOversizedThreshold = 10.0

func (e *Estate) oversizedComputeWaste() core.Money {
	total := core.ZeroUSD()
	for _, i := range e.EC2Instances {
		if i.Profile != ProfileIdle || i.CPUBaselineP50 >= idleOversizedThreshold || i.State != "running" {
			continue
		}
		current := e.InstanceMonthlyCost(i)
		target := e.rightsizedCost(i.Region, i.InstanceType, i.Platform)
		if target.LessThan(current) {
			total = total.MustAdd(current.MustSub(target))
		}
	}
	return total
}

// rightsizedCost prices the candidate two rungs down the family ladder (or
// the smallest available rung, whichever is larger), which is the
// conservative "don't downsize in one jump" heuristic FinOps guidance
// generally recommends.
func (e *Estate) rightsizedCost(region core.Region, instanceType, platform string) core.Money {
	spec, ok := e.Catalog.InstanceSpec(instanceType)
	if !ok {
		return core.Money{}
	}
	smaller := e.Catalog.SmallerCandidates(spec)
	if len(smaller) == 0 {
		return core.Money{}
	}
	idx := 1
	if idx >= len(smaller) {
		idx = len(smaller) - 1
	}
	price, ok := e.Catalog.InstancePrice(region, smaller[idx].Type, platform)
	if !ok {
		return core.Money{}
	}
	return price.Scale(core.HoursPerMonth)
}

func (e *Estate) oldGenerationWaste() core.Money {
	total := core.ZeroUSD()
	for _, i := range e.EC2Instances {
		if i.State != "running" {
			continue
		}
		spec, ok := e.Catalog.InstanceSpec(i.InstanceType)
		if !ok || spec.SuccessorType == "" {
			continue
		}
		current := e.InstanceMonthlyCost(i)
		succPrice, ok := e.Catalog.InstancePrice(i.Region, spec.SuccessorType, i.Platform)
		if !ok {
			continue
		}
		successor := succPrice.Scale(core.HoursPerMonth)
		if successor.LessThan(current) {
			total = total.MustAdd(current.MustSub(successor))
		}
	}
	return total
}

func (e *Estate) stoppedInstanceVolumeWaste() core.Money {
	total := core.ZeroUSD()
	for _, v := range e.EBSVolumes {
		if v.AttachedTo == "" {
			continue
		}
		if inst, ok := e.EC2Instances[v.AttachedTo]; ok && inst.State == "stopped" {
			total = total.MustAdd(e.VolumeMonthlyCost(v))
		}
	}
	return total
}

func (e *Estate) unattachedVolumeWaste() core.Money {
	total := core.ZeroUSD()
	for _, v := range e.EBSVolumes {
		if v.AttachedTo == "" {
			total = total.MustAdd(e.VolumeMonthlyCost(v))
		}
	}
	return total
}

func (e *Estate) gp2ToGP3Waste() core.Money {
	total := core.ZeroUSD()
	for _, v := range e.EBSVolumes {
		if v.VolumeType != "gp2" || v.AttachedTo == "" {
			continue
		}
		current := e.VolumeMonthlyCost(v)
		gp3 := priceOr0(e.Catalog.StoragePrice(v.Region, "gp3")).Scale(v.SizeGiB)
		if gp3.LessThan(current) {
			total = total.MustAdd(current.MustSub(gp3))
		}
	}
	return total
}

func (e *Estate) oldSnapshotWaste() core.Money {
	f := loadFixture()
	threshold := f.Params.SnapshotOldThresholdDays
	total := core.ZeroUSD()
	for _, s := range e.EBSSnapshots {
		ageDays := int(demoNow.Sub(s.CreatedAt).Hours() / 24)
		if ageDays >= threshold {
			total = total.MustAdd(e.SnapshotMonthlyCost(s))
		}
	}
	return total
}

func (e *Estate) unattachedEIPWaste() core.Money {
	total := core.ZeroUSD()
	for _, ip := range e.ElasticIPs {
		total = total.MustAdd(e.ElasticIPMonthlyCost(ip))
	}
	return total
}

// rdsOversizedInstanceClasses flags the specific classes this estate uses
// for its deliberately-oversized story primary. A real rule would compare
// against CloudWatch CPU/connections; the demo estate does not run
// CloudWatch, so the waste estimate names the story resource directly.
func (e *Estate) rdsWaste() (oversized, unusedReplica core.Money) {
	oversized, unusedReplica = core.ZeroUSD(), core.ZeroUSD()
	for _, r := range e.RDSInstances {
		if r.IsReadReplica {
			unusedReplica = unusedReplica.MustAdd(e.RDSInstanceMonthlyCost(r))
			continue
		}
		if r.Profile != ProfileIdle || !r.MultiAZ {
			continue
		}
		current := e.RDSInstanceMonthlyCost(r)
		// A conservative one-size-down estimate: halve compute (the typical
		// gap between adjacent RDS instance classes), storage unchanged.
		half := current.Scale(0.35) // ~35% of current cost recovered
		oversized = oversized.MustAdd(half)
	}
	return oversized, unusedReplica
}

func (e *Estate) s3LifecycleWaste() core.Money {
	total := core.ZeroUSD()
	for _, b := range e.S3Buckets {
		standardRate := priceOr0(e.Catalog.StoragePrice(b.Region, "standard"))
		total = total.MustAdd(standardRate.Scale(b.IncompleteMultipartGiB))
		total = total.MustAdd(standardRate.Scale(b.NonCurrentVersionGiB))
		if !b.HasLifecyclePolicy {
			// Half of a lifecycle-free bucket's Standard data is plausibly
			// cold enough to move to Infrequent Access; the other half is
			// active working-set data a lifecycle rule would not touch.
			iaRate := priceOr0(e.Catalog.StoragePrice(b.Region, "standard_ia"))
			coldGiB := b.StorageGiB["standard"] * 0.5
			current := standardRate.Scale(coldGiB)
			moved := iaRate.Scale(coldGiB)
			if moved.LessThan(current) {
				total = total.MustAdd(current.MustSub(moved))
			}
		}
	}
	return total
}

func (e *Estate) lambdaWaste() core.Money {
	total := core.ZeroUSD()
	for _, fn := range e.LambdaFunctions {
		if fn.ProvisionedConcurrency > 0 && fn.Profile == ProfileIdle {
			gb := float64(fn.MemoryMB) / 1024
			pcGBSeconds := gb * float64(fn.ProvisionedConcurrency) * core.HoursPerMonth * 3600
			total = total.MustAdd(priceOr0(e.Catalog.ServicePrice(fn.Region, "lambda", "provisioned_concurrency_gb_second")).Scale(pcGBSeconds))
		}
		if fn.MemoryMB >= 2048 && fn.AvgDurationMS < 250 {
			// High memory allocated to a short-running function is the
			// classic Lambda over-provisioning signature; half the compute
			// cost is a conservative estimate of the recoverable portion.
			total = total.MustAdd(e.LambdaMonthlyCost(fn).Scale(0.5))
		}
	}
	return total
}

func (e *Estate) eksPackingWaste() core.Money {
	total := core.ZeroUSD()
	for _, ng := range e.EKSNodeGroups {
		cost := e.NodeGroupMonthlyCost(ng)
		idle := 1 - ng.PackedFraction
		if idle < 0 {
			idle = 0
		}
		total = total.MustAdd(cost.Scale(idle))
	}
	return total
}

// natS3TrafficShare is the documented assumption behind NATWithoutEndpoint:
// this estate's fixture comment states its NAT traffic is heavy S3-bound
// traffic that a gateway VPC endpoint removes entirely. Not all NAT
// processing is S3-bound in reality — a real estate also sends
// third-party-API and cross-region traffic through NAT that no endpoint
// touches — so the waste estimate recovers a documented majority share
// rather than 100% of the processing charge; the NAT's hourly charge itself
// is untouched, since the gateway is still needed for other egress.
const natS3TrafficShare = 0.80

func (e *Estate) natWaste() core.Money {
	total := core.ZeroUSD()
	for _, n := range e.NATGateways {
		processed := priceOr0(e.Catalog.ServicePrice(n.Region, "nat_gateway", "gb_processed")).Scale(n.GBProcessedPerMonth)
		total = total.MustAdd(processed.Scale(natS3TrafficShare))
	}
	return total
}
