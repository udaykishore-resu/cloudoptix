package twin

import (
	"sort"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// buildGraph turns a loaded buildContext into the requested view. It is the
// single place every projection, filter, search, collapse and truncation
// decision is made, so the six views can never disagree about how a node was
// built.
func buildGraph(bc buildContext, q ports.TwinQuery, maxBeforeCollapse, hardCap, defaultMaxDepth int) ports.TwinGraph {
	view := q.View
	if view == "" {
		view = "architecture"
	}

	// Total attributed cost across the loaded scope, used for CostShare —
	// computed from the same resources the graph shows, not from a separate
	// billing query, so a node's displayed share always sums to 1 across the
	// graph rather than to whatever fraction of total tenant spend this
	// filtered view happens to represent.
	scopeTotal := core.ZeroUSD()
	for _, r := range bc.inventory.All() {
		scopeTotal = scopeTotal.MustAdd(r.MonthlyCost)
	}

	nodes := make(map[core.ID]ports.TwinNode, bc.inventory.Len())
	for _, res := range bc.inventory.All() {
		m := bc.metrics[res.ID]
		node := nodeFrom(res, m, bc.findings[res.ID], res.MonthlyCost)
		if !scopeTotal.IsZero() {
			node.CostShare = node.MonthlyCost.Ratio(scopeTotal)
		}
		applyView(&node, view, res, m)
		nodes[res.ID] = node
	}

	edges := make([]ports.TwinEdge, 0, bc.topology.Len())
	for _, e := range bc.topology.Edges() {
		if _, ok := nodes[e.FromID]; !ok {
			continue
		}
		if _, ok := nodes[e.ToID]; !ok {
			continue
		}
		edges = append(edges, ports.TwinEdge{
			From: e.FromID, To: e.ToID, Kind: e.Kind, Weight: e.EffectiveWeight(), Confidence: e.Confidence,
		})
	}

	// Rooted subgraph: an unrestricted breadth-first walk over every edge
	// kind, in both directions, bounded by depth — deliberately broader than
	// Topology.Dependents (which only follows request-path edges one way),
	// because "show me what's around this resource" is an architecture
	// question, not a blast-radius one.
	if !q.RootID.IsZero() {
		depth := q.MaxDepth
		if depth <= 0 {
			depth = defaultMaxDepth
		}
		keep := bfsSubgraph(bc.topology, q.RootID, depth)
		keep[q.RootID] = true
		nodes, edges = restrictTo(nodes, edges, keep)
	}

	if q.Search != "" {
		needle := strings.ToLower(q.Search)
		keep := make(map[core.ID]bool, len(nodes))
		for id, n := range nodes {
			if matchesSearch(n, needle) {
				keep[id] = true
			}
		}
		nodes, edges = restrictTo(nodes, edges, keep)
	}

	truncated := false
	nodeList := mapValues(nodes)
	if q.Collapse || len(nodeList) > maxBeforeCollapse {
		var collapsedAway int
		nodeList, edges, collapsedAway = collapseLowValueLeaves(nodeList, edges, q.RootID)
		truncated = truncated || collapsedAway > 0
	}
	if len(nodeList) > hardCap {
		nodeList, edges = truncateToTop(nodeList, edges, hardCap, q.RootID)
		truncated = true
	}

	sort.Slice(nodeList, func(i, j int) bool { return nodeList[i].MonthlyCost.Micros() > nodeList[j].MonthlyCost.Micros() })

	graph := ports.TwinGraph{
		Nodes: nodeList, Edges: edges, View: view, Truncated: truncated,
		Legend: legendFor(view),
		Stats:  statsFor(nodeList, edges, bc),
	}
	return graph
}

// nodeFrom builds the base TwinNode common to every view.
func nodeFrom(res cloud.Resource, m ports.ResourceMetrics, recs []optimize.Recommendation, monthlyCost core.Money) ports.TwinNode {
	// FindingCount counts every recommendation on the node, but the saving
	// sums only the primaries: a node carrying three mutually exclusive ways
	// to shrink one node group has three findings and one bankable number
	// (see optimize/conflict.go). Showing the sum of all three would make
	// the twin the one screen still quoting the double-counted figure.
	saving := optimize.TotalPotentialSaving(recs)
	node := ports.TwinNode{
		ID: res.ID, Label: res.DisplayName(), Kind: res.Kind, Category: res.Kind.Category(),
		Service: res.Kind.Service(), AccountID: res.AccountID, Region: res.Region, AZ: res.AZ,
		Environment: res.Environment, State: res.State, MonthlyCost: monthlyCost,
		EconomicFootprint: monthlyCost, // direct-cost proxy; see package doc
		Criticality:       res.Criticality, Owner: res.Owner, Tags: res.Tags,
		FindingCount: len(recs), PotentialSaving: saving,
	}
	if m.CPU != nil {
		cpu := *m.CPU
		node.CPU = &cpu
	}
	if m.Memory != nil {
		mem := *m.Memory
		node.Memory = &mem
	}
	if m.LatencyP99 != nil {
		node.LatencyP99 = m.LatencyP99.P99
	}
	if m.ErrorRate != nil {
		node.ErrorRate = m.ErrorRate.Mean
		node.Availability = clamp01(1 - m.ErrorRate.Mean)
	}
	node.Risk = riskFor(res, recs, m)
	return node
}

// riskFor is the one risk estimate every view shares: blast radius (findings
// present, criticality, production status). A dedicated security-findings
// engine would refine this further; until one exists, this is the honest
// generic signal every field it reads is already collected for.
func riskFor(res cloud.Resource, recs []optimize.Recommendation, m ports.ResourceMetrics) core.RiskLevel {
	score := 0.0
	if res.IsProduction() {
		score += 0.25
	}
	score += res.Criticality.Weight() * 0.25
	for _, r := range recs {
		if r.Risk.Score > score {
			score = (score + r.Risk.Score) / 2
		}
	}
	if m.ErrorRate != nil && m.ErrorRate.Mean > 0.01 {
		score += 0.15
	}
	if m.CPU != nil && m.CPU.P95 >= 90 {
		score += 0.1
	}
	return core.RiskLevelFromScore(clamp01(score))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// applyView adjusts fields whose meaning is view-specific. The base fields
// set by nodeFrom already cover architecture, cost, performance and
// reliability; this only fills the gaps that need the raw resource/metrics
// rather than what nodeFrom already computed.
func applyView(node *ports.TwinNode, view string, res cloud.Resource, m ports.ResourceMetrics) {
	switch view {
	case "security":
		// Elevate risk for kinds that are inherently sensitive even absent a
		// dedicated finding, so the security view is not simply a repaint of
		// the reliability view.
		if res.Kind == cloud.KindKMSKey || res.Kind == cloud.KindSecret {
			if node.Risk.Order() < core.RiskMedium.Order() {
				node.Risk = core.RiskMedium
			}
		}
		if res.Kind == cloud.KindSecurityGroup && res.Attr("open_ingress", "") == "true" {
			node.Risk = core.RiskCritical
		}
	case "economics":
		// EconomicFootprint already carries the direct-cost proxy set in
		// nodeFrom; nothing further to adjust without a wired-in economics
		// footprint lookup per resource.
	}
}

func legendFor(view string) map[string]string {
	base := map[string]string{"kind_shown_as": "icon", "state": "border_style"}
	switch view {
	case "cost":
		base["size"] = "monthly_cost"
		base["color"] = "cost_share (green=low, red=high)"
	case "performance":
		base["size"] = "monthly_cost"
		base["color"] = "cpu.p95 (blue=idle, orange=saturated)"
	case "reliability":
		base["size"] = "finding_count"
		base["color"] = "risk (green=none, red=critical)"
	case "security":
		base["size"] = "risk"
		base["color"] = "risk (green=none, red=critical)"
	case "economics":
		base["size"] = "economic_footprint"
		base["color"] = "cost_share"
	default: // architecture
		base["size"] = "uniform (group_count for collapsed nodes)"
		base["color"] = "category"
	}
	return base
}

func matchesSearch(n ports.TwinNode, needle string) bool {
	if strings.Contains(strings.ToLower(n.Label), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(string(n.Kind)), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(n.Owner), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(n.Application), needle) {
		return true
	}
	for k, v := range n.Tags {
		if strings.Contains(strings.ToLower(k), needle) || strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}

func mapValues(m map[core.ID]ports.TwinNode) []ports.TwinNode {
	out := make([]ports.TwinNode, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func restrictTo(nodes map[core.ID]ports.TwinNode, edges []ports.TwinEdge, keep map[core.ID]bool) (map[core.ID]ports.TwinNode, []ports.TwinEdge) {
	outNodes := make(map[core.ID]ports.TwinNode, len(keep))
	for id := range keep {
		if n, ok := nodes[id]; ok {
			outNodes[id] = n
		}
	}
	outEdges := make([]ports.TwinEdge, 0, len(edges))
	for _, e := range edges {
		if keep[e.From] && keep[e.To] {
			outEdges = append(outEdges, e)
		}
	}
	return outNodes, outEdges
}

// bfsSubgraph walks every edge kind in both directions from root, bounded by
// depth, and returns every node reached (not including root itself).
func bfsSubgraph(topo *cloud.Topology, root core.ID, maxDepth int) map[core.ID]bool {
	visited := map[core.ID]bool{root: true}
	type frame struct {
		id    core.ID
		depth int
	}
	queue := []frame{{root, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		neighbours := make([]core.ID, 0)
		for _, e := range topo.Outbound(cur.id) {
			neighbours = append(neighbours, e.ToID)
		}
		for _, e := range topo.Inbound(cur.id) {
			neighbours = append(neighbours, e.FromID)
		}
		for _, id := range neighbours {
			if visited[id] {
				continue
			}
			visited[id] = true
			queue = append(queue, frame{id, cur.depth + 1})
		}
	}
	delete(visited, root)
	return visited
}

func statsFor(nodes []ports.TwinNode, edges []ports.TwinEdge, bc buildContext) ports.TwinStats {
	stats := ports.TwinStats{NodeCount: len(nodes), EdgeCount: len(edges)}
	envs := map[core.Environment]bool{}
	accounts := map[core.AccountID]bool{}
	regions := map[core.Region]bool{}
	apps := map[string]bool{}
	total := core.ZeroUSD()
	connected := map[core.ID]bool{}
	for _, e := range edges {
		connected[e.From] = true
		connected[e.To] = true
	}
	orphans := 0
	for _, n := range nodes {
		total = total.MustAdd(n.MonthlyCost)
		envs[n.Environment] = true
		accounts[n.AccountID] = true
		regions[n.Region] = true
		if n.Application != "" {
			apps[n.Application] = true
		}
		if !connected[n.ID] && !n.Group {
			orphans++
		}
	}
	stats.TotalCost = total
	stats.Environments = len(envs)
	stats.Accounts = len(accounts)
	stats.Regions = len(regions)
	stats.Applications = len(apps)
	stats.OrphanCount = orphans
	if bc.inventory != nil && bc.inventory.Len() > 0 {
		stats.Completeness = float64(len(nodes)) / float64(bc.inventory.Len())
	} else {
		stats.Completeness = 1
	}
	return stats
}

func computeStats(bc buildContext) ports.TwinStats {
	nodes := make([]ports.TwinNode, 0, bc.inventory.Len())
	for _, res := range bc.inventory.All() {
		nodes = append(nodes, nodeFrom(res, bc.metrics[res.ID], bc.findings[res.ID], res.MonthlyCost))
	}
	edges := make([]ports.TwinEdge, 0, bc.topology.Len())
	for _, e := range bc.topology.Edges() {
		edges = append(edges, ports.TwinEdge{From: e.FromID, To: e.ToID, Kind: e.Kind, Weight: e.EffectiveWeight(), Confidence: e.Confidence})
	}
	return statsFor(nodes, edges, bc)
}
