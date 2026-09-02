package twin

import (
	"context"
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// CostFlow builds the Sankey-style money-flow projection.
//
// Direction: for every edge kind that cloud.RelationKind.CarriesCost reports
// (contains, runs_on, attached_to, egress_via, shared_by), cost is treated as
// flowing in the edge's own FromID→ToID direction — the direction discovery
// already produces those edges in (a volume attached_to an instance, a
// workload egress_via a NAT gateway). This is a simplification: for a couple
// of these kinds the resource that causes the cost is not always the one
// that would benefit from a fuller reverse-attribution split (that
// weighted, consumption-based reversal is exactly what package econ's
// shared-cost attribution does, deliberately, as the authoritative answer to
// "who really pays for this"). CostFlow's job is different — a topological
// picture of how spend sits across the graph — so it stays with the literal
// edge direction and documents the difference rather than quietly
// duplicating econ's more expensive algorithm.
//
// Conservation: every resource's own MonthlyCost is injected exactly once,
// at its own node. An edge only ever redistributes a fraction (its
// EffectiveWeight, normalized against the node's other outbound edges so the
// total redistributed can never exceed 100%) of what that node has already
// accumulated; whatever is not redistributed stays displayed at the node.
// Summing every node's displayed Amount is therefore always exactly equal to
// the sum of the underlying resources' own costs — see costflow_test.go for
// the invariant this buys.
func (s *Service) CostFlow(ctx context.Context, tenant core.TenantID, q ports.TwinQuery) (ports.CostFlowGraph, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.CostFlowGraph{}, err
	}
	bc, err := s.load(ctx, tenant, q)
	if err != nil {
		return ports.CostFlowGraph{}, err
	}
	return buildCostFlow(bc), nil
}

