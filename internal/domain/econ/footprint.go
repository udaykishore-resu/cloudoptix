// Package econ is CloudOptix's differentiator in code: the Architecture
// Economics model.
//
// A billing tool answers "Amazon RDS cost $24,500 last month". This package
// answers "the checkout capability cost $61,200 last month, of which $24,500
// was its database, $8,700 was NAT egress it caused, and $4,100 was its share
// of the shared observability platform — which is $0.0061 per checkout, up 14%
// because the p95 basket size grew".
//
// The three ideas that make that possible:
//
//  1. Cost is attributed along the architecture graph, not along the tag that
//     happens to be on the invoice line. Shared components split by measured
//     consumption; egress charges follow the workload that caused the traffic.
//  2. Every attributed figure records its provenance and the share of the
//     estate that could not be attributed. An unattributed remainder is shown,
//     never hidden by spreading it evenly.
//  3. Business denominators — transactions, customers, requests — are
//     first-class, so the unit economics are a stored, tracked metric rather
//     than a spreadsheet someone maintains.
//
// Traceability: REQ-ECON-001..012, SPEC-ECON-001.
package econ

import (
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Scope names what an economic footprint is computed for. The same algorithm
// serves every level; only the membership set changes.
type Scope string

const (
	ScopeOrganization       Scope = "organization"
	ScopeAccount            Scope = "account"
	ScopeEnvironment        Scope = "environment"
	ScopeApplication        Scope = "application"
	ScopeWorkload           Scope = "workload"
	ScopeBusinessCapability Scope = "business_capability"
	ScopeAPI                Scope = "api"
	ScopeTransaction        Scope = "transaction"
	ScopeResource           Scope = "resource"
)

// CostClass separates the three ways spend reaches a workload. Reporting them
// separately is what lets an architect see that a service is cheap to run but
// expensive to surround.
type CostClass string

const (
	// ClassDirect is spend on resources the workload exclusively owns: its
	// own instances, its own database, its own volumes.
	ClassDirect CostClass = "direct"
	// ClassIndirect is spend the workload demonstrably caused on resources it
	// does not own: NAT data processing for its egress, cross-AZ transfer
	// between its replicas, load balancer LCU consumption driven by its
	// traffic, log ingestion for its log volume.
	ClassIndirect CostClass = "indirect"
	// ClassShared is the workload's apportioned share of genuinely common
	// platform: the EKS control plane, the shared observability stack, the
	// transit gateway, the CI account.
	ClassShared CostClass = "shared"
)

// Component is one contributing line in a footprint, retained so that every
// aggregate number can be expanded to the resources behind it. This is what
// makes the copilot's answers auditable rather than assertive.
type Component struct {
	ResourceID   core.ID    `json:"resource_id,omitempty"`
	ResourceName string     `json:"resource_name,omitempty"`
	Kind         string     `json:"kind,omitempty"`
	Service      string     `json:"service,omitempty"`
	Class        CostClass  `json:"class"`
	Amount       core.Money `json:"amount"`
	// AllocationShare is the fraction of the resource's cost attributed here.
	// 1.0 for exclusively owned resources, less for shared ones.
	AllocationShare float64 `json:"allocation_share"`
	// Basis explains the allocation in one phrase, e.g. "measured NAT bytes",
	// "pod CPU request share", "exclusive owner". It is rendered verbatim in
	// the UI's allocation tooltip.
	Basis      string          `json:"basis"`
	Provenance core.Provenance `json:"provenance"`
}

// Footprint is the total economic weight of a scope over a period.
type Footprint struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	Scope    Scope         `json:"scope"`
	ScopeID  core.ID       `json:"scope_id"`
	Label    string        `json:"label"`
	Period   core.Period   `json:"period"`

	Direct   core.Money `json:"direct"`
	Indirect core.Money `json:"indirect"`
	Shared   core.Money `json:"shared"`
	Total    core.Money `json:"total"`

	// Unattributed is the spend inside the scope's blast area that CloudOptix
	// could not confidently assign. It is surfaced rather than absorbed: a
	// footprint with 30% unattributed cost is a data-quality finding, not a
	// number to act on.
	Unattributed core.Money `json:"unattributed"`
	// Coverage is 1 - Unattributed/(Total+Unattributed): the share of the
	// relevant spend this footprint explains.
	Coverage float64 `json:"coverage"`

	Components []Component `json:"components,omitempty"`
	// ByService and ByClass are precomputed roll-ups because every consumer
	// wants them and recomputing per request dominated p95 in profiling.
	ByService map[string]core.Money    `json:"by_service,omitempty"`
	ByClass   map[CostClass]core.Money `json:"by_class,omitempty"`

	PriorTotal core.Money `json:"prior_total,omitempty"`
	ChangePct  float64    `json:"change_pct,omitempty"`

	ComputedAt time.Time       `json:"computed_at"`
	Confidence core.Confidence `json:"confidence"`
}

