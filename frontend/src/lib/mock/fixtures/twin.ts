import type { CostFlowGraph, CostFlowLevel, CostFlowLink, CostFlowNode, Resource, TwinEdge, TwinGraph, TwinNode, TwinStats, TwinView } from "@/types/domain";
import { getWorld, moneyOf } from "../world";
import { KIND_CATEGORY, KIND_SERVICE } from "../kinds";
import { resetRng } from "../rng";

function resourceToNode(r: Resource, totalCost: number): TwinNode {
  return {
    id: r.id,
    label: r.name || r.native_id,
    kind: r.kind,
    category: KIND_CATEGORY(r.kind),
    service: KIND_SERVICE(r.kind),
    account_id: r.account_id,
    region: r.region,
    availability_zone: r.availability_zone,
    environment: r.environment,
    state: r.state,
    monthly_cost: r.monthly_cost,
    economic_footprint: r.monthly_cost,
    cost_share: totalCost > 0 ? r.monthly_cost.amount / totalCost : 0,
    cpu: r.cpu,
    memory: r.memory,
    latency_p99_ms: KIND_CATEGORY(r.kind) === "compute" ? 40 + (1 - (r.cpu?.p50 ?? 50) / 100) * 20 + (r.finding_count ? 30 : 0) : undefined,
    error_rate: KIND_CATEGORY(r.kind) === "compute" ? Math.max(0, (r.finding_count ?? 0) * 0.4 + (100 - (r.cpu?.p50 ?? 80)) * 0.01) : undefined,
    availability: KIND_CATEGORY(r.kind) === "compute" || KIND_CATEGORY(r.kind) === "database" ? 99.95 - (r.finding_count ?? 0) * 0.08 : undefined,
    risk: r.finding_count && r.finding_count > 1 ? "MEDIUM" : r.finding_count ? "LOW" : "NONE",
    criticality: r.criticality,
    owner: r.owner,
    application: r.application,
    workload: r.workload,
    tags: r.tags,
    finding_count: r.finding_count ?? 0,
    potential_saving: r.potential_saving ?? moneyOf(0),
  };
}

let graphCache: { nodes: TwinNode[]; edges: TwinEdge[]; stats: TwinStats } | null = null;

function buildBase() {
  if (graphCache) return graphCache;
  const w = getWorld();
  const totalCost = w.resources.reduce((s, r) => s + r.monthly_cost.amount, 0);
  const nodes = w.resources.map((r) => resourceToNode(r, totalCost));
  const applications = new Set(w.resources.map((r) => r.application).filter(Boolean));
  const accounts = new Set(w.resources.map((r) => r.account_id));
  const regions = new Set(w.resources.map((r) => r.region));
  const environments = new Set(w.resources.map((r) => r.environment));
  const stats: TwinStats = {
    node_count: nodes.length,
    edge_count: w.edges.length,
    total_cost: moneyOf(totalCost),
    environments: environments.size,
    accounts: accounts.size,
    regions: regions.size,
    applications: applications.size,
    orphan_count: nodes.filter((n) => !w.edges.some((e) => e.from === n.id || e.to === n.id)).length,
    completeness: 0.94,
    built_at: "2026-08-31T05:30:00Z",
  };
  graphCache = { nodes, edges: w.edges, stats };
  return graphCache;
}

const LEGENDS: Record<TwinView, Record<string, string>> = {
  architecture: { color: "Resource category", size: "Monthly cost" },
  cost: { color: "Cost share (low → high)", size: "Monthly cost" },
  performance: { color: "P99 latency", size: "Error rate" },
  reliability: { color: "Availability", size: "Blast radius" },
  security: { color: "Risk level", size: "Finding count" },
  economics: { color: "Cost class (direct/indirect/shared)", size: "Economic footprint" },
};

export function buildTwinGraph(view: TwinView = "architecture", opts: { search?: string; environments?: string[]; accountIds?: string[]; applicationId?: string } = {}): TwinGraph {
  const { nodes, edges, stats } = buildBase();
  let filtered = nodes;
  if (opts.environments?.length) filtered = filtered.filter((n) => opts.environments!.includes(n.environment));
  if (opts.accountIds?.length) filtered = filtered.filter((n) => opts.accountIds!.includes(n.account_id));
  if (opts.applicationId) filtered = filtered.filter((n) => n.application === opts.applicationId);
  if (opts.search) {
    const q = opts.search.toLowerCase();
    filtered = filtered.filter((n) => n.label.toLowerCase().includes(q) || n.service.toLowerCase().includes(q) || (n.application ?? "").toLowerCase().includes(q));
  }
  const ids = new Set(filtered.map((n) => n.id));
  const filteredEdges = edges.filter((e) => ids.has(e.from) && ids.has(e.to));
  return {
    nodes: filtered,
    edges: filteredEdges,
    stats: { ...stats, node_count: filtered.length, edge_count: filteredEdges.length },
    view,
    legend: LEGENDS[view],
    truncated: false,
  };
}

