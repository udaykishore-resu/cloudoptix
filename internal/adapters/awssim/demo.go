package awssim

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// demoNow anchors every age/date computation in the demo estate (snapshot
// age, instance launch time, log-group creation time). Using a fixed
// reference rather than time.Now() keeps the estate's declared ages exactly
// reproducible run to run: the seeded PRNG picks the same *offsets* either
// way, but pinning the anchor too means "this snapshot is 400 days old"
// stays true forever rather than slowly drifting true only while the
// binary happens to be run near its original authoring date.
var demoNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

// BuildDemoEstate constructs the intentionally inefficient e-commerce estate
// ("ShopFleet") the product demo and the test suite run against.
//
// It is deterministic: every quantity that varies (a volume's size, a
// snapshot's age, which resources land in the untagged pool) is drawn from a
// PRNG seeded from fixture.yaml's params.seed, so two calls produce byte-for-
// byte identical estates. That determinism is what lets the test suite
// assert exact totals rather than ranges, and what lets a demo be re-run
// without its numbers shifting between takes.
func BuildDemoEstate() *Estate {
	f := loadFixture()
	rng := rand.New(rand.NewSource(f.Params.Seed))
	cat := pricing.New()
	region := core.Region(f.Account.Region)

	e := NewEstate(core.AccountID(f.Account.AccountID), f.Account.Alias, []core.Region{region}, cat)

	tagged := taggerFor(rng, f.Params.UntaggedFraction)

	buildNetwork(e, f, region)
	natIDs := buildNATGateways(e, f, region)
	buildEC2Fleet(e, region, natIDs, tagged)
	buildEBSBulkWaste(e, f, region, rng, tagged)
	buildSnapshotBulkWaste(e, f, region, rng, tagged)
	buildElasticIPs(e, f, region, rng, tagged)
	buildAMIs(e, region, tagged)
	buildRDS(e, region, tagged)
	buildDynamoDB(e, region, tagged)
	buildS3(e, region, tagged)
	buildLambda(e, region, tagged)
	buildECSFargate(e, f, region, rng, tagged)
	buildEKS(e, f, region, tagged)
	buildLoadBalancers(e, region, tagged)
	buildCloudFront(e, region, tagged)
	buildAPIGateways(e, region, tagged)
	buildElastiCache(e, region, tagged)
	buildMessaging(e, region, tagged)
	buildLogGroups(e, f, region, rng, tagged)
	buildKMSAndSecrets(e, region, tagged)
	buildDevelopmentSlice(e, region, rng)

	return e
}

// devEnvironment, devApplication and devTeam tag the development slice.
// The application tag deliberately matches none of demoApplications(), so
// these resources attribute to no product application — which is the truth
// about a shared sandbox and keeps them out of every per-application
// economics figure rather than quietly inflating one.
const (
	devEnvironment = "development"
	devApplication = "dev-sandbox"
	devTeam        = "platform"
)

