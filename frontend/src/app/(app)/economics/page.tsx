"use client";
import * as React from "react";
import { ResponsiveContainer, LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip as RTooltip } from "recharts";
import { ArrowRight, Layers } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { ProvenanceChip } from "@/components/shared/provenance-chip";
import { QueryBoundary, EmptyState } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { useFootprints, useTransactions, useUnitEconomics, useUnitEconomicsHistory } from "@/lib/api/economics";
import { cn, formatDate } from "@/lib/utils";
import type { BusinessTransaction } from "@/types/api";

export default function EconomicsPage() {
  const [selectedTx, setSelectedTx] = React.useState<BusinessTransaction | undefined>();
  const footprints = useFootprints();
  const transactions = useTransactions();
  const unitEconomics = useUnitEconomics();
  const history = useUnitEconomicsHistory(selectedTx?.id);

  const ue = (id: string | undefined) => unitEconomics.data?.find((u) => u.transaction_id === id);

  return (
    <div>
      <PageHeader title="Economics" description="Economic footprints by application and workload, and cost per business transaction over time." />

      <Card className="mb-4">
        <CardHeader>
          <CardTitle>Application footprints</CardTitle>
          <CardDescription>Direct, indirect and shared cost split, with attribution coverage.</CardDescription>
        </CardHeader>
        <CardContent>
          <QueryBoundary isLoading={footprints.isLoading} isError={footprints.isError} error={footprints.error} data={footprints.data} onRetry={() => footprints.refetch()} isEmpty={(d) => d.length === 0} empty={<EmptyState title="No footprints computed yet" />}>
            {(list) => (
              <div className="space-y-2.5">
                {list.map((f) => {
                  const total = f.total?.amount || 1;
                  const directPct = ((f.direct?.amount ?? 0) / total) * 100;
                  const indirectPct = ((f.indirect?.amount ?? 0) / total) * 100;
                  const sharedPct = ((f.shared?.amount ?? 0) / total) * 100;
                  return (
                    <div key={f.id} className="rounded-md border border-border p-3">
                      <div className="flex items-center justify-between">
                        <span className="flex items-center gap-1.5 font-medium"><Layers className="h-3.5 w-3.5 text-muted-foreground" />{f.label}</span>
                        <span className="flex items-center gap-3 text-xs">
                          <span className="text-muted-foreground">{((f.coverage ?? 0) * 100).toFixed(0)}% attributed</span>
                          <MoneyDisplay money={f.total} size="sm" />
                        </span>
                      </div>
                      <div className="mt-2 flex h-2.5 w-full overflow-hidden rounded-full bg-secondary">
                        <div className="h-full bg-chart-1" style={{ width: `${directPct}%` }} title={`Direct: ${f.direct?.display}`} />
                        <div className="h-full bg-chart-3" style={{ width: `${indirectPct}%` }} title={`Indirect: ${f.indirect?.display}`} />
                        <div className="h-full bg-chart-4" style={{ width: `${sharedPct}%` }} title={`Shared: ${f.shared?.display}`} />
                      </div>
                      <div className="mt-1.5 flex items-center gap-4 text-[11px] text-muted-foreground">
                        <Legend color="bg-chart-1" label="Direct" value={f.direct} />
                        <Legend color="bg-chart-3" label="Indirect" value={f.indirect} />
                        <Legend color="bg-chart-4" label="Shared" value={f.shared} />
                        {(f.unattributed?.amount ?? 0) > 0 && <span className="ml-auto">+{f.unattributed?.display} unattributed</span>}
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </QueryBoundary>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Cost per transaction trend</CardTitle>
            <CardDescription>{selectedTx ? `Trailing 12 months for ${selectedTx.name}` : "Select a transaction to see its trend"}</CardDescription>
          </CardHeader>
          <CardContent>
            {!selectedTx && <p className="py-14 text-center text-sm text-muted-foreground">Pick a business transaction from the list to chart its unit cost history.</p>}
            {selectedTx && (
              <QueryBoundary isLoading={history.isLoading} isError={history.isError} error={history.error} data={history.data}>
                {(hist) => {
                  const points = hist.map((h) => ({ date: h.period?.start ?? "", cpu: h.cost_per_unit?.amount ?? 0 }));
                  return (
                    <div className="h-64 w-full">
                      <ResponsiveContainer width="100%" height="100%">
                        <LineChart data={points} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                          <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" vertical={false} />
                          <XAxis dataKey="date" tickFormatter={(d) => formatDate(d).slice(0, 6)} tick={{ fontSize: 10 }} minTickGap={30} />
                          <YAxis tick={{ fontSize: 10 }} tickFormatter={(v) => `$${v.toFixed(3)}`} width={54} />
                          <RTooltip
                            formatter={(v: number) => [`$${v.toFixed(4)}`, "Cost / unit"]}
                            labelFormatter={(d) => formatDate(d)}
                            contentStyle={{ background: "hsl(var(--popover))", border: "1px solid hsl(var(--border))", borderRadius: 8, fontSize: 12 }}
                          />
                          <Line type="monotone" dataKey="cpu" stroke="hsl(var(--primary))" strokeWidth={2} dot={false} />
                        </LineChart>
                      </ResponsiveContainer>
                    </div>
                  );
                }}
              </QueryBoundary>
            )}
            {selectedTx && (() => {
              const u = ue(selectedTx.id);
              if (!u) return null;
              return (
                <div className="mt-3 space-y-2 border-t border-border pt-3">
                  <p className="text-[11px] font-medium uppercase text-muted-foreground">Driver decomposition (this period)</p>
                  {(u.drivers ?? []).map((d, i) => (
                    <div key={i} className="text-xs">
                      <div className="flex items-center justify-between">
                        <span className="text-muted-foreground">{d.label}</span>
                        <span className="flex items-center gap-2">
                          <MoneyDisplay money={d.impact} size="sm" signed />
                          <span className="w-9 text-right tabular-nums text-muted-foreground">{((d.impact_share ?? 0) * 100).toFixed(0)}%</span>
                        </span>
                      </div>
                      <p className="text-[11px] text-muted-foreground">{d.explanation}</p>
                    </div>
                  ))}
                </div>
              );
            })()}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Business transactions</CardTitle>
            <CardDescription>Unit economics per completed transaction.</CardDescription>
          </CardHeader>
          <CardContent>
            <QueryBoundary isLoading={transactions.isLoading} isError={transactions.isError} error={transactions.error} data={transactions.data} onRetry={() => transactions.refetch()}>
              {(list) => (
                <ul className="space-y-1.5">
                  {list.map((t) => {
                    const u = ue(t.id);
                    return (
                      <li key={t.id}>
                        <button
                          onClick={() => setSelectedTx(t)}
                          className={cn(
                            "focus-ring flex w-full flex-col gap-1 rounded-md border px-2.5 py-2 text-left text-xs hover:border-border-strong hover:bg-secondary/40",
                            selectedTx?.id === t.id ? "border-primary bg-primary/5" : "border-border"
                          )}
                        >
                          <div className="flex items-center justify-between">
                            <span className="font-medium">{t.name}</span>
                            <ArrowRight className="h-3 w-3 text-muted-foreground" />
                          </div>
                          <p className="text-muted-foreground">{t.description}</p>
                          <div className="flex items-center justify-between">
                            {u ? <MoneyDisplay money={u.cost_per_unit} size="sm" /> : <span className="text-muted-foreground">—</span>}
                            {u && (
                              <span className={cn("tabular-nums", (u.change_pct ?? 0) > 0 ? "text-destructive" : "text-success")}>
                                {(u.change_pct ?? 0) > 0 ? "+" : ""}{(u.change_pct ?? 0).toFixed(1)}%
                              </span>
                            )}
                          </div>
                          <div className="flex items-center gap-1.5">
                            <Badge variant="secondary" className="text-[9px]">{t.criticality}</Badge>
                            {t.provenance && <ProvenanceChip provenance={t.provenance} />}
                          </div>
                        </button>
                      </li>
                    );
                  })}
                </ul>
              )}
            </QueryBoundary>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function Legend({ color, label, value }: { color: string; label: string; value: { display: string } | undefined }) {
  return (
    <span className="flex items-center gap-1">
      <span className={cn("h-2 w-2 rounded-full", color)} />
      {label} {value?.display}
    </span>
  );
}
