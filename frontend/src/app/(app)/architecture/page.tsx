"use client";
import * as React from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  Handle,
  Position,
  MarkerType,
  useNodesState,
  useEdgesState,
  type Node,
  type Edge,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Search, X, Workflow, GitFork } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { WideViewportGate } from "@/components/shared/wide-viewport-gate";
import { QueryBoundary } from "@/components/shared/states";
import { MoneyDisplay } from "@/components/shared/money-display";
import { RiskBadge } from "@/components/shared/risk-badge";
import { ResourceIcon } from "@/components/shared/resource-icon";
import { PercentileChart } from "@/components/shared/percentile-chart";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet";
import { useTwinGraph, useCostFlowGraph, useTwinDependents, useTwinDependencies } from "@/lib/api/twin";
import type { TwinView, TwinNode } from "@/types/domain";
import { cn } from "@/lib/utils";

const VIEWS: { value: TwinView; label: string }[] = [
  { value: "architecture", label: "Architecture" },
  { value: "cost", label: "Cost" },
  { value: "performance", label: "Performance" },
  { value: "reliability", label: "Reliability" },
  { value: "security", label: "Security" },
  { value: "economics", label: "Economics" },
];

const RISK_DOT: Record<string, string> = {
  NONE: "hsl(var(--muted-foreground))",
  LOW: "hsl(var(--success))",
  MEDIUM: "hsl(var(--warning))",
  HIGH: "hsl(var(--destructive))",
  CRITICAL: "hsl(var(--destructive))",
};

function TwinNodeCard({ data }: NodeProps) {
  const n = data as unknown as TwinNode & { onOpen: (id: string) => void; dimmed: boolean; highlighted: boolean };
  return (
    <div
      onClick={() => n.onOpen(n.id)}
      className={cn(
        "focus-ring w-52 cursor-pointer rounded-lg border bg-card px-2.5 py-2 shadow-sm transition-all",
        n.highlighted ? "border-primary ring-2 ring-primary/40" : "border-border",
        n.dimmed && "opacity-30"
      )}
    >
      <Handle type="target" position={Position.Top} className="!bg-border-strong" />
      <Handle type="source" position={Position.Bottom} className="!bg-border-strong" />
      <div className="flex items-center gap-1.5">
        <ResourceIcon kind={n.kind} className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate text-xs font-medium">{n.label}</span>
        <span className="ml-auto h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: RISK_DOT[n.risk] ?? RISK_DOT.NONE }} />
      </div>
      <div className="mt-1 flex items-center justify-between text-[10px] text-muted-foreground">
        <span className="truncate">{n.service} · {n.environment}</span>
        <span className="tabular-nums font-medium text-foreground">{n.monthly_cost.display}</span>
      </div>
      {n.finding_count > 0 && (
        <div className="mt-1">
          <Badge variant="warning" className="px-1 py-0 text-[9px]">{n.finding_count} finding{n.finding_count > 1 ? "s" : ""}</Badge>
        </div>
      )}
    </div>
  );
}

const nodeTypes = { twin: TwinNodeCard };

function layout(nodes: TwinNode[]): Node[] {
  // Simple deterministic column layout grouped by category/service, since
  // this is a mock-data demo without a live layout engine on the backend.
  const groups = new Map<string, TwinNode[]>();
  for (const n of nodes) {
    const key = n.category;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(n);
  }
  const cols = [...groups.keys()];
  const out: Node[] = [];
  cols.forEach((col, ci) => {
    const items = groups.get(col)!;
    items.forEach((n, ri2) => {
      out.push({
        id: n.id,
        type: "twin",
        position: { x: ci * 280, y: ri2 * 110 },
        data: n as unknown as Record<string, unknown>,
      });
    });
  });
  return out;
}