// buildDevelopmentSlice adds ShopFleet's small shared development sandbox.
//
// Its purpose is to make the autonomous path demonstrable rather than
// theoretical. The estate is otherwise entirely production, and the shipped
// balanced policy pack auto-executes only unambiguous waste in
// non-production — so with no non-production resources the demo could report
// "0 auto-executable" forever while being completely correct, and the one
// capability the product is named for would never run end to end in front of
// anyone. A sandbox is also simply what a real estate has.
//
// Every resource here is tagged (never subject to the estate's untagged
// roll): the Environment tag is the entire point, and an untagged sandbox
// resource would inherit the account's production convention and land back
// in the population this slice exists to sit outside of.
//
// The mix is chosen so the demo shows both halves of the governance story in
// one non-production account:
//
//   - Never-used instances produce stop_instance findings — reversible,
//     non-destructive, categorically waste — which balanced.yaml clears for
//     unattended execution.
//   - Unattached volumes, stale snapshots and an idle Elastic IP produce
//     destructive or irreversible findings, which the platform's own guard
//     holds for a human no matter what any policy says.
//
// Sizes are deliberately small (a few hundred dollars a month against a
// ~$180K estate): this slice is there to be acted on, not to move the
// estate's headline numbers or its waste envelope.
func buildDevelopmentSlice(e *Estate, region core.Region, rng *rand.Rand) {
	az := "us-east-1a"
	devTags := func(name string) core.Tags {
		return core.Tags{
			"Name": name, "Application": devApplication,
			"Environment": devEnvironment, "Team": devTeam,
			// Tier 3 is a fact about a sandbox, and it is load-bearing: risk
			// scoring weights criticality, and an UNSET criticality is read
			// as "moderately important" rather than "unimportant" — which is
			// the right default for an unlabelled production resource and the
			// wrong one here.
			"Criticality": "tier3",
		}
	}

	// t3.small is chosen so these instances read unambiguously as "stop it",
	// not "shrink it": a rightsizing finding on a t3.small cannot clear its
	// own $15/month materiality floor, so the never-used finding stands
	// alone rather than becoming one of two competing answers whose winner
	// depends on the priority formula.
	devInstances := []struct {
		name  string
		itype string
		days  int
	}{
		{"dev-sandbox-scratch-1", "t3.small", 240},
		{"dev-sandbox-scratch-2", "t3.small", 190},
		{"dev-experiment-runner", "t3.small", 310},
	}
	for _, d := range devInstances {
		id := nextID("i")
		e.EC2Instances[id] = &EC2Instance{
			Base: Base{ID: id, Name: d.name, Region: region, AZ: az, State: cloud.StateRunning,
				Tags: devTags(d.name), CreatedAt: daysAgo(d.days)},
			InstanceType: d.itype, Platform: "linux", Profile: ProfileUnused, CPUBaselineP50: 0.8,
		}
		volID := nextID("vol")
		e.EBSVolumes[volID] = &EBSVolume{
			Base: Base{ID: volID, Name: d.name + "-root", Region: region, AZ: az, State: cloud.StateInUse,
				Tags: devTags(d.name + "-root"), CreatedAt: daysAgo(d.days)},
			VolumeType: "gp3", SizeGiB: 20, IOPS: 3000, ThroughputMiBps: 125, AttachedTo: id, Encrypted: true,
		}
	}

	// gp3, not gp2: the estate's unattached-gp2 count is an asserted property
	// of the production waste story, and this slice must not perturb it.
	for i := 1; i <= 4; i++ {
		id := nextID("vol")
		name := fmt.Sprintf("dev-orphan-vol-%02d", i)
		e.EBSVolumes[id] = &EBSVolume{
			Base: Base{ID: id, Name: name, Region: region, AZ: az, State: cloud.StateAvailable,
				Tags: devTags(name), CreatedAt: daysAgo(randRange(rng, 120, 400))},
			VolumeType: "gp3", SizeGiB: float64(randRange(rng, 40, 120)), IOPS: 3000, ThroughputMiBps: 125,
		}
	}

	for i := 1; i <= 6; i++ {
		id := nextID("snap")
		name := fmt.Sprintf("dev-stale-snapshot-%02d", i)
		e.EBSSnapshots[id] = &EBSSnapshot{
			Base: Base{ID: id, Name: name, Region: region, State: cloud.StateAvailable,
				Tags: devTags(name), CreatedAt: daysAgo(randRange(rng, 400, 900))},
			SizeGiB: float64(randRange(rng, 30, 90)),
		}
	}

	for i := 1; i <= 2; i++ {
		id := nextID("eipalloc")
		name := fmt.Sprintf("dev-idle-eip-%02d", i)
		e.ElasticIPs[id] = &ElasticIP{
			Base: Base{ID: id, Name: name, Region: region, State: cloud.StateAvailable,
				Tags: devTags(name), CreatedAt: daysAgo(randRange(rng, 90, 400))},
			PublicIP: fmt.Sprintf("52.16.%d.%d", rng.Intn(255), rng.Intn(255)),
		}
	}
}

// tagger returns a function that decides, per resource, whether it carries
// Application/Environment tags. Rolling this independently for every
// resource (named and bulk) rather than picking a fixed untagged subset is
// what makes the untagged fraction a genuine estate-wide statistic instead
// of a handful of hand-picked examples — the untagged pool includes bulk
// waste resources exactly as it would in a real account, where nobody tags
// an orphaned volume either.
func taggerFor(rng *rand.Rand, untaggedFraction float64) func() bool {
	return func() bool { return rng.Float64() >= untaggedFraction }
}

func mkTags(tagged bool, name, app, env, team string) core.Tags {
	t := core.Tags{"Name": name}
	if tagged {
		t["Application"] = app
		t["Environment"] = env
		t["Team"] = team
	}
	return t
}

