package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// This file computes optimize.BlastRadius by walking the architecture
// graph, never by estimation. cloud.Topology.Dependents already does the
// hard part (transitive closure along request-path edges, discounted by
// edge confidence); this file turns that node set into the counts, the
// score and — the part that matters most — a Completeness figure that keeps
// a blast radius computed on a thin graph from ever reading as a small one.
//
// Traceability: REQ-OPT-008, SPEC-OPT-005.

const (
	blastMaxDepth = 6
	// Reference counts a resourcesAffected/servicesAffected/criticalServices
	// figure saturates against. These are deliberately modest: a change that
	// ripples to ten downstream resources or two critical services is already
	// a large blast radius for a single optimization action, and the curve
	// should be near its ceiling well before an estate's largest fan-outs.
	refResources = 10.0
	refServices  = 4.0
	refCritical  = 2.0
	refAPIs      = 3.0
	// assumedRequestsPerUserMonth converts a declared transaction volume into
	// an estimated user count when no better signal exists. It is a labelled
	// assumption, not a measurement — see the doc on EstimatedUsers below.
	assumedRequestsPerUserMonth = 40.0
)

// ComputeBlastRadius walks the dependency graph from one resource and
// summarises what an action against it could affect if it goes wrong.
func ComputeBlastRadius(ctx EvalContext, r cloud.Resource) optimize.BlastRadius {
	deps := ctx.Topology.Dependents(r.ID, blastMaxDepth)

	b := optimize.BlastRadius{ResourcesAffected: len(deps)}

	workloads := map[core.ID]bool{}
	criticalWorkloads := map[core.ID]bool{}
	envs := map[core.Environment]bool{r.Environment: true}
	apis := 0
	crossAccount := false
	var confSum float64

	for id, conf := range deps {
		confSum += float64(conf)
		dep, ok := ctx.Inventory.ByID(id)
		if !ok {
			continue
		}
		if !dep.WorkloadID.IsZero() {
			workloads[dep.WorkloadID] = true
			if dep.Criticality == core.CriticalityTier0 || dep.Criticality == core.CriticalityTier1 {
				criticalWorkloads[dep.WorkloadID] = true
			}
		}
		envs[dep.Environment] = true
		if dep.AccountID != "" && dep.AccountID != r.AccountID {
			crossAccount = true
		}
		switch dep.Kind {
		case cloud.KindALB, cloud.KindNLB, cloud.KindAPIGateway, cloud.KindCloudFront:
			apis++
		}
	}

	b.ServicesAffected = len(workloads)
	b.CriticalServices = len(criticalWorkloads)
	b.APIsAffected = apis
	b.CrossAccount = crossAccount
	for e := range envs {
		if e != "" {
			b.EnvironmentsAffected = append(b.EnvironmentsAffected, e)
		}
	}

	// Completeness. Zero dependents is ambiguous: it can mean "this resource
	// is genuinely a leaf" or "the topology has not resolved this resource's
	// edges yet". A compute or database resource with no discovered edges at
	// all is far more likely to be the second case in any estate with more
	// than a handful of resources, so it is scored conservatively rather than
	// as a confirmed-isolated (and therefore falsely reassuring) leaf.
	switch {
	case ctx.Topology.Len() == 0:
		b.Completeness = 0.1
	case len(deps) == 0:
		if r.Kind.Category() == cloud.CategoryCompute || r.Kind.Category() == cloud.CategoryDatabase {
			b.Completeness = 0.35
		} else {
			b.Completeness = 0.9
		}
	default:
		b.Completeness = confSum / float64(len(deps))
	}

	tx := matchedTransactions(ctx.Spec, r)
	var totalVolume float64
	for _, t := range tx {
		b.TransactionsAffected = append(b.TransactionsAffected, t.Name)
		totalVolume += t.MonthlyVolume
	}
	if totalVolume > 0 {
		// Labelled assumption: a monthly transaction volume is converted to a
		// user count at a fixed requests-per-user rate because CloudOptix has
		// no per-tenant measurement of that ratio at rule-evaluation time (it
		// would require the fully-resolved economics graph, which this
		// context does not carry). It is documented here rather than silently
		// baked into the number so a reviewer can see exactly where it came
		// from.
		b.EstimatedUsers = int64(totalVolume / assumedRequestsPerUserMonth)
	} else if b.CriticalServices > 0 && ctx.Spec.Business.CustomerCount > 0 {
		share := 0.1 * float64(b.CriticalServices)
		if share > 1 {
			share = 1
		}
		b.EstimatedUsers = int64(float64(ctx.Spec.Business.CustomerCount) * share)
	}

	baseScore := 0.35*saturate(float64(b.ResourcesAffected), refResources) +
		0.25*saturate(float64(b.ServicesAffected), refServices) +
		0.10*saturate(float64(b.APIsAffected), refAPIs) +
		0.30*saturate(float64(b.CriticalServices), refCritical)

	// Low completeness pushes the score UP, never down: a blast radius
	// computed on a partially-observed graph must never read as smaller than
	// one computed on a complete graph, so missing information is treated as
	// the conservative (higher-risk) case rather than averaged away.
	uncertaintyBump := (1 - b.Completeness) * 0.3
	b.Score = clamp01(baseScore + uncertaintyBump)
	b.Level = core.RiskLevelFromScore(b.Score)
	b.Explanation = fmt.Sprintf(
		"%d dependents (%d services, %d critical) across %d environment(s); graph %.0f%% observable",
		b.ResourcesAffected, b.ServicesAffected, b.CriticalServices, len(b.EnvironmentsAffected), b.Completeness*100)
	if b.Completeness < 0.5 {
		b.Explanation += " — low graph coverage; treated as higher risk, not a small blast radius"
	}
	return b
}

func saturate(x, ref float64) float64 {
	if ref <= 0 {
		return 0
	}
	v := x / ref
	if v > 1 {
		return 1
	}
	if v < 0 {
		return 0
	}
	return v
}
