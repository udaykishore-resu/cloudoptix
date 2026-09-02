"use client";
import Link from "next/link";
import { AlertTriangle, DollarSign, Gauge, PiggyBank, Sparkles, TrendingDown } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { StatTile } from "@/components/shared/stat-tile";
import { MoneyDisplay } from "@/components/shared/money-display";
import { SavingsFunnelChart } from "@/components/shared/savings-funnel-chart";
import { ErrorState, LoadingBlock, LoadingCards } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { RiskBadge } from "@/components/shared/risk-badge";
import { ConfidenceBadge } from "@/components/shared/confidence-badge";
import { useExecutiveSummary, useEfficiencyScore } from "@/lib/api/economics";
import { cn, formatDateTime } from "@/lib/utils";

export default function OverviewPage() {
  const summary = useExecutiveSummary();
  const efficiency = useEfficiencyScore();

  return (
    <div>
      <PageHeader
        title="Executive overview"
        description={summary.data ? `Generated ${formatDateTime(summary.data.generated_at ?? "")} · period ${summary.data.period?.start?.slice(0, 10)} – ${summary.data.period?.end?.slice(0, 10)}` : "Loading…"}
        actions={
          <Button asChild size="sm" variant="outline">
            <Link href="/recommendations">View all opportunities</Link>
          </Button>
        }
      />

      {summary.isLoading && <LoadingCards count={4} />}
      {summary.isError && <ErrorState error={summary.error} onRetry={() => summary.refetch()} />}
      {summary.data && (
        <>
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            <StatTile
              label="Monthly spend (MTD)"
              value={<MoneyDisplay money={summary.data.monthly_spend} size="xl" freshness="Cost & Usage Report, ~12h lag" />}
              changePct={summary.data.spend_change_pct}
              changeGoodDirection="down"
              sub={<>Forecast month-end: <MoneyDisplay money={summary.data.forecast_month_end} size="sm" /></>}
              icon={DollarSign}
            />
            <StatTile
              label="Potential vs realized savings"
              value={<MoneyDisplay money={summary.data.realized_savings} size="xl" />}
              sub={<>of <MoneyDisplay money={summary.data.potential_savings} size="sm" muted /> potential/mo identified</>}
              icon={PiggyBank}
              tone="success"
            />
            <StatTile
              label="Waste %"
              value={`${summary.data.waste_pct.toFixed(1)}%`}
              sub="Share of spend on findings classified as waste"
              icon={TrendingDown}
              tone={summary.data.waste_pct > 15 ? "warning" : "default"}
            />
            <StatTile
              label="Cloud Efficiency Score"
              value={<span className="flex items-baseline gap-1.5">{summary.data.efficiency_score.toFixed(0)}<span className="text-sm font-normal text-muted-foreground">/ 100</span><Badge variant="secondary" className="ml-1">{summary.data.efficiency_grade}</Badge></span>}
              sub="Weighted composite across 8 factors"
              icon={Gauge}
            />
          </div>

          <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-3">
            <Card className="xl:col-span-2">
              <CardHeader>
                <CardTitle>Cloud Efficiency Score — factor breakdown</CardTitle>
                <CardDescription>Each point of improvement maps to a concrete action; opportunity is the estimated monthly saving from closing that factor&rsquo;s gap.</CardDescription>
              </CardHeader>
              <CardContent>
                {efficiency.isLoading && <LoadingBlock height="h-56" />}
                {efficiency.data && (
                  <ul className="space-y-3">
                    {efficiency.data.factors.map((f) => (
                      <li key={f.name}>
                        <div className="mb-1 flex items-center justify-between text-xs">
                          <span className="font-medium capitalize">{f.name.replace(/_/g, " ")}</span>
                          <span className="flex items-center gap-2 text-muted-foreground">
                            {f.opportunity && f.opportunity.amount > 0 && <MoneyDisplay money={f.opportunity} size="sm" suffix="opportunity" />}
                            <span className="tabular-nums font-medium text-foreground">{f.score.toFixed(0)}/100</span>
                          </span>
                        </div>
                        <Progress value={f.score} indicatorClassName={cn(f.score >= 80 ? "bg-success" : f.score >= 60 ? "bg-warning" : "bg-destructive")} />
                        <p className="mt-1 text-[11px] text-muted-foreground">{f.detail}</p>
                      </li>
                    ))}
                  </ul>
                )}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>Cost SLO status</CardTitle>
                <CardDescription>Economic error budget health across declared objectives.</CardDescription>
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="grid grid-cols-3 gap-2 text-center">
                  <div className="rounded-md bg-success/10 p-2">
                    <div className="text-lg font-semibold text-success tabular-nums">{summary.data.cost_slos_healthy}</div>
                    <div className="text-[10px] text-muted-foreground">Healthy</div>
                  </div>
                  <div className="rounded-md bg-warning/10 p-2">
                    <div className="text-lg font-semibold text-warning tabular-nums">{summary.data.cost_slos_at_risk}</div>
                    <div className="text-[10px] text-muted-foreground">At risk</div>
                  </div>
                  <div className="rounded-md bg-destructive/10 p-2">
                    <div className="text-lg font-semibold text-destructive tabular-nums">{summary.data.cost_slos_breached}</div>
                    <div className="text-[10px] text-muted-foreground">Breached</div>
                  </div>
                </div>
                <ul className="space-y-2.5">
                  {summary.data.budget_states?.map((b) => (
                    <li key={b.id} className="rounded-md border border-border p-2.5">
                      <div className="flex items-center justify-between text-xs">
                        <span className="font-medium">{b.slo_name}</span>
                        <BudgetPill state={b.state} />
                      </div>
                      <Progress value={Math.min(100, (b.consumed_ratio ?? 0) * 100)} className="mt-1.5 h-1.5" indicatorClassName={cn(b.state === "healthy" ? "bg-success" : b.state === "watch" ? "bg-warning" : "bg-destructive")} />
                      <p className="mt-1 text-[11px] text-muted-foreground">{b.explanation}</p>
                    </li>
                  ))}
                </ul>
                <Button asChild size="sm" variant="outline" className="w-full">
                  <Link href="/slos">View all SLOs</Link>
                </Button>
              </CardContent>
            </Card>
          </div>

          <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-3">
            <Card className="xl:col-span-2">
              <CardHeader className="flex-row items-center justify-between space-y-0">
                <div>
                  <CardTitle>Top opportunities</CardTitle>
                  <CardDescription>Highest-priority open recommendations right now.</CardDescription>
                </div>
                <Button asChild size="sm" variant="ghost">
                  <Link href="/recommendations">See all →</Link>
                </Button>
              </CardHeader>
              <CardContent className="space-y-2">
                {summary.data.top_opportunities?.length ? (
                  summary.data.top_opportunities.map((r) => (
                    <Link
                      key={r.id}
                      href={`/recommendations?id=${r.id}`}
                      className="focus-ring flex items-center justify-between gap-3 rounded-md border border-border p-2.5 text-sm hover:border-border-strong hover:bg-secondary/40"
                    >
                      <div className="min-w-0">
                        <p className="truncate font-medium">{r.title}</p>
                        <p className="truncate text-xs text-muted-foreground">{r.finding.resource_name} · {r.finding.environment}</p>
                      </div>
                      <div className="flex shrink-0 items-center gap-2">
                        <RiskBadge level={r.risk.level} />
                        <ConfidenceBadge confidence={r.confidence} size="sm" />
                        <MoneyDisplay money={r.estimated_monthly_saving} size="sm" className="w-20 text-right" />
                      </div>
                    </Link>
                  ))
                ) : (
                  <p className="py-6 text-center text-sm text-muted-foreground">No open opportunities — nice work.</p>
                )}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-1.5"><Sparkles className="h-4 w-4 text-primary" /> Savings funnel</CardTitle>
                <CardDescription>Potential → realized, this period.</CardDescription>
              </CardHeader>
              <CardContent>
                <SavingsFunnelChart funnel={summary.data.savings_funnel} />
              </CardContent>
            </Card>
          </div>
        </>
      )}
    </div>
  );
}

function BudgetPill({ state }: { state: string }) {
  const map: Record<string, { label: string; variant: "success" | "warning" | "destructive" | "muted" }> = {
    healthy: { label: "Healthy", variant: "success" },
    watch: { label: "Watch", variant: "warning" },
    at_risk: { label: "At risk", variant: "warning" },
    exhausted: { label: "Exhausted", variant: "destructive" },
    breached: { label: "Breached", variant: "destructive" },
    unknown: { label: "Unknown", variant: "muted" },
  };
  const cfg = map[state] ?? map.unknown;
  return (
    <Badge variant={cfg.variant} className="gap-1">
      {(cfg.variant === "destructive") && <AlertTriangle className="h-3 w-3" />}
      {cfg.label}
    </Badge>
  );
}