func daysAgo(n int) time.Time { return demoNow.AddDate(0, 0, -n) }

var idCounter int

func nextID(prefix string) string {
	idCounter++
	return fmt.Sprintf("%s-0%08x", prefix, idCounter)
}

// --- network -----------------------------------------------------------

func buildNetwork(e *Estate, f fixture, region core.Region) {
	e.VPCs[f.VPC.ID] = &VPC{
		Base: Base{ID: f.VPC.ID, Name: f.VPC.Name, Region: region, State: cloud.StateAvailable,
			Tags: core.Tags{"Name": f.VPC.Name}, CreatedAt: daysAgo(900)},
		CIDR: f.VPC.CIDR,
	}
	for _, s := range f.VPC.Subnets {
		e.Subnets[s.ID] = &Subnet{
			Base: Base{ID: s.ID, Name: s.Name, Region: region, AZ: s.AZ, State: cloud.StateAvailable,
				Tags: core.Tags{"Name": s.Name}, CreatedAt: daysAgo(900)},
			VPCID: f.VPC.ID, CIDR: s.CIDR,
		}
	}
	for _, sg := range f.VPC.SecurityGroups {
		e.SecurityGroups[sg.ID] = &SecurityGroup{
			Base: Base{ID: sg.ID, Name: sg.Name, Region: region, State: cloud.StateAvailable,
				Tags: core.Tags{"Name": sg.Name}, CreatedAt: daysAgo(900)},
			VPCID: f.VPC.ID,
		}
	}
}

func buildNATGateways(e *Estate, f fixture, region core.Region) []string {
	var ids []string
	for _, n := range f.NATGateways {
		e.NATGateways[n.ID] = &NATGateway{
			Base: Base{ID: n.ID, Name: n.Name, Region: region, AZ: n.AZ, State: cloud.StateAvailable,
				Tags: core.Tags{"Name": n.Name}, CreatedAt: daysAgo(850)},
			SubnetID: n.Subnet, GBProcessedPerMonth: n.GBProcessedMonth,
		}
		ids = append(ids, n.ID)
	}
	return ids
}

// --- EC2 fleet -----------------------------------------------------------

type ec2Spec struct {
	name, itype, az, app, env, team, platform string
	profile                                   UtilizationProfile
	cpuP50                                    float64
	stopped                                   bool
	rootGiB                                   float64
	rootType                                  string
	launchedDaysAgo                           int
	forceUntagged                             bool
}