func buildCostFlow(bc buildContext) ports.CostFlowGraph {
	resources := bc.inventory.All()
	ownCost := make(map[core.ID]core.Money, len(resources))
	labels := make(map[core.ID]string, len(resources))
	kinds := make(map[core.ID]cloud.Kind, len(resources))
	scopeTotal := core.ZeroUSD()
	for _, r := range resources {
		ownCost[r.ID] = r.MonthlyCost
		labels[r.ID] = r.DisplayName()
		kinds[r.ID] = r.Kind
		scopeTotal = scopeTotal.MustAdd(r.MonthlyCost)
	}

	// Build the carries-cost adjacency, grouped per source node so weights
	// can be normalized per node.
	outbound := map[core.ID][]costFlowEdge{}
	inDegree := map[core.ID]int{}
	nodesInFlow := map[core.ID]bool{}
	for _, e := range bc.topology.Edges() {
		if !e.Kind.CarriesCost() {
			continue
		}
		if _, ok := ownCost[e.FromID]; !ok {
			continue
		}
		if _, ok := ownCost[e.ToID]; !ok {
			continue
		}
		outbound[e.FromID] = append(outbound[e.FromID], costFlowEdge{to: e.ToID, weight: e.EffectiveWeight(), kind: e.Kind})
		inDegree[e.ToID]++
		nodesInFlow[e.FromID] = true
		nodesInFlow[e.ToID] = true
	}
	// Normalize each node's outbound weights so they never sum above 1: a
	// node with a single weight-1 edge keeps it (100% flows on); a node
	// contains-ing five children with default weight 1 each is normalized to
	// 1/5 apiece, an even split absent a measured consumption signal.
	fraction := map[core.ID]map[int]float64{} // node -> edge index -> fraction
	for from, edges := range outbound {
		sum := 0.0
		for _, e := range edges {
			sum += e.weight
		}
		fraction[from] = map[int]float64{}
		if sum <= 1 {
			for i, e := range edges {
				fraction[from][i] = e.weight
			}
		} else {
			for i, e := range edges {
				fraction[from][i] = e.weight / sum
			}
		}
	}

	// Topological order via Kahn's algorithm restricted to nodes that
	// participate in a carries-cost edge; a cycle (malformed relationship
	// data) leaves some nodes unprocessed, and those are treated as isolated
	// roots — their own cost is retained, not distributed, rather than
	// guessed at.
	order, depth := topoOrder(nodesInFlow, outbound, inDegree)

	accumulated := make(map[core.ID]float64, len(resources))
	for id, c := range ownCost {
		accumulated[id] = c.Units()
	}
	linkAmount := make(map[core.ID]float64) // sum of outbound link amounts already sent, per node
	var links []ports.CostFlowLink
	for _, id := range order {
		total := accumulated[id]
		for i, e := range outbound[id] {
			amt := total * fraction[id][i]
			if amt <= 0 {
				continue
			}
			accumulated[e.to] += amt
			linkAmount[id] += amt
			links = append(links, ports.CostFlowLink{From: id, To: e.to, Amount: core.USDollars(amt), Basis: string(e.kind)})
		}
	}

	// Every node's displayed amount is what it ended up holding minus what
	// it sent onward — the retained remainder, per the conservation
	// invariant described above.
	levelOf := map[int][]ports.CostFlowNode{}
	maxDepth := 0
	for _, r := range resources {
		id := r.ID
		amt := accumulated[id] - linkAmount[id]
		if amt < 0 {
			amt = 0 // floating point slack only; weights are capped at 1
		}
		d := depth[id] // zero for nodes never touched by a carries-cost edge
		if d > maxDepth {
			maxDepth = d
		}
		share := 0.0
		if !scopeTotal.IsZero() {
			share = amt / scopeTotal.Units()
		}
		levelOf[d] = append(levelOf[d], ports.CostFlowNode{
			ID: id, Label: labels[id], Kind: string(kinds[id]), Amount: core.USDollars(amt), Share: share,
		})
	}

	levels := make([]ports.CostFlowLevel, 0, maxDepth+1)
	for d := 0; d <= maxDepth; d++ {
		nodes := levelOf[d]
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].Amount.Micros() > nodes[j].Amount.Micros() })
		levels = append(levels, ports.CostFlowLevel{Depth: d, Nodes: nodes})
	}
	sort.Slice(links, func(i, j int) bool { return links[i].Amount.Micros() > links[j].Amount.Micros() })

	unattributed := bc.billedTotal.MustSub(scopeTotal)
	if unattributed.IsNegative() {
		// The resource-level estimate exceeds the period's billed total —
		// possible when MonthlyCost is a run-rate projection rather than the
		// exact period's bill. Unattributed cannot be negative by
		// definition, so it is floored at zero and the graph's own total
		// (scopeTotal) is what the conservation invariant is checked
		// against, not the billed figure.
		unattributed = core.ZeroUSD()
	}

	return ports.CostFlowGraph{
		Levels: levels, Links: links, Total: scopeTotal, Unattributed: unattributed, Period: bc.period,
	}
}

// topoOrder runs Kahn's algorithm over the carries-cost subgraph, returning
// a valid processing order and each node's longest-path depth from a source.
// Nodes involved in a cycle are appended at the end in arbitrary order with
// depth 0, so they are still displayed (their own cost retained) without the
// algorithm claiming a topological position for them that does not exist.
type costFlowEdge struct {
	to     core.ID
	weight float64
	kind   cloud.RelationKind
}

func topoOrder(nodes map[core.ID]bool, outbound map[core.ID][]costFlowEdge, inDegree map[core.ID]int) ([]core.ID, map[core.ID]int) {
	depth := make(map[core.ID]int, len(nodes))
	remaining := make(map[core.ID]int, len(nodes))
	for id := range nodes {
		remaining[id] = inDegree[id]
	}
	var queue []core.ID
	for id := range nodes {
		if remaining[id] == 0 {
			queue = append(queue, id)
		}
	}
	sort.Slice(queue, func(i, j int) bool { return queue[i] < queue[j] }) // deterministic order
	var order []core.ID
	visited := map[core.ID]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		order = append(order, id)
		for _, e := range outbound[id] {
			if depth[e.to] < depth[id]+1 {
				depth[e.to] = depth[id] + 1
			}
			remaining[e.to]--
			if remaining[e.to] == 0 {
				queue = append(queue, e.to)
			}
		}
	}
	if len(order) < len(nodes) {
		var leftover []core.ID
		for id := range nodes {
			if !visited[id] {
				leftover = append(leftover, id)
			}
		}
		sort.Slice(leftover, func(i, j int) bool { return leftover[i] < leftover[j] })
		order = append(order, leftover...)
	}
	return order, depth
}
