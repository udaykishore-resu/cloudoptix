package cloud

import (
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// RelationKind names the edge types in the architecture graph. The set is
// deliberately small: every richer notion the UI shows is a projection of
// these, which keeps graph algorithms (blast radius, cost flow, dependency
// closure) uniform.
type RelationKind string

const (
	// RelContains is structural ownership: a VPC contains a subnet, a cluster
	// contains a node group. Cost flows down a contains edge.
	RelContains RelationKind = "contains"
	// RelRunsOn is placement: a Kubernetes workload runs on a node group, a
	// task runs on a cluster.
	RelRunsOn RelationKind = "runs_on"
	// RelRoutesTo is request-path traffic: CloudFront routes to API Gateway
	// routes to an ALB routes to a target group.
	RelRoutesTo RelationKind = "routes_to"
	// RelDependsOn is a runtime dependency discovered from configuration,
	// security groups, or declared in the spec: a service depends on a
	// database.
	RelDependsOn RelationKind = "depends_on"
	// RelAttachedTo is device attachment: a volume to an instance, an EIP to
	// a NAT gateway.
	RelAttachedTo RelationKind = "attached_to"
	// RelReplicaOf links a read replica to its primary.
	RelReplicaOf RelationKind = "replica_of"
	// RelProducesTo and RelConsumesFrom model asynchronous messaging, where
	// the request path is not a call graph.
	RelProducesTo   RelationKind = "produces_to"
	RelConsumesFrom RelationKind = "consumes_from"
	// RelEgressVia records that a resource's internet-bound traffic leaves
	// through a specific NAT gateway or endpoint. This edge is what makes NAT
	// cost attributable to the workload that caused it rather than to the
	// network team.
	RelEgressVia RelationKind = "egress_via"
	// RelSharedBy marks a shared platform component consumed by many
	// workloads; the economics engine splits its cost across these edges.
	RelSharedBy RelationKind = "shared_by"
)

// TraversesRequestPath reports whether the edge lies on a synchronous request
// path. Blast-radius calculation walks these transitively; it does not treat
// a "contains" edge as propagating availability risk.
func (r RelationKind) TraversesRequestPath() bool {
	switch r {
	case RelRoutesTo, RelDependsOn, RelRunsOn, RelAttachedTo, RelReplicaOf:
		return true
	}
	return false
}

// CarriesCost reports whether cost flows along the edge in the cost-flow
// visualisation.
func (r RelationKind) CarriesCost() bool {
	switch r {
	case RelContains, RelRunsOn, RelAttachedTo, RelEgressVia, RelSharedBy:
		return true
	}
	return false
}

// Relationship is a directed, typed edge between two resources.
type Relationship struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	FromID   core.ID       `json:"from_id"`
	ToID     core.ID       `json:"to_id"`
	Kind     RelationKind  `json:"kind"`
	// Weight carries edge-specific magnitude: the share of traffic on a
	// routes_to edge, the fraction of a shared component consumed on a
	// shared_by edge. Defaults to 1.
	Weight float64 `json:"weight"`
	// Confidence records how sure discovery is about the edge. A dependency
	// inferred from security-group rules is less certain than one declared in
	// the onboarding spec, and blast radius discounts uncertain edges.
	Confidence  core.Confidence   `json:"confidence"`
	Source      core.Provenance   `json:"source"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	FirstSeenAt time.Time         `json:"first_seen_at"`
	LastSeenAt  time.Time         `json:"last_seen_at"`
}

// EffectiveWeight returns the weight, defaulting to 1 when unset.
func (r Relationship) EffectiveWeight() float64 {
	if r.Weight <= 0 {
		return 1
	}
	return r.Weight
}

// Topology is an indexed relationship set supporting the traversals the
// platform performs constantly: forward and reverse adjacency, transitive
// dependency closure, and shared-component fan-out.
type Topology struct {
	edges    []Relationship
	outbound map[core.ID][]int
	inbound  map[core.ID][]int
}

// NewTopology indexes an edge slice.
func NewTopology(edges []Relationship) *Topology {
	t := &Topology{
		edges:    edges,
		outbound: map[core.ID][]int{},
		inbound:  map[core.ID][]int{},
	}
	for i, e := range edges {
		t.outbound[e.FromID] = append(t.outbound[e.FromID], i)
		t.inbound[e.ToID] = append(t.inbound[e.ToID], i)
	}
	return t
}

// Edges returns every relationship.
func (t *Topology) Edges() []Relationship { return t.edges }

// Len returns the edge count.
func (t *Topology) Len() int { return len(t.edges) }

// Outbound returns edges leaving a node, optionally filtered by kind.
func (t *Topology) Outbound(id core.ID, kinds ...RelationKind) []Relationship {
	return t.collect(t.outbound[id], kinds)
}

// Inbound returns edges entering a node, optionally filtered by kind.
func (t *Topology) Inbound(id core.ID, kinds ...RelationKind) []Relationship {
	return t.collect(t.inbound[id], kinds)
}

func (t *Topology) collect(idxs []int, kinds []RelationKind) []Relationship {
	out := make([]Relationship, 0, len(idxs))
	for _, i := range idxs {
		e := t.edges[i]
		if len(kinds) == 0 {
			out = append(out, e)
			continue
		}
		for _, k := range kinds {
			if e.Kind == k {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// Dependents returns every node that transitively depends on the given node —
// that is, everything upstream on a request path. This is the core of blast
// radius: changing X can affect everything that reaches X.
//
// The walk discounts by edge confidence and stops at maxDepth to bound cost on
// pathological graphs. Returned confidence is the product along the strongest
// path to each dependent.
func (t *Topology) Dependents(id core.ID, maxDepth int) map[core.ID]core.Confidence {
	found := map[core.ID]core.Confidence{}
	type frame struct {
		id    core.ID
		depth int
		conf  float64
	}
	queue := []frame{{id: id, depth: 0, conf: 1}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		for _, e := range t.Inbound(cur.id) {
			if !e.Kind.TraversesRequestPath() {
				continue
			}
			ec := float64(e.Confidence)
			if ec <= 0 {
				ec = 0.8 // discovered-but-unrated edges are treated as likely
			}
			conf := cur.conf * ec
			if conf < 0.05 {
				continue // the path has decayed below usefulness
			}
			if prev, ok := found[e.FromID]; ok && float64(prev) >= conf {
				continue
			}
			found[e.FromID] = core.Confidence(conf)
			queue = append(queue, frame{id: e.FromID, depth: cur.depth + 1, conf: conf})
		}
	}
	delete(found, id)
	return found
}

// Dependencies returns everything the node transitively relies on.
func (t *Topology) Dependencies(id core.ID, maxDepth int) map[core.ID]core.Confidence {
	found := map[core.ID]core.Confidence{}
	type frame struct {
		id    core.ID
		depth int
		conf  float64
	}
	queue := []frame{{id: id, depth: 0, conf: 1}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		for _, e := range t.Outbound(cur.id) {
			if !e.Kind.TraversesRequestPath() {
				continue
			}
			ec := float64(e.Confidence)
			if ec <= 0 {
				ec = 0.8
			}
			conf := cur.conf * ec
			if conf < 0.05 {
				continue
			}
			if prev, ok := found[e.ToID]; ok && float64(prev) >= conf {
				continue
			}
			found[e.ToID] = core.Confidence(conf)
			queue = append(queue, frame{id: e.ToID, depth: cur.depth + 1, conf: conf})
		}
	}
	delete(found, id)
	return found
}

// Consumers returns the nodes sharing a platform component, with their share
// weights normalized to sum to one. The economics engine uses this to split
// shared cost; when no consumer is recorded the component's cost stays
// unallocated rather than being silently spread evenly, because a wrong
// allocation is worse than a visible gap.
func (t *Topology) Consumers(id core.ID) map[core.ID]float64 {
	edges := t.Inbound(id, RelSharedBy, RelRunsOn, RelEgressVia, RelAttachedTo)
	if len(edges) == 0 {
		return nil
	}
	total := 0.0
	for _, e := range edges {
		total += e.EffectiveWeight()
	}
	if total == 0 {
		return nil
	}
	out := make(map[core.ID]float64, len(edges))
	for _, e := range edges {
		out[e.FromID] += e.EffectiveWeight() / total
	}
	return out
}