// buildEC2Fleet lays out ShopFleet's compute footprint: a handful of
// deliberately oversized and old-generation instances (the rightsizing
// story), several stopped-but-still-billing instances, and a realistic
// steady-state service fleet behind them so the waste is a minority of the
// footprint, not all of it — a demo estate that is 100% waste would not
// exercise a rule engine's ability to tell the two apart.
func buildEC2Fleet(e *Estate, region core.Region, natIDs []string, tagged func() bool) {
	azs := []string{"us-east-1a", "us-east-1b", "us-east-1c"}
	specs := []ec2Spec{
		// Oversized, chronically idle (4-8% p95 CPU): the rightsizing story.
		{"web-storefront-web-1", "m5.4xlarge", azs[0], "web-storefront", "production", "storefront", "linux", ProfileIdle, 3.5, false, 30, "gp3", 420, false},
		{"web-storefront-web-2", "m5.4xlarge", azs[1], "web-storefront", "production", "storefront", "linux", ProfileIdle, 4.1, false, 30, "gp3", 400, false},
		{"checkout-api-worker-1", "r5.2xlarge", azs[0], "checkout-api", "production", "payments", "linux", ProfileIdle, 5.8, false, 20, "gp3", 380, false},
		{"order-fulfillment-svc-1", "m5.2xlarge", azs[2], "order-fulfillment", "production", "fulfillment", "linux", ProfileIdle, 6.2, false, 20, "gp3", 300, false},
		{"analytics-batch-node-1", "r5.4xlarge", azs[1], "analytics-batch", "production", "data", "linux", ProfileCyclical, 7.9, false, 40, "gp3", 260, false},
		{"search-indexer-1", "c5.2xlarge", azs[0], "search", "production", "search", "linux", ProfileIdle, 4.7, false, 20, "gp3", 340, false},
		{"admin-portal-app-1", "m5.xlarge", azs[2], "admin-portal", "production", "platform", "linux", ProfileIdle, 6.9, false, 20, "gp3", 200, false},

		// Old-generation, still on-demand.
		{"legacy-batch-m4", "m4.2xlarge", azs[0], "analytics-batch", "production", "data", "linux", ProfileSteady, 42, false, 40, "gp2", 950, false},
		{"legacy-report-c4", "c4.xlarge", azs[1], "admin-portal", "production", "platform", "linux", ProfileCyclical, 30, false, 20, "gp2", 900, false},
		{"legacy-cache-warmer-r4", "r4.large", azs[2], "catalog-service", "production", "catalog", "linux", ProfileSteady, 35, false, 20, "gp2", 880, false},

		// Stopped, but still holding large volumes.
		{"decom-web-01", "m5.xlarge", azs[0], "web-storefront", "staging", "storefront", "linux", ProfileIdle, 0, true, 500, "gp2", 500, false},
		{"decom-batch-02", "r5.xlarge", azs[1], "analytics-batch", "staging", "data", "linux", ProfileIdle, 0, true, 800, "gp2", 460, false},
		{"old-staging-app", "t3.large", azs[2], "web-storefront", "staging", "storefront", "linux", ProfileIdle, 0, true, 200, "gp3", 300, false},
		{"abandoned-poc", "c5.xlarge", azs[0], "recommendation-engine", "sandbox", "data", "linux", ProfileIdle, 0, true, 300, "gp2", 250, false},

		// Right-sized production service fleet (steady baseline).
		{"checkout-api-app-1", "m5.xlarge", azs[0], "checkout-api", "production", "payments", "linux", ProfileSteady, 58, false, 20, "gp3", 300, false},
		{"checkout-api-app-2", "m5.xlarge", azs[1], "checkout-api", "production", "payments", "linux", ProfileSteady, 56, false, 20, "gp3", 300, false},
		{"checkout-api-app-3", "m5.xlarge", azs[2], "checkout-api", "production", "payments", "linux", ProfileSteady, 61, false, 20, "gp3", 300, false},
		{"catalog-service-app-1", "m5.large", azs[0], "catalog-service", "production", "catalog", "linux", ProfileSteady, 55, false, 20, "gp3", 300, false},
		{"catalog-service-app-2", "m5.large", azs[1], "catalog-service", "production", "catalog", "linux", ProfileSteady, 60, false, 20, "gp3", 300, false},
		{"catalog-service-app-3", "m5.large", azs[2], "catalog-service", "production", "catalog", "linux", ProfileSteady, 57, false, 20, "gp3", 300, false},
		{"payments-svc-1", "m5.xlarge", azs[0], "checkout-api", "production", "payments", "linux", ProfileSteady, 62, false, 20, "gp3", 300, false},
		{"payments-svc-2", "m5.xlarge", azs[1], "checkout-api", "production", "payments", "linux", ProfileSteady, 64, false, 20, "gp3", 300, false},
		{"inventory-svc-1", "c5.xlarge", azs[0], "inventory", "production", "fulfillment", "linux", ProfileSteady, 59, false, 20, "gp3", 300, false},
		{"inventory-svc-2", "c5.xlarge", azs[1], "inventory", "production", "fulfillment", "linux", ProfileSteady, 63, false, 20, "gp3", 300, false},
		{"notifications-worker-1", "t3.large", azs[2], "notifications", "production", "platform", "linux", ProfileSpiky, 25, false, 20, "gp3", 300, false},
		{"recommendation-engine-1", "r5.xlarge", azs[0], "recommendation-engine", "production", "data", "linux", ProfileSpiky, 30, false, 20, "gp3", 260, false},
		{"search-query-1", "c5.2xlarge", azs[1], "search", "production", "search", "linux", ProfileSteady, 66, false, 20, "gp3", 300, false},
		{"search-query-2", "c5.2xlarge", azs[2], "search", "production", "search", "linux", ProfileSteady, 68, false, 20, "gp3", 300, false},

		// Untagged pool — forgotten resources, no Application/Environment.
		{"mystery-box-1", "t3.medium", azs[0], "", "", "", "linux", ProfileIdle, 2, false, 20, "gp3", 700, true},
		{"temp-migration-vm", "m5.large", azs[1], "", "", "", "linux", ProfileSteady, 40, false, 20, "gp3", 120, true},
		{"jenkins-old", "c5.large", azs[2], "", "", "", "linux", ProfileIdle, 8, false, 20, "gp3", 650, true},
	}

	// Wide autoscaled service tier: more of the same handful of
	// applications, spread across AZs, so EC2 spend is dominated by
	// ordinary (non-wasteful) capacity rather than by the story instances.
	scaleOut := []struct {
		namePrefix, itype, app, team string
		count                        int
	}{
		{"web-storefront-web-scale", "m5.xlarge", "web-storefront", "storefront", 12},
		{"catalog-service-scale", "c5.xlarge", "catalog-service", "catalog", 8},
		{"checkout-api-scale", "m5.2xlarge", "checkout-api", "payments", 6},
	}
	for _, sc := range scaleOut {
		for i := 1; i <= sc.count; i++ {
			specs = append(specs, ec2Spec{
				name: fmt.Sprintf("%s-%d", sc.namePrefix, i), itype: sc.itype, az: azs[i%3],
				app: sc.app, env: "production", team: sc.team, platform: "linux",
				profile: ProfileSteady, cpuP50: 45 + float64(i%20), rootGiB: 20, rootType: "gp3",
				launchedDaysAgo: 90 + i*3,
			})
		}
	}

	natIdx := 0
	for _, sp := range specs {
		id := nextID("i")
		state := cloud.StateRunning
		var stoppedAt *time.Time
		if sp.stopped {
			state = cloud.StateStopped
			t := daysAgo(60)
			stoppedAt = &t
		}
		isTagged := tagged() && !sp.forceUntagged
		if sp.forceUntagged {
			isTagged = false
		}
		nat := ""
		if !sp.stopped && len(natIDs) > 0 {
			nat = natIDs[natIdx%len(natIDs)]
			natIdx++
		}
		e.EC2Instances[id] = &EC2Instance{
			Base: Base{ID: id, Name: sp.name, Region: region, AZ: sp.az, State: state,
				Tags: mkTags(isTagged, sp.name, sp.app, sp.env, sp.team), CreatedAt: daysAgo(sp.launchedDaysAgo)},
			InstanceType: sp.itype, Platform: sp.platform, Profile: sp.profile,
			CPUBaselineP50: sp.cpuP50, StoppedAt: stoppedAt, NATGatewayID: nat,
		}
		volID := nextID("vol")
		e.EBSVolumes[volID] = &EBSVolume{
			Base: Base{ID: volID, Name: sp.name + "-root", Region: region, AZ: sp.az, State: cloud.StateInUse,
				Tags: mkTags(isTagged, sp.name+"-root", sp.app, sp.env, sp.team), CreatedAt: daysAgo(sp.launchedDaysAgo)},
			VolumeType: sp.rootType, SizeGiB: sp.rootGiB, IOPS: 3000, ThroughputMiBps: 125,
			AttachedTo: id, Encrypted: true,
		}
	}
}

