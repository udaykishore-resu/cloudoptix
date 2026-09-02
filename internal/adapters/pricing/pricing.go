// Package pricing implements ports.PricingCatalog over a static, embedded AWS
// price book.
//
// The design decision this package turns on: CloudOptix never calls the live
// AWS Price List API on the hot path. The cost compiler, the mutation engine
// and every counterfactual evaluate hundreds to thousands of candidate
// configurations per analysis run, and the Price List API is neither fast
// enough nor stable enough (it revises SKUs and pagination tokens under you)
// to be an inner-loop dependency. So the catalog is a JSON file embedded into
// the binary with go:embed: it ships with the binary, is trivially
// reproducible in tests, and its staleness is an explicit, visible fact
// (PricingDate) rather than a silent one.
//
// A lookup that finds nothing returns (zero, false) and nothing else. The
// catalog never fabricates a number for an unknown instance type, region or
// dimension: every downstream engine (rightsizing, commitment analysis,
// savings estimation) multiplies a price by a large usage quantity, and a
// guessed price would silently corrupt every recommendation built on it. A
// caller that gets false must treat the configuration as unpriced, not price
// it at zero or at a neighbour's rate.
//
// Traceability: REQ-COST-002, SPEC-COST-003 (pricing), REQ-OPT-003 (rightsizing candidates).
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

//go:embed pricebook.json
var pricebookJSON []byte

// bookInstanceType is the on-disk shape of one EC2 instance type entry.
type bookInstanceType struct {
	Family       string  `json:"family"`
	Size         string  `json:"size"`
	Generation   int     `json:"generation"`
	VCPU         float64 `json:"vcpu"`
	MemoryGiB    float64 `json:"memory_gib"`
	NetworkGbps  float64 `json:"network_gbps"`
	Architecture string  `json:"architecture"`
	Burstable    bool    `json:"burstable"`
	EBSOptimized bool    `json:"ebs_optimized"`
	Successor    string  `json:"successor,omitempty"`

	OnDemand   float64 `json:"on_demand"`
	Spot       float64 `json:"spot"`
	RI1yrNoUp  float64 `json:"ri_1yr_no_upfront"`
	RI1yrAllUp float64 `json:"ri_1yr_all_upfront"`
	RI3yrNoUp  float64 `json:"ri_3yr_no_upfront"`
	RI3yrAllUp float64 `json:"ri_3yr_all_upfront"`
	SP1yrNoUp  float64 `json:"sp_1yr_no_upfront"`
	SP1yrAllUp float64 `json:"sp_1yr_all_upfront"`
	SP3yrNoUp  float64 `json:"sp_3yr_no_upfront"`
	SP3yrAllUp float64 `json:"sp_3yr_all_upfront"`
}

type bookStorageClass struct {
	PerGiBMonth   float64 `json:"per_gib_month"`
	PerIOPSMonth  float64 `json:"per_iops_month"`
	PerMiBpsMonth float64 `json:"per_mibps_month"`
}

type bookRDSClass struct {
	Family           string  `json:"family"`
	VCPU             float64 `json:"vcpu"`
	MemoryGiB        float64 `json:"memory_gib"`
	Burstable        bool    `json:"burstable"`
	OnDemandSingleAZ float64 `json:"on_demand_single_az"`
}

type bookRDS struct {
	InstanceClasses   map[string]bookRDSClass `json:"instance_classes"`
	EngineMultiplier  map[string]float64      `json:"engine_multiplier"`
	MultiAZMultiplier float64                 `json:"multi_az_multiplier"`
	Storage           map[string]float64      `json:"storage"`
}

type bookCacheNode struct {
	VCPU      float64 `json:"vcpu"`
	MemoryGiB float64 `json:"memory_gib"`
	OnDemand  float64 `json:"on_demand"`
}

type bookElastiCache struct {
	NodeTypes        map[string]bookCacheNode `json:"node_types"`
	EngineMultiplier map[string]float64       `json:"engine_multiplier"`
}

// book is the full on-disk shape of pricebook.json.
type book struct {
	PricingDate       string                        `json:"pricing_date"`
	BaseRegion        string                        `json:"base_region"`
	RegionMultipliers map[string]float64            `json:"region_multipliers"`
	InstanceTypes     map[string]bookInstanceType   `json:"instance_types"`
	InstanceFamilies  map[string][]string           `json:"instance_families"`
	EBS               map[string]bookStorageClass   `json:"ebs"`
	S3                map[string]bookStorageClass   `json:"s3"`
	RDS               bookRDS                       `json:"rds"`
	ElastiCache       bookElastiCache               `json:"elasticache"`
	Services          map[string]map[string]float64 `json:"services"`
	DataTransfer      map[string]float64            `json:"data_transfer"`
}

