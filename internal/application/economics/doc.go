// Package economics implements ports.EconomicsService: Architecture
// Economics, CloudOptix's differentiator. A billing tool can say "RDS cost
// $24,500 last month"; this package is what turns that into "the checkout
// capability cost $61,200, of which $24,500 was its database, $8,700 was NAT
// egress it caused, and $4,100 was its measured share of the shared
// observability platform — $0.0061 per checkout, up 14% because basket size
// grew, not because anything got more expensive per unit."
//
// The attribution algorithm's key move is reusing cloud.Topology.Consumers
// as the single source of truth for splitting cost that touches more than
// one owner. Consumers already normalizes a shared component's inbound
// shared_by/runs_on/egress_via/attached_to edges into per-consumer shares
// summing to one; this package does not reimplement that arithmetic, it
// walks every resource with recorded consumers and, for each one that is
// this scope's own, books that resource's MonthlyCost times its measured
// share as ClassIndirect (a single consumer — the cost was exclusively
// caused by it) or ClassShared (multiple consumers — a genuine platform
// component). A resource this scope structurally depends on
// (depends_on/routes_to) but for which no consumer edge was ever recorded is
// not guessed at: its cost is added to the scope's Unattributed remainder,
// fully visible, rather than silently divided evenly across however many
// scopes happen to touch it. An even split would manufacture false
// precision — it would tell a team "your database costs you $340/month"
// when the true number could be $50 or $900 depending on a traffic pattern
// nobody measured — and a wrong number that looks confident is worse for a
// FinOps decision than an honest "we don't know yet, here's what's unclear."
//
// Traceability: REQ-ECON-001..012, SPEC-ECON-001..005.
package economics