// --- bulk EBS/snapshot/EIP waste ------------------------------------------

func buildEBSBulkWaste(e *Estate, f fixture, region core.Region, rng *rand.Rand, tagged func() bool) {
	p := f.Params
	azs := []string{"us-east-1a", "us-east-1b", "us-east-1c"}
	for i := 0; i < p.UnattachedGP2Count; i++ {
		id := nextID("vol")
		name := fmt.Sprintf("orphan-vol-%03d", i+1)
		size := float64(randRange(rng, p.UnattachedGP2MinGiB, p.UnattachedGP2MaxGiB))
		e.EBSVolumes[id] = &EBSVolume{
			Base: Base{ID: id, Name: name, Region: region, AZ: azs[i%3], State: cloud.StateAvailable,
				Tags: mkTags(tagged(), name, "", "", ""), CreatedAt: daysAgo(randRange(rng, 100, 700))},
			VolumeType: "gp2", SizeGiB: size, IOPS: 3000, ThroughputMiBps: 125, AttachedTo: "",
		}
	}
	for i := 0; i < p.GP2ShouldBeGP3Count; i++ {
		instID := attachTargetFor(e, i)
		id := nextID("vol")
		name := fmt.Sprintf("data-vol-%03d", i+1)
		size := float64(randRange(rng, p.GP2ShouldBeGP3MinGiB, p.GP2ShouldBeGP3MaxGiB))
		e.EBSVolumes[id] = &EBSVolume{
			Base: Base{ID: id, Name: name, Region: region, AZ: azs[i%3], State: cloud.StateInUse,
				Tags: mkTags(tagged(), name, "", "", ""), CreatedAt: daysAgo(randRange(rng, 100, 800))},
			VolumeType: "gp2", SizeGiB: size, IOPS: 3000, ThroughputMiBps: 125, AttachedTo: instID,
		}
	}
}

