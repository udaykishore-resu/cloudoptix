package twin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Service implements ports.TwinService.
type Service struct {
	Repos ports.Repositories
	Cache ports.Cache
	Clock core.Clock

	// CacheTTL bounds how long a built graph is served from cache before a
	// fresh Graph call recomputes it. Defaults to 5 minutes.
	CacheTTL time.Duration
	// CostLookbackDays is the window ByResource cost attribution is pulled
	// over. Defaults to 30.
	CostLookbackDays int
	// MaxNodesBeforeCollapse is the node count above which collapsing runs
	// even when the caller did not ask for it, because an ungrouped
	// 40,000-node graph is not something any renderer can usefully draw.
	// Defaults to 500.
	MaxNodesBeforeCollapse int
	// HardNodeCap is the absolute ceiling on nodes returned after
	// collapsing; beyond it the lowest-value remaining nodes are dropped and
	// Truncated is set. Defaults to 2000.
	HardNodeCap int
	// DefaultMaxDepth bounds a rooted subgraph or Dependents walk when the
	// caller does not specify one. Defaults to 4.
	DefaultMaxDepth int
}

var _ ports.TwinService = (*Service)(nil)

// NewService builds a Service with the platform defaults.
func NewService(repos ports.Repositories, cache ports.Cache) *Service {
	return &Service{
		Repos: repos, Cache: cache, Clock: core.SystemClock{},
		CacheTTL: 5 * time.Minute, CostLookbackDays: 30,
		MaxNodesBeforeCollapse: 500, HardNodeCap: 2000, DefaultMaxDepth: 4,
	}
}

func (s *Service) clock() core.Clock {
	if s.Clock == nil {
		return core.SystemClock{}
	}
	return s.Clock
}

// buildContext is the data every view projection is built from, assembled
// once per Graph call so that six view-specific queries never turn into six
// separate database round trips.
type buildContext struct {
	tenant      core.TenantID
	inventory   *cloud.Inventory
	topology    *cloud.Topology
	metrics     map[core.ID]ports.ResourceMetrics
	findings    map[core.ID][]optimize.Recommendation
	period      core.Period
	billedTotal core.Money
}

func (s *Service) load(ctx context.Context, tenant core.TenantID, q ports.TwinQuery) (buildContext, error) {
	filter := resourceFilterFromQuery(q)
	inv, err := s.Repos.Resources.LoadInventory(ctx, tenant, filter)
	if err != nil {
		return buildContext{}, err
	}
	topo, err := s.Repos.Resources.LoadTopology(ctx, tenant, filter)
	if err != nil {
		return buildContext{}, err
	}
	ids := make([]core.ID, 0, inv.Len())
	for _, r := range inv.All() {
		ids = append(ids, r.ID)
	}
	metrics, err := s.Repos.Metrics.LoadSummaries(ctx, tenant, ids)
	if err != nil {
		metrics = map[core.ID]ports.ResourceMetrics{}
	}

	findings := map[core.ID][]optimize.Recommendation{}
	if page, ferr := s.Repos.Recommendations.List(ctx, tenant, ports.RecommendationFilter{
		Statuses: []optimize.Status{optimize.StatusOpen, optimize.StatusUnderReview, optimize.StatusApproved, optimize.StatusScheduled},
	}, ports.ListOptions{Limit: 500}); ferr == nil {
		for _, rec := range page.Items {
			rid := rec.Finding.ResourceID
			if rid.IsZero() {
				continue
			}
			findings[rid] = append(findings[rid], rec)
		}
	}

	lookback := s.CostLookbackDays
	if lookback <= 0 {
		lookback = 30
	}
	period := core.PeriodOfDays(s.clock().Now(), lookback)
	billedTotal, err := s.Repos.Costs.Total(ctx, tenant, ports.CostFilter{Period: period})
	if err != nil {
		billedTotal = core.ZeroUSD()
	}

	return buildContext{
		tenant: tenant, inventory: inv, topology: topo, metrics: metrics,
		findings: findings, period: period, billedTotal: billedTotal,
	}, nil
}

func resourceFilterFromQuery(q ports.TwinQuery) ports.ResourceFilter {
	return ports.ResourceFilter{
		AccountIDs: q.AccountIDs, Regions: q.Regions, Kinds: q.Kinds,
		Environments: q.Environments, ApplicationID: q.ApplicationID, WorkloadID: q.WorkloadID,
		MinMonthlyCost: q.MinMonthlyCost, Search: q.Search,
	}
}