// NewFootprint aggregates components into a footprint, computing the class
// splits, service roll-up and coverage.
func NewFootprint(tenant core.TenantID, scope Scope, scopeID core.ID, label string, period core.Period, components []Component, unattributed core.Money) Footprint {
	f := Footprint{
		ID:           core.NewID("fp"),
		TenantID:     tenant,
		Scope:        scope,
		ScopeID:      scopeID,
		Label:        label,
		Period:       period,
		Components:   components,
		Direct:       core.ZeroUSD(),
		Indirect:     core.ZeroUSD(),
		Shared:       core.ZeroUSD(),
		Unattributed: unattributed,
		ByService:    map[string]core.Money{},
		ByClass:      map[CostClass]core.Money{},
		ComputedAt:   time.Now().UTC(),
	}
	for _, c := range components {
		switch c.Class {
		case ClassDirect:
			f.Direct = f.Direct.MustAdd(c.Amount)
		case ClassIndirect:
			f.Indirect = f.Indirect.MustAdd(c.Amount)
		case ClassShared:
			f.Shared = f.Shared.MustAdd(c.Amount)
		}
		f.ByClass[c.Class] = f.ByClass[c.Class].MustAdd(c.Amount)
		if c.Service != "" {
			f.ByService[c.Service] = f.ByService[c.Service].MustAdd(c.Amount)
		}
	}
	f.Total = f.Direct.MustAdd(f.Indirect).MustAdd(f.Shared)
	relevant := f.Total.MustAdd(unattributed)
	if !relevant.IsZero() {
		f.Coverage = f.Total.Ratio(relevant)
	} else {
		f.Coverage = 1
	}
	// Confidence in a footprint is dominated by coverage but also penalised
	// when a large share of it is allocated rather than owned outright.
	allocatedShare := 0.0
	if !f.Total.IsZero() {
		allocatedShare = f.Shared.MustAdd(f.Indirect).Ratio(f.Total)
	}
	f.Confidence = core.Confidence(f.Coverage * (1 - 0.25*allocatedShare)).Clamp()
	return f
}

