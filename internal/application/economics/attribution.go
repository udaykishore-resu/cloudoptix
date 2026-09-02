package economics

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Footprint computes (or serves a fresh-enough persisted copy of) the
// economic footprint for one scope entity over a period, then persists what
// it computed so ListFootprints and the SLO evaluator can read it back
// without recomputing.
func (s *Service) Footprint(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID, period core.Period) (econ.Footprint, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.Footprint{}, err
	}
	if period.IsZero() {
		period = s.defaultPeriod()
	}
	if cached, err := s.Repos.Economics.GetFootprint(ctx, tenant, scope, scopeID, period); err == nil {
		return cached, nil
	}
	fp, err := s.computeFootprintCore(ctx, tenant, scope, scopeID, period, true)
	if err != nil {
		return econ.Footprint{}, err
	}
	// A live-computed footprint is persisted as a side effect so the next
	// caller in this same period — very likely the SLO evaluator or the
	// dashboard a moment later — gets GetFootprint's cheap path instead of
	// paying for the full walk again.
	_ = s.Repos.Economics.SaveFootprints(ctx, tenant, []econ.Footprint{fp})
	return fp, nil
}

// computeFootprintCore runs the attribution algorithm described in the
// package doc without touching the persisted-footprint cache, so Compute can
// call it directly for every scope entity in one pass instead of round
// tripping through Footprint's cache-then-save dance N times.
//
// withPrior asks it to also resolve the immediately preceding period of the
// same length for the PriorTotal/ChangePct comparison; that lookup calls
// back into this same function with withPrior=false, which is what keeps the
// recursion at exactly one level deep instead of walking every period back
// to the beginning of time.
func (s *Service) computeFootprintCore(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID, period core.Period, withPrior bool) (econ.Footprint, error) {
	own, label, err := s.resourcesForScope(ctx, tenant, scope, scopeID)
	if err != nil {
		return econ.Footprint{}, err
	}

	// The attribution walk needs the whole graph, not just the scope's own
	// slice of it — Consumers and the depends_on/routes_to walk both reach
	// outside the scope by definition, into resources this scope does not
	// own but is either a measured consumer of or a structural dependent of.
	fullInv, err := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{})
	if err != nil {
		return econ.Footprint{}, err
	}
	topo, err := s.Repos.Resources.LoadTopology(ctx, tenant, ports.ResourceFilter{})
	if err != nil {
		return econ.Footprint{}, err
	}
	byResource, err := s.Repos.Costs.ByResource(ctx, tenant, ports.CostFilter{Period: period})
	if err != nil {
		byResource = map[core.ID]core.Money{}
	}

	// costOf prefers the period's actual billed amount (what really landed
	// on the invoice) and falls back to the resource's denormalized run-rate
	// prorated to the period length only when no billing has been joined to
	// it yet — e.g. a resource discovered since the last cost ingestion.
	periodDays := period.Days()
	if periodDays <= 0 {
		periodDays = core.AverageDaysPerMonth
	}
	costOf := func(r cloud.Resource) core.Money {
		if amt, ok := byResource[r.ID]; ok {
			return amt
		}
		return r.MonthlyCost.Scale(periodDays / core.AverageDaysPerMonth)
	}

	ownWeight := make(map[core.ID]float64, len(own))
	if scope == econ.ScopeTransaction {
		tx, terr := s.Repos.Economics.GetTransaction(ctx, tenant, scopeID)
		allTx, _ := s.Repos.Economics.ListTransactions(ctx, tenant)
		for _, r := range own {
			w := 1.0
			if terr == nil {
				w = workloadShare(tx, allTx, r.WorkloadID)
			}
			ownWeight[r.ID] = w
		}
	} else {
		for _, r := range own {
			ownWeight[r.ID] = 1.0
		}
	}

	var components []econ.Component
	for _, r := range own {
		w := ownWeight[r.ID]
		basis := "exclusive owner"
		if w < 0.999 {
			basis = fmt.Sprintf("%.0f%% measured path share of the owning workload", w*100)
		}
		components = append(components, econ.Component{
			ResourceID: r.ID, ResourceName: r.DisplayName(), Kind: string(r.Kind), Service: r.Kind.Service(),
			Class: econ.ClassDirect, Amount: costOf(r).Scale(w), AllocationShare: w, Basis: basis,
			Provenance: core.ProvenanceConfirmed,
		})
	}

	// Indirect and shared cost: for every resource anywhere in the estate
	// that records consumers (shared_by/runs_on/egress_via/attached_to
	// inbound edges), sum the share attributed to consumers this scope owns.
	// A component with exactly one consumer overall is Indirect — the whole
	// of its cost was caused by one thing, which happens to be this scope's
	// own; more than one consumer makes it genuinely Shared, and this scope
	// only ever books its measured slice.
	handledExternal := map[core.ID]bool{}
	for _, r := range fullInv.All() {
		if _, isOwn := ownWeight[r.ID]; isOwn {
			continue
		}
		consumers := topo.Consumers(r.ID)
		if len(consumers) == 0 {
			continue
		}
		var scopeShare float64
		for consumerID, share := range consumers {
			if w, ok := ownWeight[consumerID]; ok {
				scopeShare += share * w
			}
		}
		if scopeShare <= 0 {
			continue
		}
		class := econ.ClassIndirect
		basis := "exclusively caused by this scope"
		if len(consumers) > 1 {
			class = econ.ClassShared
			basis = fmt.Sprintf("%.1f%% measured share across %d consumers", scopeShare*100, len(consumers))
		}
		components = append(components, econ.Component{
			ResourceID: r.ID, ResourceName: r.DisplayName(), Kind: string(r.Kind), Service: r.Kind.Service(),
			Class: class, Amount: costOf(r).Scale(scopeShare), AllocationShare: scopeShare, Basis: basis,
			Provenance: core.ProvenanceInferred,
		})
		handledExternal[r.ID] = true
	}

	// Unattributed: a resource this scope's own resources structurally
	// depend on (depends_on/routes_to — the request-path edges, not the
	// cost-carrying ones already walked above) but for which no consumer
	// edge was ever recorded. Its full cost is surfaced, once per scope
	// regardless of how many owned resources reach it, rather than guessed
	// at — see the package doc for why an even split is worse than a gap.
	unattributed := core.ZeroUSD()
	seenUnattributed := map[core.ID]bool{}
	for _, r := range own {
		for _, e := range topo.Outbound(r.ID, cloud.RelDependsOn, cloud.RelRoutesTo) {
			target := e.ToID
			if _, isOwn := ownWeight[target]; isOwn {
				continue // an edge within the scope, already counted as Direct
			}
			if handledExternal[target] || seenUnattributed[target] {
				continue
			}
			tr, ok := fullInv.ByID(target)
			if !ok {
				continue
			}
			seenUnattributed[target] = true
			unattributed = unattributed.MustAdd(costOf(tr))
		}
	}

	fp := econ.NewFootprint(tenant, scope, scopeID, label, period, components, unattributed)

	if withPrior {
		// Prior-period comparison: the immediately preceding window of the
		// same length, computed as a second, independent walk (not derived
		// from this one) so a change in the graph between the two periods is
		// reflected honestly rather than assumed away.
		priorPeriod := core.Period{Start: period.Start.Add(-period.Duration()), End: period.Start}
		if prior, perr := s.computeFootprintCore(ctx, tenant, scope, scopeID, priorPeriod, false); perr == nil {
			fp.PriorTotal = prior.Total
			if !prior.Total.IsZero() {
				fp.ChangePct = fp.Total.MustSub(prior.Total).Ratio(prior.Total) * 100
			}
		}
	}
	return fp, nil
}

// workloadShare resolves the fraction of a workload's cost this transaction
// is responsible for. An explicit PathShare wins; absent one, the default is
// an even split across every transaction that also names the workload on its
// critical path — "unmeasured" is not the same as "exclusive", and crediting
// a shared workload's full cost to every transaction that touches it would
// massively overcount the estate's spend.
func workloadShare(tx econ.BusinessTransaction, all []econ.BusinessTransaction, workloadID core.ID) float64 {
	if workloadID.IsZero() {
		return 1
	}
	if tx.PathShare != nil {
		if share, ok := tx.PathShare[workloadID]; ok && share > 0 {
			return share
		}
	}
	sharers := 0
	for _, other := range all {
		for _, w := range other.WorkloadIDs {
			if w == workloadID {
				sharers++
				break
			}
		}
	}
	if sharers <= 1 {
		return 1
	}
	return 1.0 / float64(sharers)
}