// Graph builds and returns the requested view projection, cached for
// CacheTTL keyed by the exact query.
func (s *Service) Graph(ctx context.Context, tenant core.TenantID, q ports.TwinQuery) (ports.TwinGraph, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.TwinGraph{}, err
	}
	key := cacheKey("graph", q)
	if s.Cache != nil {
		var cached ports.TwinGraph
		if hit, _ := s.Cache.Get(ctx, tenant, key, &cached); hit {
			return cached, nil
		}
	}

	bc, err := s.load(ctx, tenant, q)
	if err != nil {
		return ports.TwinGraph{}, err
	}
	graph := buildGraph(bc, q, s.MaxNodesBeforeCollapse, s.HardNodeCap, s.DefaultMaxDepth)
	graph.Stats.BuiltAt = s.clock().Now()

	if s.Cache != nil {
		_ = s.Cache.Set(ctx, tenant, key, graph, s.CacheTTL)
	}
	return graph, nil
}

// Node returns one resource's full detail panel: every optional field is
// populated regardless of view, since the detail panel is not projected —
// it is the one place a user wants everything at once.
func (s *Service) Node(ctx context.Context, tenant core.TenantID, resourceID core.ID) (ports.TwinNode, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.TwinNode{}, err
	}
	res, err := s.Repos.Resources.Get(ctx, tenant, resourceID)
	if err != nil {
		return ports.TwinNode{}, err
	}
	metrics, _ := s.Repos.Metrics.GetSummary(ctx, tenant, resourceID)
	var recs []optimize.Recommendation
	if page, ferr := s.Repos.Recommendations.List(ctx, tenant, ports.RecommendationFilter{ResourceID: resourceID}, ports.ListOptions{Limit: 100}); ferr == nil {
		recs = page.Items
	}
	node := nodeFrom(res, metrics, recs, res.MonthlyCost)
	applyView(&node, "architecture", res, metrics)
	applyView(&node, "cost", res, metrics)
	applyView(&node, "performance", res, metrics)
	applyView(&node, "reliability", res, metrics)
	return node, nil
}

// Rebuild invalidates every cached derived graph for the tenant and returns
// fresh top-level statistics, so a caller (typically the discovery or cost
// pipeline, on completion) can confirm the twin is current without paying
// for a full graph render.
func (s *Service) Rebuild(ctx context.Context, tenant core.TenantID) (ports.TwinStats, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.TwinStats{}, err
	}
	if s.Cache != nil {
		_ = s.Cache.InvalidatePrefix(ctx, tenant, "twin:")
	}
	bc, err := s.load(ctx, tenant, ports.TwinQuery{View: "architecture"})
	if err != nil {
		return ports.TwinStats{}, err
	}
	stats := computeStats(bc)
	stats.BuiltAt = s.clock().Now()
	return stats, nil
}

// Dependents returns the transitive dependents of a resource — everything
// upstream on a request path — as full twin nodes, which is what the UI's
// blast-radius panel renders directly.
func (s *Service) Dependents(ctx context.Context, tenant core.TenantID, resourceID core.ID, maxDepth int) ([]ports.TwinNode, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	if maxDepth <= 0 {
		maxDepth = s.DefaultMaxDepth
		if maxDepth <= 0 {
			maxDepth = 4
		}
	}
	topo, err := s.Repos.Resources.LoadTopology(ctx, tenant, ports.ResourceFilter{})
	if err != nil {
		return nil, err
	}
	deps := topo.Dependents(resourceID, maxDepth)
	if len(deps) == 0 {
		return nil, nil
	}
	out := make([]ports.TwinNode, 0, len(deps))
	for id, conf := range deps {
		res, err := s.Repos.Resources.Get(ctx, tenant, id)
		if err != nil {
			continue
		}
		metrics, _ := s.Repos.Metrics.GetSummary(ctx, tenant, id)
		node := nodeFrom(res, metrics, nil, res.MonthlyCost)
		node.Risk = core.RiskLevelFromScore(1 - float64(conf))
		out = append(out, node)
	}
	return out, nil
}

func cacheKey(kind string, q ports.TwinQuery) string {
	b, _ := json.Marshal(q)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("twin:%s:%s", kind, hex.EncodeToString(sum[:16]))
}