// TopComponents returns the n largest contributors.
func (f Footprint) TopComponents(n int) []Component {
	out := make([]Component, len(f.Components))
	copy(out, f.Components)
	sort.Slice(out, func(i, j int) bool { return out[i].Amount.Micros() > out[j].Amount.Micros() })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// MonthlyRunRate normalises the footprint to a monthly figure regardless of
// the period it was computed over.
func (f Footprint) MonthlyRunRate() core.Money {
	days := f.Period.Days()
	if days <= 0 {
		return f.Total
	}
	return f.Total.Div(days).Scale(core.AverageDaysPerMonth)
}

// BusinessTransaction is a customer-defined unit of business work. Defining
// one is how a tenant tells CloudOptix what its denominator is.
type BusinessTransaction struct {
	ID            core.ID       `json:"id"`
	TenantID      core.TenantID `json:"tenant_id"`
	Name          string        `json:"name"` // checkout, payment, claim, search
	Description   string        `json:"description,omitempty"`
	ApplicationID core.ID       `json:"application_id,omitempty"`
	// WorkloadIDs are the workloads on this transaction's critical path. Their
	// footprints, weighted by PathShare, form the transaction's cost.
	WorkloadIDs []core.ID `json:"workload_ids"`
	// PathShare is the fraction of each workload's capacity this transaction
	// consumes, defaulting to an even split when unmeasured. It is what stops
	// a shared service being counted at full cost in five transactions.
	PathShare map[core.ID]float64 `json:"path_share,omitempty"`
	// VolumeSource says where the transaction count comes from: a CloudWatch
	// metric, an API Gateway count, or a figure the tenant declares.
	VolumeSource VolumeSource     `json:"volume_source"`
	Provenance   core.Provenance  `json:"provenance"`
	Criticality  core.Criticality `json:"criticality"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

// VolumeSource describes how transaction volume is measured.
type VolumeSource struct {
	Kind       string            `json:"kind"` // "cloudwatch" | "declared" | "prometheus" | "alb_requests"
	Namespace  string            `json:"namespace,omitempty"`
	MetricName string            `json:"metric_name,omitempty"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
	// DeclaredMonthly is used when Kind is "declared": the tenant states a
	// volume during onboarding before any instrumentation exists.
	DeclaredMonthly float64 `json:"declared_monthly,omitempty"`
}

// UnitEconomics is the tracked cost-per-unit metric for a transaction. It is
// stored as a time series so that a regression is visible as a trend rather
// than discovered during a quarterly review.
type UnitEconomics struct {
	ID            core.ID       `json:"id"`
	TenantID      core.TenantID `json:"tenant_id"`
	TransactionID core.ID       `json:"transaction_id"`
	Name          string        `json:"name"`
	Period        core.Period   `json:"period"`

	Volume        float64    `json:"volume"`
	TotalCost     core.Money `json:"total_cost"`
	CostPerUnit   core.Money `json:"cost_per_unit"`
	DirectPerUnit core.Money `json:"direct_per_unit"`
	SharedPerUnit core.Money `json:"shared_per_unit"`

	PriorCostPerUnit core.Money `json:"prior_cost_per_unit,omitempty"`
	ChangePct        float64    `json:"change_pct,omitempty"`

	// Drivers explains a movement: volume changed, unit cost changed, or the
	// mix of contributing services changed. Without this decomposition a
	// rising cost-per-transaction is unactionable, because falling volume and
	// rising cost look identical in the headline number.
	Drivers          []Driver        `json:"drivers,omitempty"`
	Confidence       core.Confidence `json:"confidence"`
	VolumeProvenance core.Provenance `json:"volume_provenance"`
	ComputedAt       time.Time       `json:"computed_at"`
}

// Driver is one decomposed cause of a unit-economics movement.
type Driver struct {
	Kind        string     `json:"kind"` // "volume" | "unit_cost" | "mix" | "new_component"
	Label       string     `json:"label"`
	Impact      core.Money `json:"impact"`
	ImpactShare float64    `json:"impact_share"`
	Explanation string     `json:"explanation"`
}

// ComputeUnitEconomics divides a footprint by a measured volume. A zero or
// unknown volume yields a zero cost-per-unit with the confidence collapsed to
// zero rather than a division-by-zero or a fabricated denominator.
func ComputeUnitEconomics(tx BusinessTransaction, f Footprint, volume float64, volumeProv core.Provenance) UnitEconomics {
	ue := UnitEconomics{
		ID:               core.NewID("ue"),
		TenantID:         f.TenantID,
		TransactionID:    tx.ID,
		Name:             tx.Name,
		Period:           f.Period,
		Volume:           volume,
		TotalCost:        f.Total,
		VolumeProvenance: volumeProv,
		ComputedAt:       time.Now().UTC(),
	}
	if volume <= 0 {
		ue.Confidence = 0
		return ue
	}
	ue.CostPerUnit = f.Total.Div(volume)
	ue.DirectPerUnit = f.Direct.Div(volume)
	ue.SharedPerUnit = f.Shared.MustAdd(f.Indirect).Div(volume)

	// Confidence is the footprint's confidence discounted by how the volume
	// was obtained. A declared volume from onboarding is a starting point, not
	// a measurement, and the UI must say so.
	volFactor := 1.0
	switch volumeProv {
	case core.ProvenanceConfirmed:
		volFactor = 1.0
	case core.ProvenanceInferred:
		volFactor = 0.75
	case core.ProvenanceRequiresConfirmation:
		volFactor = 0.6
	default:
		volFactor = 0.4
	}
	ue.Confidence = core.Confidence(float64(f.Confidence) * volFactor).Clamp()
	return ue
}

// DecomposeChange explains the movement between two unit-economics
// observations, splitting it into a volume effect and a unit-cost effect using
// the standard price/volume variance decomposition.
func DecomposeChange(prior, current UnitEconomics) []Driver {
	if prior.Volume <= 0 || current.Volume <= 0 {
		return nil
	}
	totalDelta := current.TotalCost.MustSub(prior.TotalCost)
	if totalDelta.IsZero() {
		return nil
	}
	// Volume effect: prior unit cost applied to the volume change.
	volumeEffect := prior.CostPerUnit.Scale(current.Volume - prior.Volume)
	// Unit-cost effect: the unit-cost change applied to current volume.
	unitEffect := current.CostPerUnit.MustSub(prior.CostPerUnit).Scale(current.Volume)

	drivers := []Driver{
		{
			Kind:        "volume",
			Label:       "Transaction volume",
			Impact:      volumeEffect,
			Explanation: describeVolume(prior.Volume, current.Volume),
		},
		{
			Kind:        "unit_cost",
			Label:       "Cost per transaction",
			Impact:      unitEffect,
			Explanation: describeUnit(prior.CostPerUnit, current.CostPerUnit),
		},
	}
	magnitude := volumeEffect.Abs().MustAdd(unitEffect.Abs())
	for i := range drivers {
		if !magnitude.IsZero() {
			drivers[i].ImpactShare = drivers[i].Impact.Abs().Ratio(magnitude)
		}
	}
	sort.Slice(drivers, func(i, j int) bool {
		return drivers[i].Impact.Abs().Micros() > drivers[j].Impact.Abs().Micros()
	})
	return drivers
}

func describeVolume(prior, current float64) string {
	if prior == 0 {
		return "volume newly measured"
	}
	pct := (current - prior) / prior * 100
	if pct >= 0 {
		return formatPct("volume rose %.1f%%", pct)
	}
	return formatPct("volume fell %.1f%%", -pct)
}

func describeUnit(prior, current core.Money) string {
	if prior.IsZero() {
		return "unit cost newly measured"
	}
	pct := (current.Units() - prior.Units()) / prior.Units() * 100
	if pct >= 0 {
		return formatPct("unit cost rose %.1f%%", pct)
	}
	return formatPct("unit cost fell %.1f%%", -pct)
}
