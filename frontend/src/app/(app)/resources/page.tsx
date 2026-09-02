"use client";
import * as React from "react";
import { useReactTable, getCoreRowModel, getSortedRowModel, flexRender, createColumnHelper, type SortingState } from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDown, ArrowUp, ArrowUpDown, Bookmark, Search, X } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { RiskBadge } from "@/components/shared/risk-badge";
import { ResourceIcon } from "@/components/shared/resource-icon";
import { PercentileChart } from "@/components/shared/percentile-chart";
import { ErrorState, EmptyState } from "@/components/shared/states";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { useResources, useResource, type ResourceFilter } from "@/lib/api/twin";
import { useRecommendations } from "@/lib/api/recommendations";
import { getWorld } from "@/lib/mock/world";
import { CATEGORY_LABEL, KIND_CATEGORY } from "@/lib/mock/kinds";
import { cn, formatDate } from "@/lib/utils";
import type { Resource } from "@/types/domain";

const columnHelper = createColumnHelper<Resource>();

export default function ResourcesPage() {
  const [search, setSearch] = React.useState("");
  const [environment, setEnvironment] = React.useState<string>("all");
  const [category, setCategory] = React.useState<string>("all");
  const [accountId, setAccountId] = React.useState<string>("all");
  const [sorting, setSorting] = React.useState<SortingState>([{ id: "monthly_cost", desc: true }]);
  const [selectedId, setSelectedId] = React.useState<string | undefined>();

  const filter: ResourceFilter = {
    search: search || undefined,
    environments: environment !== "all" ? [environment] : undefined,
    accountIds: accountId !== "all" ? [accountId] : undefined,
  };
  const resources = useResources(filter);
  const world = React.useMemo(() => getWorld(), []);

  const filtered = React.useMemo(() => {
    let list = resources.data ?? [];
    if (category !== "all") list = list.filter((r) => KIND_CATEGORY(r.kind) === category);
    return list;
  }, [resources.data, category]);

  const columns = React.useMemo(
    () => [
      columnHelper.accessor("name", {
        header: "Resource",
        cell: (info) => (
          <div className="flex min-w-0 items-center gap-2">
            <ResourceIcon kind={info.row.original.kind} className="shrink-0 text-muted-foreground" />
            <div className="min-w-0">
              <p className="truncate font-medium">{info.getValue() ?? info.row.original.native_id}</p>
              <p className="truncate text-[10px] text-muted-foreground">{info.row.original.kind}</p>
            </div>
          </div>
        ),
      }),
      columnHelper.accessor((r) => r.application ?? "—", { id: "application", header: "Application" }),
      columnHelper.accessor("environment", {
        header: "Env",
        cell: (info) => <Badge variant="secondary" className="capitalize">{info.getValue()}</Badge>,
      }),
      columnHelper.accessor("region", { header: "Region" }),
      columnHelper.accessor("state", {
        header: "State",
        cell: (info) => <span className={cn("capitalize", info.getValue() === "idle" && "text-warning", info.getValue() === "running" && "text-success")}>{info.getValue()}</span>,
      }),
      columnHelper.accessor((r) => r.cpu?.p50 ?? -1, {
        id: "cpu",
        header: "CPU p50",
        cell: (info) => (info.getValue() >= 0 ? `${info.getValue().toFixed(0)}%` : "—"),
      }),
      columnHelper.accessor((r) => r.monthly_cost.amount, {
        id: "monthly_cost",
        header: "Monthly cost",
        cell: (info) => <MoneyDisplay money={info.row.original.monthly_cost} size="sm" />,
      }),
      columnHelper.accessor((r) => r.finding_count ?? 0, {
        id: "finding_count",
        header: "Findings",
        cell: (info) => (info.getValue() > 0 ? <Badge variant="warning">{info.getValue()}</Badge> : <span className="text-muted-foreground">—</span>),
      }),
    ],
    []
  );

  const table = useReactTable({
    data: filtered,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });

  const rows = table.getRowModel().rows;
  const parentRef = React.useRef<HTMLDivElement>(null);
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 44,
    overscan: 12,
  });
  const virtualRows = virtualizer.getVirtualItems();
  const totalSize = virtualizer.getTotalSize();
  const paddingTop = virtualRows.length > 0 ? virtualRows[0].start : 0;
  const paddingBottom = virtualRows.length > 0 ? totalSize - virtualRows[virtualRows.length - 1].end : 0;

  const totalCost = filtered.reduce((s, r) => s + r.monthly_cost.amount, 0);

  return (
    <div>
      <PageHeader
        title="Resource explorer"
        description={`${filtered.length.toLocaleString()} resources · $${totalCost.toLocaleString(undefined, { maximumFractionDigits: 0 })}/mo across the filtered set`}
        actions={
          <Button size="sm" variant="outline">
            <Bookmark className="h-3.5 w-3.5" /> Saved views
          </Button>
        }
      />

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="relative w-64">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input placeholder="Search name, ID, kind…" value={search} onChange={(e) => setSearch(e.target.value)} className="h-8 pl-8 text-xs" />
        </div>
        <Select value={environment} onValueChange={setEnvironment}>
          <SelectTrigger className="h-8 w-40 text-xs"><SelectValue placeholder="Environment" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All environments</SelectItem>
            {["production", "staging", "development", "shared_services"].map((e) => (
              <SelectItem key={e} value={e} className="capitalize">{e}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={category} onValueChange={setCategory}>
          <SelectTrigger className="h-8 w-40 text-xs"><SelectValue placeholder="Category" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All categories</SelectItem>
            {Object.entries(CATEGORY_LABEL).map(([k, v]) => (
              <SelectItem key={k} value={k}>{v}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={accountId} onValueChange={setAccountId}>
          <SelectTrigger className="h-8 w-48 text-xs"><SelectValue placeholder="Account" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All accounts</SelectItem>
            {world.accounts.map((a) => (
              <SelectItem key={a.id} value={a.id}>{a.alias}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        {(search || environment !== "all" || category !== "all" || accountId !== "all") && (
          <Button size="sm" variant="ghost" onClick={() => { setSearch(""); setEnvironment("all"); setCategory("all"); setAccountId("all"); }}>
            <X className="h-3.5 w-3.5" /> Clear
          </Button>
        )}
      </div>

      {resources.isError && <ErrorState error={resources.error} onRetry={() => resources.refetch()} />}
      {resources.isLoading && (
        <div className="space-y-1.5">
          {Array.from({ length: 10 }).map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}
        </div>
      )}
      {resources.data && filtered.length === 0 && <EmptyState title="No resources match these filters" />}

      {resources.data && filtered.length > 0 && (
        <div className="overflow-hidden rounded-lg border border-border">
          <div className="overflow-x-auto">
            <div className="min-w-[900px]">
              <div className="sticky top-0 z-10 flex border-b border-border bg-surface">
                {table.getHeaderGroups().map((hg) =>
                  hg.headers.map((h) => (
                    <button
                      key={h.id}
                      onClick={h.column.getToggleSortingHandler()}
                      className={cn(
                        "flex h-9 flex-1 items-center gap-1 whitespace-nowrap px-3 text-left text-xs font-medium text-muted-foreground hover:text-foreground",
                        h.column.id === "name" && "flex-[2]"
                      )}
                    >
                      {flexRender(h.column.columnDef.header, h.getContext())}
                      {h.column.getIsSorted() === "asc" ? <ArrowUp className="h-3 w-3" /> : h.column.getIsSorted() === "desc" ? <ArrowDown className="h-3 w-3" /> : <ArrowUpDown className="h-3 w-3 opacity-30" />}
                    </button>
                  ))
                )}
              </div>
              <div ref={parentRef} className="max-h-[65vh] overflow-y-auto">
                <div style={{ height: totalSize, position: "relative" }}>
                  <div style={{ transform: `translateY(${paddingTop}px)` }}>
                    {virtualRows.map((vr) => {
                      const row = rows[vr.index];
                      return (
                        <div
                          key={row.id}
                          onClick={() => setSelectedId(row.original.id)}
                          className="focus-ring flex cursor-pointer items-center border-b border-border text-sm hover:bg-secondary/40"
                          style={{ height: vr.size }}
                        >
                          {row.getVisibleCells().map((cell) => (
                            <div key={cell.id} className={cn("flex-1 truncate px-3", cell.column.id === "name" && "flex-[2]")}>
                              {flexRender(cell.column.columnDef.cell, cell.getContext())}
                            </div>
                          ))}
                        </div>
                      );
                    })}
                  </div>
                  <div style={{ height: paddingBottom }} />
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      <ResourceDetailSheet id={selectedId} onClose={() => setSelectedId(undefined)} />
    </div>
  );
}

function ResourceDetailSheet({ id, onClose }: { id: string | undefined; onClose: () => void }) {
  const resource = useResource(id);
  const recs = useRecommendations();
  const findings = (recs.data ?? []).filter((r) => r.finding.resource_id === id);

  return (
    <Sheet open={!!id} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
        {resource.data && (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2"><ResourceIcon kind={resource.data.kind} />{resource.data.name ?? resource.data.native_id}</SheetTitle>
              <SheetDescription>{resource.data.kind} · {resource.data.native_id}</SheetDescription>
            </SheetHeader>
            <div className="space-y-4 px-4 pb-6">
              <div className="rounded-md border border-border p-3">
                <MoneyDisplay money={resource.data.monthly_cost} size="lg" period={undefined} freshness="Cost & Usage Report" />
                {resource.data.potential_saving && resource.data.potential_saving.amount > 0 && (
                  <p className="mt-1 text-xs text-success">Potential saving <MoneyDisplay money={resource.data.potential_saving} size="sm" />/mo</p>
                )}
              </div>
              <div className="grid grid-cols-2 gap-2 text-xs">
                <Info label="Account" value={resource.data.account_id} />
                <Info label="Region" value={resource.data.region} />
                <Info label="Environment" value={resource.data.environment} />
                <Info label="State" value={resource.data.state} />
                <Info label="Application" value={resource.data.application ?? "—"} />
                <Info label="Workload" value={resource.data.workload ?? "—"} />
                <Info label="Owner" value={resource.data.owner ?? "—"} />
                <Info label="Purchase model" value={resource.data.purchase_model.replace(/_/g, " ")} />
                <Info label="Criticality" value={resource.data.criticality} />
                <Info label="First seen" value={formatDate(resource.data.first_seen_at)} />
              </div>
              {resource.data.cpu && (
                <div>
                  <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">CPU utilization</p>
                  <PercentileChart percentiles={resource.data.cpu} unit="%" />
                </div>
              )}
              {resource.data.memory && (
                <div>
                  <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Memory utilization</p>
                  <PercentileChart percentiles={resource.data.memory} unit="%" />
                </div>
              )}
              <div>
                <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Findings ({findings.length})</p>
                {findings.length === 0 && <p className="text-xs text-muted-foreground">No open findings for this resource.</p>}
                <ul className="space-y-1.5">
                  {findings.map((f) => (
                    <li key={f.id} className="flex items-center justify-between rounded-md border border-border px-2 py-1.5 text-xs">
                      <span className="truncate">{f.title}</span>
                      <span className="flex shrink-0 items-center gap-2">
                        <RiskBadge level={f.risk.level} />
                        <MoneyDisplay money={f.estimated_monthly_saving} size="sm" />
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
              {resource.data.tags && Object.keys(resource.data.tags).length > 0 && (
                <div>
                  <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Tags</p>
                  <div className="flex flex-wrap gap-1">
                    {Object.entries(resource.data.tags).map(([k, v]) => (
                      <Badge key={k} variant="secondary" className="text-[10px]">{k}={String(v)}</Badge>
                    ))}
                  </div>
                </div>
              )}
              {((resource.data.dependencies?.length ?? 0) > 0 || (resource.data.dependents?.length ?? 0) > 0) && (
                <div className="grid grid-cols-2 gap-3 text-xs">
                  <div>
                    <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Depends on</p>
                    <p className="tabular-nums">{resource.data.dependencies?.length ?? 0}</p>
                  </div>
                  <div>
                    <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Depended on by</p>
                    <p className="tabular-nums">{resource.data.dependents?.length ?? 0}</p>
                  </div>
                </div>
              )}
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
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
