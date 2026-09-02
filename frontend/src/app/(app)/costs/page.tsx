"use client";
import * as React from "react";
import { AlertTriangle, ArrowDown, ArrowUp, Minus } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { StatTile } from "@/components/shared/stat-tile";
import { MoneyDisplay } from "@/components/shared/money-display";
import { SeverityBadge } from "@/components/shared/risk-badge";
import { QueryBoundary, LoadingBlock, EmptyState } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import {
  ResponsiveContainer,
  ComposedChart,
  Area,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip as RTooltip,
  ReferenceDot,
} from "recharts";
import {
  useCostSummary,
  useCostSeries,
  useCostBreakdown,
  useCostForecastSeries,
  useCostAnomalies,
  useCostExplanation,
  type BreakdownDimension,
} from "@/lib/api/costs";
import { cn, formatDate, formatDateTime } from "@/lib/utils";

const DIMENSIONS: { value: BreakdownDimension; label: string }[] = [
  { value: "service", label: "Service" },
  { value: "account", label: "Account" },
  { value: "region", label: "Region" },
  { value: "environment", label: "Environment" },
  { value: "application", label: "Application" },
];

export default function CostsPage() {
  const [dimension, setDimension] = React.useState<BreakdownDimension>("service");
  const summary = useCostSummary();
  const series = useCostSeries(90);
  const forecastSeries = useCostForecastSeries(30);
  const breakdown = useCostBreakdown(dimension);
  const anomalies = useCostAnomalies();
  const explanation = useCostExplanation();

  return (
    <div>
      <PageHeader title="Cost intelligence" description="Trend, breakdowns, forecast and anomalies across the connected AWS estate." />

      <QueryBoundary
        isLoading={summary.isLoading}
        isError={summary.isError}
        error={summary.error}
        data={summary.data}
        onRetry={() => summary.refetch()}
        loading={<div className="grid grid-cols-2 gap-3 lg:grid-cols-4"><LoadingBlock height="h-28" /><LoadingBlock height="h-28" /><LoadingBlock height="h-28" /><LoadingBlock height="h-28" /></div>}
      >
        {(s) => (
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            <StatTile label="Month to date" value={<MoneyDisplay money={s.month_to_date} size="xl" freshness="Cost & Usage Report, ~12h lag" period={s.period} />} changePct={s.change_pct} changeGoodDirection="down" icon={Minus} />
            <StatTile label="Daily average" value={<MoneyDisplay money={s.daily_average} size="xl" />} icon={Minus} />
            <StatTile label="Prior month" value={<MoneyDisplay money={s.prior_month} size="xl" muted />} icon={Minus} />
            <StatTile
              label="Forecast (this month)"
              value={<MoneyDisplay money={s.forecast?.expected} size="xl" />}
              sub={s.forecast ? <>range <MoneyDisplay money={s.forecast.low} size="sm" muted /> – <MoneyDisplay money={s.forecast.high} size="sm" muted /> · {s.forecast.method}</> : undefined}
              icon={Minus}
            />
          </div>
        )}
      </QueryBoundary>

      <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Spend trend &amp; forecast</CardTitle>
            <CardDescription>90-day actuals with a 30-day forecast band. Shaded region is the low/high forecast interval.</CardDescription>
          </CardHeader>
          <CardContent>
            <QueryBoundary isLoading={series.isLoading || forecastSeries.isLoading} isError={series.isError} error={series.error} data={series.data} onRetry={() => series.refetch()}>
              {(sr) => {
                const points = (sr.points ?? []).map((p) => ({ date: p.period?.start ?? "", amount: p.amount?.amount ?? 0 }));
                const fc = (forecastSeries.data as { points?: { date: string; expected: number; low: number; high: number }[] } | undefined)?.points ?? [];
                const fcPoints = fc.map((p) => ({ date: p.date, forecast: p.expected, band: [p.low, p.high] as [number, number] }));
                const merged = [...points, ...fcPoints];
                return (
                  <div className="h-80 w-full">
                    <ResponsiveContainer width="100%" height="100%">
                      <ComposedChart data={merged} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
                        <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" vertical={false} />
                        <XAxis dataKey="date" tickFormatter={(d) => formatDate(d).slice(0, 6)} tick={{ fontSize: 10 }} minTickGap={40} />
                        <YAxis tick={{ fontSize: 10 }} tickFormatter={(v) => `$${(v / 1000).toFixed(0)}k`} width={44} />
                        <RTooltip
                          formatter={(v: number, name: string) => [`$${Number(v).toLocaleString(undefined, { maximumFractionDigits: 0 })}`, name]}
                          labelFormatter={(d) => formatDate(d)}
                          contentStyle={{ background: "hsl(var(--popover))", border: "1px solid hsl(var(--border))", borderRadius: 8, fontSize: 12 }}
                        />
                        <Area type="monotone" dataKey="band" stroke="none" fill="hsl(var(--chart-4))" fillOpacity={0.15} name="Forecast range" />
                        <Line type="monotone" dataKey="amount" stroke="hsl(var(--primary))" strokeWidth={2} dot={false} name="Actual" />
                        <Line type="monotone" dataKey="forecast" stroke="hsl(var(--chart-4))" strokeWidth={2} strokeDasharray="4 3" dot={false} name="Forecast" />
                        {(anomalies.data ?? []).map((a) => (
                          <ReferenceDot key={a.id} x={a.period?.start} y={a.actual?.amount} r={4} fill="hsl(var(--destructive))" stroke="none" />
                        ))}
                      </ComposedChart>
                    </ResponsiveContainer>
                  </div>
                );
              }}
            </QueryBoundary>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Why did cost change?</CardTitle>
            <CardDescription>Contributor decomposition, current vs. baseline period.</CardDescription>
          </CardHeader>
          <CardContent>
            <QueryBoundary isLoading={explanation.isLoading} isError={explanation.isError} error={explanation.error} data={explanation.data} onRetry={() => explanation.refetch()}>
              {(e) => (
                <div className="space-y-3">
                  <div className="flex items-baseline justify-between">
                    <MoneyDisplay money={e.current_total} size="lg" />
                    <span className={cn("flex items-center gap-1 text-sm font-medium", (e.delta_pct ?? 0) >= 0 ? "text-destructive" : "text-success")}>
                      {(e.delta_pct ?? 0) >= 0 ? <ArrowUp className="h-3.5 w-3.5" /> : <ArrowDown className="h-3.5 w-3.5" />}
                      {Math.abs(e.delta_pct ?? 0).toFixed(1)}%
                    </span>
                  </div>
                  <p className="text-xs text-muted-foreground">vs. <MoneyDisplay money={e.baseline_total} size="sm" muted /> baseline</p>
                  <ul className="space-y-2">
                    {(e.contributors ?? []).slice(0, 6).map((c, i) => (
                      <li key={i} className="flex items-center justify-between text-xs">
                        <span className="truncate">{c.key}</span>
                        <span className="flex items-center gap-2 shrink-0">
                          <MoneyDisplay money={c.delta} size="sm" signed />
                          <span className="w-9 text-right text-muted-foreground tabular-nums">{((c.share ?? 0) * 100).toFixed(0)}%</span>
                        </span>
                      </li>
                    ))}
                  </ul>
                  {e.narrative && <p className="border-t border-border pt-2 text-xs text-muted-foreground">{e.narrative}</p>}
                  {!!(e.linked_changes ?? []).length && (
                    <div className="border-t border-border pt-2">
                      <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Linked changes</p>
                      <ul className="space-y-1">
                        {e.linked_changes!.map((lc, i) => (
                          <li key={i} className="text-xs">
                            <Badge variant="secondary" className="mr-1.5">{lc.kind}</Badge>
                            {lc.label}
                          </li>
                        ))}
                      </ul>
                    </div>
                  )}
                </div>
              )}
            </QueryBoundary>
          </CardContent>
        </Card>
      </div>

      <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader className="flex-row items-center justify-between space-y-0">
            <div>
              <CardTitle>Breakdown</CardTitle>
              <CardDescription>Spend by {dimension}, with prior-period comparison.</CardDescription>
            </div>
            <Tabs value={dimension} onValueChange={(v) => setDimension(v as BreakdownDimension)}>
              <TabsList>
                {DIMENSIONS.map((d) => (
                  <TabsTrigger key={d.value} value={d.value}>{d.label}</TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          </CardHeader>
          <CardContent>
            <QueryBoundary isLoading={breakdown.isLoading} isError={breakdown.isError} error={breakdown.error} data={breakdown.data} onRetry={() => breakdown.refetch()} isEmpty={(d) => !(d.items ?? []).length} empty={<EmptyState title="No breakdown data" />}>
              {(b) => (
                <ul className="space-y-1.5">
                  {(b.items ?? []).map((item) => (
                    <li key={item.key} className="flex items-center gap-3 rounded-md px-1.5 py-1.5 text-sm hover:bg-secondary/40">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center justify-between">
                          <span className="truncate font-medium">{item.label ?? item.key}</span>
                          <span className="flex shrink-0 items-center gap-3">
                            <span className={cn("text-xs tabular-nums", (item.change_pct ?? 0) > 0 ? "text-destructive" : "text-success")}>
                              {(item.change_pct ?? 0) > 0 ? "+" : ""}{(item.change_pct ?? 0).toFixed(1)}%
                            </span>
                            <MoneyDisplay money={item.amount} size="sm" />
                          </span>
                        </div>
                        <div className="mt-1 h-1.5 w-full rounded-full bg-secondary">
                          <div className="h-1.5 rounded-full bg-primary" style={{ width: `${Math.min(100, (item.share ?? 0) * 100)}%` }} />
                        </div>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </QueryBoundary>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-1.5"><AlertTriangle className="h-4 w-4 text-warning" /> Anomalies</CardTitle>
            <CardDescription>Statistically significant deviations from expected spend.</CardDescription>
          </CardHeader>
          <CardContent>
            <QueryBoundary isLoading={anomalies.isLoading} isError={anomalies.isError} error={anomalies.error} data={anomalies.data} onRetry={() => anomalies.refetch()} isEmpty={(d) => d.length === 0} empty={<EmptyState title="No anomalies detected" description="Spend is tracking within expected bounds." />}>
              {(list) => (
                <ul className="space-y-2.5">
                  {list.map((a) => (
                    <li key={a.id} className="rounded-md border border-border p-2.5">
                      <div className="flex items-center justify-between text-xs">
                        <span className="font-medium">{a.key} <span className="text-muted-foreground">({a.dimension})</span></span>
                        <SeverityBadge level={a.severity ?? "MEDIUM"} />
                      </div>
                      <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
                        <span>Expected <MoneyDisplay money={a.expected} size="sm" muted /></span>
                        <span>Actual <MoneyDisplay money={a.actual} size="sm" /></span>
                        <span className="text-destructive">+{(a.delta_pct ?? 0).toFixed(0)}%</span>
                      </div>
                      {a.explanation && <p className="mt-1.5 text-[11px] text-muted-foreground">{a.explanation}</p>}
                      <p className="mt-1 text-[10px] text-muted-foreground">Detected {a.detected_at ? formatDateTime(a.detected_at) : "—"}</p>
                    </li>
                  ))}
                </ul>
              )}
            </QueryBoundary>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