export function getTwinNode(id: string): TwinNode | undefined {
  const { nodes } = buildBase();
  return nodes.find((n) => n.id === id);
}

export function getDependents(id: string, maxDepth = 3): TwinNode[] {
  const { nodes, edges } = buildBase();
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const visited = new Set<string>([id]);
  let frontier = [id];
  const result: TwinNode[] = [];
  for (let depth = 0; depth < maxDepth && frontier.length; depth++) {
    const next: string[] = [];
    for (const f of frontier) {
      for (const e of edges) {
        if (e.from === f && !visited.has(e.to)) {
          visited.add(e.to);
          next.push(e.to);
          const n = byId.get(e.to);
          if (n) result.push(n);
        }
      }
    }
    frontier = next;
  }
  return result;
}

export function getDependencies(id: string): TwinNode[] {
  const { nodes, edges } = buildBase();
  const byId = new Map(nodes.map((n) => [n.id, n]));
  return edges.filter((e) => e.to === id).map((e) => byId.get(e.from)).filter((n): n is TwinNode => !!n);
}

export function getEdgesFor(id: string): TwinEdge[] {
  const { edges } = buildBase();
  return edges.filter((e) => e.from === id || e.to === id);
}

/** Builds the Sankey-style cost-flow projection: money flows from account
 * level down through application → workload → resource, with a level for
 * spend the economics engine could not attribute to any workload. */
export function buildCostFlow(): CostFlowGraph {
  resetRng(55010);
  const w = getWorld();
  const totalCost = w.resources.reduce((s, r) => s + r.monthly_cost.amount, 0);

  const level0: CostFlowNode[] = [{ id: "root", label: "Total spend", kind: "root", amount: moneyOf(totalCost), share: 1 }];

  const byApp = new Map<string, number>();
  let unattributed = 0;
  for (const r of w.resources) {
    if (r.application) byApp.set(r.application, (byApp.get(r.application) ?? 0) + r.monthly_cost.amount);
    else unattributed += r.monthly_cost.amount;
  }
  const level1: CostFlowNode[] = [...byApp.entries()].map(([app, amt]) => ({ id: `app:${app}`, label: app, kind: "application", amount: moneyOf(amt), share: amt / totalCost }));
  level1.push({ id: "app:unattributed", label: "Unattributed (shared platform)", kind: "unattributed", amount: moneyOf(unattributed), share: unattributed / totalCost });

  const byWorkload = new Map<string, { app: string; amt: number }>();
  for (const r of w.resources) {
    if (!r.workload || !r.application) continue;
    const key = `${r.application}::${r.workload}`;
    const cur = byWorkload.get(key) ?? { app: r.application, amt: 0 };
    cur.amt += r.monthly_cost.amount;
    byWorkload.set(key, cur);
  }
  const level2: CostFlowNode[] = [...byWorkload.entries()].map(([key, v]) => ({ id: `wl:${key}`, label: key.split("::")[1], kind: "workload", amount: moneyOf(v.amt), share: v.amt / totalCost }));

  const links: CostFlowLink[] = [];
  level1.forEach((n) => links.push({ from: "root", to: n.id, amount: n.amount, basis: n.kind === "unattributed" ? "resources with no application tag" : "sum of workload footprints" }));
  byWorkload.forEach((v, key) => links.push({ from: `app:${v.app}`, to: `wl:${key}`, amount: moneyOf(v.amt), basis: "direct resource ownership" }));

  const levels: CostFlowLevel[] = [
    { depth: 0, nodes: level0 },
    { depth: 1, nodes: level1.sort((a, b) => b.amount.amount - a.amount.amount) },
    { depth: 2, nodes: level2.sort((a, b) => b.amount.amount - a.amount.amount) },
  ];

  return {
    levels,
    links,
    total: moneyOf(totalCost),
    unattributed: moneyOf(unattributed),
    period: { start: "2026-08-01T00:00:00Z", end: "2026-08-31T06:00:00Z" },
  };
}