// Catalog implements ports.PricingCatalog over the embedded price book.
// Every lookup key is normalized (trimmed, lowercased) at the boundary, so
// callers never have to worry about "M5.LARGE" vs "m5.large" — AWS's own
// APIs are inconsistent about case in exactly this way.
type Catalog struct {
	b           book
	pricingDate time.Time
}

var _ ports.PricingCatalog = (*Catalog)(nil)

// New loads the embedded price book. It panics on malformed embedded JSON,
// which is a build-time defect (the file is authored and committed by this
// package, never supplied at runtime) and should fail loudly rather than
// leave every caller silently degraded.
func New() *Catalog {
	var b book
	if err := json.Unmarshal(pricebookJSON, &b); err != nil {
		panic(fmt.Sprintf("pricing: embedded pricebook.json is malformed: %v", err))
	}
	date, err := time.Parse(time.RFC3339, b.PricingDate)
	if err != nil {
		panic(fmt.Sprintf("pricing: embedded pricebook.json has an invalid pricing_date: %v", err))
	}
	bridgeRDSStorage(&b)
	return &Catalog{b: b, pricingDate: date}
}

// bridgeRDSStorage folds the RDS storage rates into the same table
// StoragePrice/IOPSPrice already serve EBS and S3 from. PricingCatalog has
// one storage-price method family, not a dedicated RDS-storage method, so
// RDS's gp2/gp3/io1/backup rates are exposed as ordinary storage classes
// under an "rds_" prefix (rds_gp2, rds_gp3, rds_io1, rds_backup) rather than
// growing the port. The prefix exists only to avoid colliding with EBS's own
// gp2/gp3/io1 keys, whose per-GiB rate differs from RDS's.
func bridgeRDSStorage(b *book) {
	if b.EBS == nil {
		b.EBS = map[string]bookStorageClass{}
	}
	get := func(k string) float64 { return b.RDS.Storage[k] }
	b.EBS["rds_gp2"] = bookStorageClass{PerGiBMonth: get("gp2")}
	b.EBS["rds_gp3"] = bookStorageClass{PerGiBMonth: get("gp3")}
	b.EBS["rds_io1"] = bookStorageClass{PerGiBMonth: get("io1"), PerIOPSMonth: get("iops_per_month")}
	b.EBS["rds_backup"] = bookStorageClass{PerGiBMonth: get("backup_per_gib_month")}
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// regionMultiplier resolves a region to its price multiplier off us-east-1.
// An unrecognised region returns (0, false) rather than assuming parity with
// us-east-1 — silently pricing an unlisted region at US rates is exactly the
// kind of fabrication this package exists to avoid.
func (c *Catalog) regionMultiplier(region core.Region) (float64, bool) {
	r := norm(string(region))
	for k, v := range c.b.RegionMultipliers {
		if norm(k) == r {
			return v, true
		}
	}
	return 0, false
}

// platformMultiplier maps an OS/license platform onto its price premium over
// Linux. Only the platforms AWS actually meters separately are recognised;
// an unrecognised platform string returns false rather than silently billing
// it at the Linux rate, since a Windows or RHEL instance priced as Linux
// understates cost by 25-85%.
func platformMultiplier(platform string) (float64, bool) {
	switch norm(platform) {
	case "", "linux", "linux/unix":
		return 1.0, true
	case "windows":
		return 1.85, true
	case "rhel", "red hat enterprise linux":
		return 1.24, true
	case "suse", "suse linux":
		return 1.05, true
	default:
		return 0, false
	}
}

func (c *Catalog) instanceType(instanceType string) (bookInstanceType, bool) {
	key := norm(instanceType)
	if it, ok := c.b.InstanceTypes[key]; ok {
		return it, true
	}
	// Fall back to a case-insensitive scan: the primary map is already
	// lowercase-keyed by construction (the generator emits lowercase keys),
	// but a defensive scan protects against a future hand-edit of the JSON
	// that does not preserve that convention.
	for k, v := range c.b.InstanceTypes {
		if norm(k) == key {
			return v, true
		}
	}
	return bookInstanceType{}, false
}

// InstancePrice returns the hourly on-demand price for an instance type in a
// region, adjusted for platform (Linux, Windows, RHEL, SUSE).
func (c *Catalog) InstancePrice(region core.Region, instanceType string, platform string) (core.Money, bool) {
	it, ok := c.instanceType(instanceType)
	if !ok {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	pmul, ok := platformMultiplier(platform)
	if !ok {
		return core.Money{}, false
	}
	return core.USDollars(it.OnDemand * rmul * pmul), true
}

// SpotPrice returns a recent average spot price for an instance type. Spot
// prices are not platform-adjusted here beyond the Linux baseline the catalog
// stores: Windows spot markets exist but are thin enough that a single
// documented average would be misleading, so Windows/RHEL/SUSE spot lookups
// are intentionally unsupported and return false.
func (c *Catalog) SpotPrice(region core.Region, instanceType string) (core.Money, bool) {
	it, ok := c.instanceType(instanceType)
	if !ok {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	return core.USDollars(it.Spot * rmul), true
}

// storageLookup merges the EBS and S3 storage-class tables: the two key
// spaces never collide (gp2/gp3/io1/io2/st1/sc1/snapshot vs.
// standard/standard_ia/onezone_ia/intelligent_tiering/glacier_ir/
// glacier_flexible/deep_archive), so StoragePrice can serve both an EBS
// volume type and an S3 storage class through one method, matching the port.
func (c *Catalog) storageLookup(class string) (bookStorageClass, bool) {
	key := norm(class)
	if v, ok := c.b.EBS[key]; ok {
		return v, true
	}
	if v, ok := c.b.S3[key]; ok {
		return v, true
	}
	return bookStorageClass{}, false
}

// StoragePrice returns the per-GiB-month price for an EBS volume type or an
// S3 storage class.
func (c *Catalog) StoragePrice(region core.Region, storageClass string) (core.Money, bool) {
	sc, ok := c.storageLookup(storageClass)
	if !ok {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	return core.USDollars(sc.PerGiBMonth * rmul), true
}

// IOPSPrice returns the per-provisioned-IOPS-month price for gp3/io1/io2.
// Volume types with no provisioned-IOPS pricing (gp2, st1, sc1) return false.
func (c *Catalog) IOPSPrice(region core.Region, volumeType string) (core.Money, bool) {
	sc, ok := c.b.EBS[norm(volumeType)]
	if !ok || sc.PerIOPSMonth == 0 {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	return core.USDollars(sc.PerIOPSMonth * rmul), true
}

// ThroughputPrice returns the per-MiBps-month price for gp3-style volumes.
// Only gp3 bills throughput separately; every other volume type returns
// false.
func (c *Catalog) ThroughputPrice(region core.Region, volumeType string) (core.Money, bool) {
	sc, ok := c.b.EBS[norm(volumeType)]
	if !ok || sc.PerMiBpsMonth == 0 {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	return core.USDollars(sc.PerMiBpsMonth * rmul), true
}

// engineNorm maps the free-form engine strings the discoverer and the rule
// engine use onto the catalog's engine keys.
func engineNorm(engine string) string {
	e := norm(engine)
	e = strings.ReplaceAll(e, "_", "-")
	switch e {
	case "postgres", "postgresql":
		return "postgres"
	case "mysql":
		return "mysql"
	case "aurora-postgresql", "aurora postgresql":
		return "aurora-postgresql"
	case "aurora-mysql", "aurora mysql":
		return "aurora-mysql"
	default:
		return e
	}
}

// DatabasePrice returns the hourly single-AZ or multi-AZ price for an RDS or
// Aurora instance class and engine.
func (c *Catalog) DatabasePrice(region core.Region, instanceClass, engine string, multiAZ bool) (core.Money, bool) {
	rc, ok := c.b.RDS.InstanceClasses[norm(instanceClass)]
	if !ok {
		return core.Money{}, false
	}
	emul, ok := c.b.RDS.EngineMultiplier[engineNorm(engine)]
	if !ok {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	price := rc.OnDemandSingleAZ * emul * rmul
	if multiAZ {
		price *= c.b.RDS.MultiAZMultiplier
	}
	return core.USDollars(price), true
}

// CachePrice returns the hourly price for an ElastiCache node type and
// engine (redis or memcached).
func (c *Catalog) CachePrice(region core.Region, nodeType, engine string) (core.Money, bool) {
	nt, ok := c.b.ElastiCache.NodeTypes[norm(nodeType)]
	if !ok {
		return core.Money{}, false
	}
	emul, ok := c.b.ElastiCache.EngineMultiplier[norm(engine)]
	if !ok {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	return core.USDollars(nt.OnDemand * emul * rmul), true
}

// ServicePrice returns a unit price for a metered-service dimension, e.g.
// ("nat_gateway", "hours") or ("lambda", "gb_second"). For a small set of
// very-low-unit-cost, high-volume dimensions (lambda/request,
// dynamodb/on_demand_read, dynamodb/on_demand_write, sqs/requests,
// sns/requests, cloudfront/requests, api_gateway/rest_request,
// api_gateway/http_request, kinesis/put_units) the stored and returned price
// is per 1,000 units rather than per unit — a true per-unit price below
// roughly $0.000001 cannot survive core.Money's micro-dollar precision, and
// AWS's own pricing pages already quote most of these per thousand or per
// million for the same reason.
func (c *Catalog) ServicePrice(region core.Region, service, dimension string) (core.Money, bool) {
	dims, ok := c.b.Services[norm(service)]
	if !ok {
		return core.Money{}, false
	}
	price, ok := dims[norm(dimension)]
	if !ok {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	return core.USDollars(price * rmul), true
}

// DataTransferPrice returns the per-GB price for a transfer direction.
func (c *Catalog) DataTransferPrice(region core.Region, direction string) (core.Money, bool) {
	price, ok := c.b.DataTransfer[norm(direction)]
	if !ok {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	return core.USDollars(price * rmul), true
}

// InstanceSpec returns the capacity and generation of an instance type.
func (c *Catalog) InstanceSpec(instanceType string) (ports.InstanceSpec, bool) {
	it, ok := c.instanceType(instanceType)
	if !ok {
		return ports.InstanceSpec{}, false
	}
	return ports.InstanceSpec{
		Type:          norm(instanceType),
		Family:        it.Family,
		Size:          it.Size,
		Generation:    it.Generation,
		VCPU:          it.VCPU,
		MemoryGiB:     it.MemoryGiB,
		NetworkGbps:   it.NetworkGbps,
		EBSOptimized:  it.EBSOptimized,
		Architecture:  it.Architecture,
		Burstable:     it.Burstable,
		SuccessorType: it.Successor,
	}, true
}

// InstanceFamily returns every type in the same family as instanceType,
// ordered small to large — the candidate set a rightsizing decision walks.
func (c *Catalog) InstanceFamily(instanceType string) []string {
	it, ok := c.instanceType(instanceType)
	if !ok {
		return nil
	}
	list := c.b.InstanceFamilies[it.Family]
	out := make([]string, len(list))
	copy(out, list)
	return out
}

// CommitmentPrice returns the effective hourly rate under a commitment.
//
// term accepts "1yr"/"1_year"/"1" and "3yr"/"3_year"/"3" (matched by
// substring on the digit, so any of AWS's own spellings work without a strict
// enum). payment accepts "reserved_no_upfront", "reserved_all_upfront",
// "savings_plan_no_upfront" and "savings_plan_all_upfront" (matched loosely:
// containing "savings" selects a Savings Plan rate, containing "all" selects
// the all-upfront rate, otherwise no-upfront). Partial-upfront is
// deliberately not modelled: it sits between the two published rates and
// interpolating it would not change which commitment a rule recommends, so
// the catalog does not carry the extra schema for a distinction that has no
// decision riding on it.
func (c *Catalog) CommitmentPrice(region core.Region, instanceType, term, payment string) (core.Money, bool) {
	it, ok := c.instanceType(instanceType)
	if !ok {
		return core.Money{}, false
	}
	rmul, ok := c.regionMultiplier(region)
	if !ok {
		return core.Money{}, false
	}
	t := norm(term)
	p := norm(payment)
	savingsPlan := strings.Contains(p, "saving") || strings.Contains(p, "sp")
	allUpfront := strings.Contains(p, "all")
	var rate float64
	switch {
	case strings.Contains(t, "1") && !strings.Contains(t, "3"):
		switch {
		case savingsPlan && allUpfront:
			rate = it.SP1yrAllUp
		case savingsPlan:
			rate = it.SP1yrNoUp
		case allUpfront:
			rate = it.RI1yrAllUp
		default:
			rate = it.RI1yrNoUp
		}
	case strings.Contains(t, "3"):
		switch {
		case savingsPlan && allUpfront:
			rate = it.SP3yrAllUp
		case savingsPlan:
			rate = it.SP3yrNoUp
		case allUpfront:
			rate = it.RI3yrAllUp
		default:
			rate = it.RI3yrNoUp
		}
	default:
		return core.Money{}, false
	}
	if rate == 0 {
		return core.Money{}, false
	}
	return core.USDollars(rate * rmul), true
}

// PricingDate reports how fresh the catalog is.
func (c *Catalog) PricingDate() time.Time { return c.pricingDate }