export default function ArchitecturePage() {
  const [view, setView] = React.useState<TwinView>("architecture");
  const [search, setSearch] = React.useState("");
  const [selectedId, setSelectedId] = React.useState<string | undefined>();
  const [mode, setMode] = React.useState<"graph" | "flow">("graph");

  const graph = useTwinGraph({ view, search: search || undefined });
  const costFlow = useCostFlowGraph();
  const dependents = useTwinDependents(selectedId);
  const dependencies = useTwinDependencies(selectedId);

  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);

  const neighborIds = React.useMemo(() => {
    if (!selectedId) return new Set<string>();
    const s = new Set<string>([selectedId]);
    (graph.data?.edges ?? []).forEach((e) => {
      if (e.from === selectedId) s.add(e.to);
      if (e.to === selectedId) s.add(e.from);
    });
    return s;
  }, [selectedId, graph.data]);

  React.useEffect(() => {
    if (!graph.data) return;
    const laidOut = layout(graph.data.nodes);
    setNodes(
      laidOut.map((n) => ({
        ...n,
        data: {
          ...n.data,
          onOpen: (id: string) => setSelectedId(id),
          dimmed: !!selectedId && !neighborIds.has(n.id),
          highlighted: n.id === selectedId,
        },
      }))
    );
    setEdges(
      graph.data.edges.map((e, i) => ({
        id: `e${i}`,
        source: e.from,
        target: e.to,
        label: e.label ?? e.kind,
        animated: e.kind === "invokes",
        style: { stroke: "hsl(var(--border-strong))", opacity: selectedId ? (neighborIds.has(e.from) && neighborIds.has(e.to) ? 1 : 0.15) : 0.7 },
        labelStyle: { fontSize: 9, fill: "hsl(var(--muted-foreground))" },
        markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14, color: "hsl(var(--border-strong))" },
      }))
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graph.data, selectedId, neighborIds]);

  const selectedNode = graph.data?.nodes.find((n) => n.id === selectedId);

  return (
    <div className="flex h-[calc(100vh-6rem)] flex-col">
      <PageHeader
        title="Architecture Digital Twin"
        description="Live graph of the discovered estate. Six lenses on the same topology; click a node for full detail."
        actions={
          <Tabs value={mode} onValueChange={(v) => setMode(v as "graph" | "flow")}>
            <TabsList>
              <TabsTrigger value="graph"><Workflow className="mr-1 h-3.5 w-3.5" />Graph</TabsTrigger>
              <TabsTrigger value="flow"><GitFork className="mr-1 h-3.5 w-3.5" />Cost flow</TabsTrigger>
            </TabsList>
          </Tabs>
        }
      />

      <WideViewportGate>
        {mode === "graph" ? (
          <div className="flex min-h-0 flex-1 flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <Tabs value={view} onValueChange={(v) => setView(v as TwinView)}>
                <TabsList>
                  {VIEWS.map((v) => (
                    <TabsTrigger key={v.value} value={v.value}>{v.label}</TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
              <div className="relative ml-auto w-64">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                <Input placeholder="Search nodes…" value={search} onChange={(e) => setSearch(e.target.value)} className="h-8 pl-8 text-xs" />
              </div>
              {selectedId && (
                <Button size="sm" variant="ghost" onClick={() => setSelectedId(undefined)}>
                  <X className="h-3.5 w-3.5" /> Clear selection
                </Button>
              )}
              {graph.data && (
                <span className="text-xs text-muted-foreground">
                  {graph.data.stats.node_count} nodes · {graph.data.stats.edge_count} edges · <MoneyDisplay money={graph.data.stats.total_cost} size="sm" />/mo
                  {graph.data.truncated && <span className="ml-1 text-warning">(truncated — narrow your search)</span>}
                </span>
              )}
            </div>

            <div className="relative min-h-0 flex-1 overflow-hidden rounded-lg border border-border bg-secondary/20">
              {graph.isLoading && <div className="flex h-full items-center justify-center text-sm text-muted-foreground">Loading graph…</div>}
              {graph.isError && <div className="flex h-full items-center justify-center text-sm text-destructive">Couldn&apos;t load the graph.</div>}
              {graph.data && (
                <ReactFlow
                  nodes={nodes}
                  edges={edges}
                  onNodesChange={onNodesChange}
                  onEdgesChange={onEdgesChange}
                  nodeTypes={nodeTypes}
                  fitView
                  minZoom={0.2}
                  maxZoom={1.5}
                  proOptions={{ hideAttribution: true }}
                >
                  <Background gap={20} color="hsl(var(--border))" />
                  <Controls showInteractive={false} />
                  <MiniMap pannable zoomable className="!bg-card" />
                </ReactFlow>
              )}
              {graph.data?.legend && (
                <div className="absolute bottom-3 left-3 rounded-md border border-border bg-card/95 px-3 py-2 text-[11px] shadow-sm backdrop-blur">
                  <p className="mb-1 font-medium text-muted-foreground">Legend — {VIEWS.find((v) => v.value === view)?.label} view</p>
                  <ul className="space-y-0.5">
                    {Object.entries(graph.data.legend).map(([k, v]) => (
                      <li key={k}>
                        <span className="font-medium">{k}:</span> {v}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          </div>
        ) : (
          <QueryBoundary isLoading={costFlow.isLoading} isError={costFlow.isError} error={costFlow.error} data={costFlow.data} onRetry={() => costFlow.refetch()}>
            {(cf) => (
              <Card className="flex-1 overflow-auto">
                <CardHeader>
                  <CardTitle>Cost flow — root to workload</CardTitle>
                  <p className="text-xs text-muted-foreground">
                    <MoneyDisplay money={cf.total} size="sm" /> total, of which <MoneyDisplay money={cf.unattributed} size="sm" muted /> could not be confidently attributed past this level — shown honestly rather than force-allocated.
                  </p>
                </CardHeader>
                <CardContent>
                  <div className="grid gap-6 overflow-x-auto pb-4" style={{ gridTemplateColumns: `repeat(${cf.levels.length}, minmax(200px, 1fr))` }}>
                    {cf.levels.map((level) => (
                      <div key={level.depth} className="space-y-2">
                        <p className="text-[11px] font-medium uppercase text-muted-foreground">Level {level.depth}</p>
                        {level.nodes.map((n) => (
                          <div key={n.id} className="rounded-md border border-border p-2">
                            <div className="flex items-center justify-between text-xs">
                              <span className="truncate font-medium">{n.label}</span>
                              <span className="tabular-nums text-muted-foreground">{(n.share * 100).toFixed(0)}%</span>
                            </div>
                            <MoneyDisplay money={n.amount} size="sm" />
                            <div className="mt-1 h-1 w-full rounded-full bg-secondary">
                              <div className="h-1 rounded-full bg-primary" style={{ width: `${Math.min(100, n.share * 100)}%` }} />
                            </div>
                          </div>
                        ))}
                        {n_unattributed(level.depth, cf.levels.length, cf.unattributed)}
                      </div>
                    ))}
                  </div>
                </CardContent>
              </Card>
            )}
          </QueryBoundary>
        )}
      </WideViewportGate>

      <Sheet open={!!selectedId} onOpenChange={(o) => !o && setSelectedId(undefined)}>
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
          {selectedNode && (
            <>
              <SheetHeader>
                <SheetTitle className="flex items-center gap-2"><ResourceIcon kind={selectedNode.kind} /> {selectedNode.label}</SheetTitle>
                <SheetDescription>{selectedNode.kind} · {selectedNode.service} · {selectedNode.region}</SheetDescription>
              </SheetHeader>
              <div className="space-y-4 px-4 pb-6">
                <div className="grid grid-cols-2 gap-2 text-xs">
                  <Info label="Account" value={selectedNode.account_id} />
                  <Info label="Environment" value={selectedNode.environment} />
                  <Info label="State" value={selectedNode.state} />
                  <Info label="Criticality" value={selectedNode.criticality} />
                  <Info label="Owner" value={selectedNode.owner ?? "—"} />
                  <Info label="Application" value={selectedNode.application ?? "—"} />
                </div>
                <div className="rounded-md border border-border p-3">
                  <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Cost</p>
                  <div className="flex items-center justify-between">
                    <MoneyDisplay money={selectedNode.monthly_cost} size="lg" />
                    <RiskBadge level={selectedNode.risk} />
                  </div>
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    Economic footprint <MoneyDisplay money={selectedNode.economic_footprint} size="sm" /> ({(selectedNode.cost_share * 100).toFixed(1)}% of total estate)
                  </p>
                  {selectedNode.potential_saving.amount > 0 && (
                    <p className="mt-1 text-[11px] text-success">Potential saving: <MoneyDisplay money={selectedNode.potential_saving} size="sm" /></p>
                  )}
                </div>
                {selectedNode.cpu && (
                  <div>
                    <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">CPU utilization</p>
                    <PercentileChart percentiles={selectedNode.cpu} unit="%" />
                  </div>
                )}
                {(selectedNode.latency_p99_ms !== undefined || selectedNode.availability !== undefined) && (
                  <div className="grid grid-cols-2 gap-2 text-xs">
                    {selectedNode.latency_p99_ms !== undefined && <Info label="p99 latency" value={`${selectedNode.latency_p99_ms.toFixed(0)} ms`} />}
                    {selectedNode.availability !== undefined && <Info label="Availability" value={`${selectedNode.availability.toFixed(2)}%`} />}
                    {selectedNode.error_rate !== undefined && <Info label="Error rate" value={`${(selectedNode.error_rate * 100).toFixed(2)}%`} />}
                  </div>
                )}
                <div>
                  <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Dependencies ({dependencies.data?.length ?? 0})</p>
                  <NodeList nodes={dependencies.data} onSelect={setSelectedId} empty="No upstream dependencies recorded." />
                </div>
                <div>
                  <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Dependents ({dependents.data?.length ?? 0})</p>
                  <NodeList nodes={dependents.data} onSelect={setSelectedId} empty="Nothing depends on this resource." />
                </div>
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function n_unattributed(depth: number, maxDepth: number, unattributed: { display: string; amount: number }) {
  if (depth !== maxDepth - 1 || unattributed.amount <= 0) return null;
  return (
    <div className="rounded-md border border-dashed border-border p-2 text-[11px] text-muted-foreground">
      + {unattributed.display} unattributed
    </div>
  );
}

function Info({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-[10px] uppercase text-muted-foreground">{label}</p>
      <p className="truncate font-medium">{value}</p>
    </div>
  );
}

function NodeList({ nodes, onSelect, empty }: { nodes: TwinNode[] | undefined; onSelect: (id: string) => void; empty: string }) {
  if (!nodes?.length) return <p className="text-xs text-muted-foreground">{empty}</p>;
  return (
    <ul className="space-y-1">
      {nodes.map((n) => (
        <li key={n.id}>
          <button onClick={() => onSelect(n.id)} className="focus-ring flex w-full items-center justify-between gap-2 rounded-md border border-border px-2 py-1.5 text-xs hover:bg-secondary/40">
            <span className="flex items-center gap-1.5 truncate"><ResourceIcon kind={n.kind} className="h-3 w-3" />{n.label}</span>
            <MoneyDisplay money={n.monthly_cost} size="sm" />
          </button>
        </li>
      ))}
    </ul>
  );
}