// attachTargetFor picks a running instance (round-robin over the discovered
// set) to attach an extra data volume to, so gp2-should-be-gp3 volumes are
// realistically in-use rather than orphaned.
func attachTargetFor(e *Estate, i int) string {
	if len(e.EC2Instances) == 0 {
		return ""
	}
	ids := make([]string, 0, len(e.EC2Instances))
	for id, inst := range e.EC2Instances {
		if inst.State == cloud.StateRunning {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return ids[i%len(ids)]
}

func buildSnapshotBulkWaste(e *Estate, f fixture, region core.Region, rng *rand.Rand, tagged func() bool) {
	p := f.Params
	volIDs := make([]string, 0, len(e.EBSVolumes))
	for id := range e.EBSVolumes {
		volIDs = append(volIDs, id)
	}
	for i := 0; i < p.SnapshotCount; i++ {
		id := nextID("snap")
		name := fmt.Sprintf("shopfleet-backup-%04d", i+1)
		size := float64(randRange(rng, p.SnapshotMinGiB, p.SnapshotMaxGiB))
		age := randRange(rng, p.SnapshotMinAgeDays, p.SnapshotMaxAgeDays)
		vol := ""
		if len(volIDs) > 0 {
			vol = volIDs[rng.Intn(len(volIDs))]
		}
		e.EBSSnapshots[id] = &EBSSnapshot{
			Base: Base{ID: id, Name: name, Region: region, State: cloud.StateAvailable,
				Tags: mkTags(tagged(), name, "", "", ""), CreatedAt: daysAgo(age)},
			VolumeID: vol, SizeGiB: size,
		}
	}
}

func buildElasticIPs(e *Estate, f fixture, region core.Region, rng *rand.Rand, tagged func() bool) {
	p := f.Params
	for i := 0; i < p.UnattachedEIPCount; i++ {
		id := nextID("eipalloc")
		name := fmt.Sprintf("unattached-eip-%02d", i+1)
		e.ElasticIPs[id] = &ElasticIP{
			Base: Base{ID: id, Name: name, Region: region, State: cloud.StateAvailable,
				Tags: mkTags(tagged(), name, "", "", ""), CreatedAt: daysAgo(randRange(rng, 60, 500))},
			PublicIP: fmt.Sprintf("52.14.%d.%d", rng.Intn(255), rng.Intn(255)), AttachedTo: "",
		}
	}
	natIDs := make([]string, 0, len(e.NATGateways))
	for id := range e.NATGateways {
		natIDs = append(natIDs, id)
	}
	for i := 0; i < p.AttachedEIPCount; i++ {
		id := nextID("eipalloc")
		name := fmt.Sprintf("attached-eip-%02d", i+1)
		target := ""
		if len(natIDs) > 0 {
			target = natIDs[i%len(natIDs)]
		}
		e.ElasticIPs[id] = &ElasticIP{
			Base: Base{ID: id, Name: name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), name, "", "", ""), CreatedAt: daysAgo(randRange(rng, 60, 500))},
			PublicIP: fmt.Sprintf("52.15.%d.%d", rng.Intn(255), rng.Intn(255)), AttachedTo: target,
		}
	}
}

func buildAMIs(e *Estate, region core.Region, tagged func() bool) {
	amis := []struct {
		name    string
		sizeGiB float64
	}{
		{"shopfleet-web-golden-2025-11", 30}, {"shopfleet-batch-golden-2025-09", 40},
		{"shopfleet-legacy-image-2024-03", 40}, {"shopfleet-checkout-golden-2026-02", 30},
		{"shopfleet-poc-image-2024-11", 30},
	}
	for i, a := range amis {
		id := nextID("ami")
		e.AMIs[id] = &AMI{
			Base: Base{ID: id, Name: a.name, Region: region, State: cloud.StateAvailable,
				Tags: mkTags(tagged(), a.name, "", "", ""), CreatedAt: daysAgo(200 + i*80)},
			SizeGiB: a.sizeGiB,
		}
	}
}

func randRange(rng *rand.Rand, min, max int) int {
	if max <= min {
		return min
	}
	return min + rng.Intn(max-min+1)
}
