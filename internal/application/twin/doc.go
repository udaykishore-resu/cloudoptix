// Package twin implements ports.TwinService: the Architecture Digital Twin
// that renders the estate — resources, relationships, attributed cost,
// metrics and findings — as a graph a human can actually look at.
//
// The key design decision is that a "resource graph" and a "twin view" are
// not the same object. The graph is one thing: resources and relationships,
// which never changes shape. A view is a projection of that graph that picks
// which numeric fields drive node size and colour and which edges matter —
// the cost view sizes nodes by MonthlyCost and colours them by spend tier,
// the reliability view sizes them by blast radius and colours them by risk.
// Building six view-specific graph types would let each one drift out of
// sync with the others; instead every view produces the same TwinNode shape
// with different fields populated, and TwinGraph.Legend documents which
// fields the caller should read for the active view. That is also what makes
// collapsing safe: a collapsed synthetic node still satisfies the same
// struct, so nothing downstream needs a special case for it.
//
// The second decision worth stating: CostFlow's accumulation is built so that
// conservation is provable by construction, not merely observed to hold in
// testing. Every resource's own cost is injected exactly once, at its own
// node; an edge only ever redistributes a fraction of what a node has
// already received (never more than 100%, and the un-redistributed
// remainder stays attributed to that node); so summing every node's
// displayed amount, across every level, is always exactly equal to the sum
// of the resources' own costs, by induction over the graph's topological
// order — see costflow.go for exactly what "flows in the FromID→ToID
// direction" means for each carries-cost edge kind, and why the true billed
// total can still exceed that sum (the honest Unattributed remainder).
//
// Traceability: REQ-TWIN-001..009, SPEC-TWIN-001..003.
package twin
