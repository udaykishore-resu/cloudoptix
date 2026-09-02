package twin

import (
	"fmt"
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// minGroupSize is the smallest number of similar leaves worth replacing with
// one synthetic node. Below this, collapsing would hide detail without
// meaningfully reducing the graph's size.
const minGroupSize = 5

// minProtectedCost is the absolute floor for the cost-protection threshold —
// see costProtectionThreshold — so that in a small or cheap graph, "2% of
// total" does not work out to a few cents and protect nothing at all.
const minProtectedCost = 50.0 // USD/month

// costProtectionThreshold is the cost above which a node is never folded
// into a collapsed group, regardless of its connectivity. It scales with the
// graph's own total rather than being a fixed rank (a "top 25 by cost" rule
// breaks down the moment more than 25 nodes share the same cost, which is
// exactly the common case — a thousand identical $1/month EBS snapshots),
// so instead a node earns protection by being expensive relative to the
// estate it sits in.
func costProtectionThreshold(nodes []ports.TwinNode) core.Money {
	total := core.ZeroUSD()
	for _, n := range nodes {
		total = total.MustAdd(n.MonthlyCost)
	}
	pct := total.Scale(0.02)
	floor := core.USDollars(minProtectedCost)
	if pct.GreaterThan(floor) {
		return pct
	}
	return floor
}

// collapseLowValueLeaves groups low-cost, low-connectivity nodes that share a
// kind, account and region into single synthetic nodes, so a 40,000-resource
// estate — mostly small, similar, uninteresting things — renders as a graph
// a human can actually look at. It returns the reduced node and edge sets and
// how many original nodes were folded away.
func collapseLowValueLeaves(nodes []ports.TwinNode, edges []ports.TwinEdge, root core.ID) ([]ports.TwinNode, []ports.TwinEdge, int) {
	degree := make(map[core.ID]int, len(nodes))
	for _, e := range edges {
		degree[e.From]++
		degree[e.To]++
	}

	threshold := costProtectionThreshold(nodes)
	protected := map[core.ID]bool{}
	for _, n := range nodes {
		if n.MonthlyCost.GreaterThan(threshold) {
			protected[n.ID] = true
		}
	}
	if !root.IsZero() {
		protected[root] = true
	}

	type groupKey struct {
		kind      cloud.Kind
		accountID core.AccountID
		region    core.Region
	}
	groups := map[groupKey][]ports.TwinNode{}
	for _, n := range nodes {
		if protected[n.ID] || n.Group {
			continue
		}
		if degree[n.ID] > 1 {
			continue // only leaves (zero or one connection) are candidates
		}
		key := groupKey{kind: n.Kind, accountID: n.AccountID, region: n.Region}
		groups[key] = append(groups[key], n)
	}

	toCollapse := map[core.ID]bool{}
	var synthetic []ports.TwinNode
	for key, members := range groups {
		if len(members) < minGroupSize {
			continue
		}
		total := core.ZeroUSD()
		for _, m := range members {
			toCollapse[m.ID] = true
			total = total.MustAdd(m.MonthlyCost)
		}
		synthetic = append(synthetic, ports.TwinNode{
			ID: syntheticGroupID(key.kind, key.accountID, key.region), Label: fmt.Sprintf("%d × %s", len(members), key.kind),
			Kind: key.kind, Category: key.kind.Category(), Service: key.kind.Service(),
			AccountID: key.accountID, Region: key.region, MonthlyCost: total,
			Group: true, GroupCount: len(members),
		})
	}

	if len(toCollapse) == 0 {
		return nodes, edges, 0
	}

	outNodes := make([]ports.TwinNode, 0, len(nodes)-len(toCollapse)+len(synthetic))
	for _, n := range nodes {
		if toCollapse[n.ID] {
			continue
		}
		outNodes = append(outNodes, n)
	}
	outNodes = append(outNodes, synthetic...)

	groupIDFor := func(n ports.TwinNode) core.ID {
		return syntheticGroupID(n.Kind, n.AccountID, n.Region)
	}
	byID := make(map[core.ID]ports.TwinNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	seenEdge := map[[2]core.ID]bool{}
	outEdges := make([]ports.TwinEdge, 0, len(edges))
	for _, e := range edges {
		from, to := e.From, e.To
		if toCollapse[from] {
			from = groupIDFor(byID[from])
		}
		if toCollapse[to] {
			to = groupIDFor(byID[to])
		}
		if from == to {
			continue // an edge entirely inside one collapsed group
		}
		k := [2]core.ID{from, to}
		if seenEdge[k] {
			continue
		}
		seenEdge[k] = true
		e.From, e.To = from, to
		outEdges = append(outEdges, e)
	}

	return outNodes, outEdges, len(toCollapse)
}

func syntheticGroupID(kind cloud.Kind, accountID core.AccountID, region core.Region) core.ID {
	return core.ID(fmt.Sprintf("group_%s_%s_%s", kind, accountID, region))
}

// truncateToTop keeps the root (if any) plus the hardCap-1 highest-cost
// remaining nodes, dropping the rest. It is the last resort when even
// collapsing leaves the graph too large to render.
func truncateToTop(nodes []ports.TwinNode, edges []ports.TwinEdge, hardCap int, root core.ID) ([]ports.TwinNode, []ports.TwinEdge) {
	if hardCap <= 0 || len(nodes) <= hardCap {
		return nodes, edges
	}
	sorted := append([]ports.TwinNode(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].MonthlyCost.Micros() > sorted[j].MonthlyCost.Micros() })

	keep := map[core.ID]bool{}
	if !root.IsZero() {
		keep[root] = true
	}
	for _, n := range sorted {
		if len(keep) >= hardCap {
			break
		}
		keep[n.ID] = true
	}
	var outNodes []ports.TwinNode
	for _, n := range nodes {
		if keep[n.ID] {
			outNodes = append(outNodes, n)
		}
	}
	var outEdges []ports.TwinEdge
	for _, e := range edges {
		if keep[e.From] && keep[e.To] {
			outEdges = append(outEdges, e)
		}
	}
	return outNodes, outEdges
}
