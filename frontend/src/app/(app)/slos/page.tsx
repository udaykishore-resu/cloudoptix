"use client";
import * as React from "react";
import { AlertTriangle, CheckCircle2, Flame, History } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { QueryBoundary, EmptyState } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { useCostSLOs, useBudgetStates, useSloViolations } from "@/lib/api/economics";
import { cn, formatDate, formatDateTime } from "@/lib/utils";
import type { CostSLO, BudgetState } from "@/types/domain";
import type { Money } from "@/types/api";

const STATE_TONE: Record<BudgetState, { label: string; badge: string; bar: string }> = {
  healthy: { label: "Healthy", badge: "bg-success/15 text-success", bar: "bg-success" },
  watch: { label: "Watch", badge: "bg-warning/15 text-warning", bar: "bg-warning" },
  at_risk: { label: "At risk", badge: "bg-warning/15 text-warning", bar: "bg-warning" },
  exhausted: { label: "Exhausted", badge: "bg-destructive/15 text-destructive", bar: "bg-destructive" },
  breached: { label: "Breached", badge: "bg-destructive/15 text-destructive", bar: "bg-destructive" },
  unknown: { label: "Unknown", badge: "bg-muted text-muted-foreground", bar: "bg-muted-foreground" },
};

export default function SlosPage() {
  const slos = useCostSLOs();
  const budgets = useBudgetStates();

  const joined = React.useMemo(() => {
    if (!slos.data || !budgets.data) return [];
    return slos.data.map((s) => ({ slo: s, budget: budgets.data!.find((b) => b.slo_id === s.id) }));
  }, [slos.data, budgets.data]);

  const counts = React.useMemo(() => {
    const c = { healthy: 0, watch: 0, at_risk: 0, exhausted: 0, breached: 0 };
    joined.forEach((j) => {
      const s = j.budget?.state ?? "unknown";
      if (s in c) c[s as keyof typeof c]++;
    });
    return c;
  }, [joined]);

  return (
    <div>
      <PageHeader title="Cost SLOs" description="Declared cost objectives, economic error budgets, and burn-rate status." />

      <div className="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-5">
        <CountTile label="Total SLOs" value={joined.length} icon={CheckCircle2} />
        <CountTile label="Healthy" value={counts.healthy} tone="success" icon={CheckCircle2} />
        <CountTile label="Watch / at risk" value={counts.watch + counts.at_risk} tone="warning" icon={Flame} />
        <CountTile label="Exhausted" value={counts.exhausted} tone="destructive" icon={AlertTriangle} />
        <CountTile label="Breached" value={counts.breached} tone="destructive" icon={AlertTriangle} />
      </div>

      <QueryBoundary
        isLoading={slos.isLoading || budgets.isLoading}
        isError={slos.isError || budgets.isError}
        error={slos.error ?? budgets.error}
        data={joined.length ? joined : undefined}
        onRetry={() => { slos.refetch(); budgets.refetch(); }}
        empty={<EmptyState title="No cost SLOs declared" description="Define objectives during onboarding or in Settings." />}
      >
        {(list) => (
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            {list.map(({ slo, budget }) => (
              <SloCard key={slo.id} slo={slo} state={budget?.state ?? "unknown"} consumedRatio={budget?.consumed_ratio ?? 0} burnRate={budget?.burn_rate ?? 0} explanation={budget?.explanation} exhaustionDate={budget?.exhaustion_date} actual={budget?.actual} target={budget?.target ?? slo.target} />
            ))}
          </div>
        )}
      </QueryBoundary>
    </div>
  );
}

function SloCard({
  slo,
  state,
  consumedRatio,
  burnRate,
  explanation,
  exhaustionDate,
  actual,
  target,
}: {
  slo: CostSLO;
  state: BudgetState;
  consumedRatio: number;
  burnRate: number;
  explanation?: string;
  exhaustionDate?: string;
  actual?: Money;
  target?: Money;
}) {
  const tone = STATE_TONE[state];
  const violations = useSloViolations(slo.id);

  return (
    <Card className={cn(state === "breached" || state === "exhausted" ? "border-destructive/40" : state === "at_risk" || state === "watch" ? "border-warning/40" : undefined)}>
      <CardHeader className="flex-row items-start justify-between space-y-0">
        <div>
          <CardTitle className="text-sm">{slo.name}</CardTitle>
          <CardDescription>{slo.scope}{slo.scope_id ? ` · ${slo.scope_id}` : ""} · {slo.window.replace(/_/g, " ")}</CardDescription>
        </div>
        <Badge className={cn("capitalize", tone.badge)} variant="outline">{tone.label}</Badge>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-center justify-between text-sm">
          <span>
            Actual: <span className="font-medium">{actual ? <MoneyDisplay money={actual} size="sm" /> : "—"}</span>
          </span>
          <span className="text-muted-foreground">
            Target ({slo.direction.replace(/_/g, " ")}): <span className="font-medium text-foreground">{target ? <MoneyDisplay money={target} size="sm" muted /> : slo.target_ratio !== undefined ? `${(slo.target_ratio * 100).toFixed(1)}%` : "—"}</span>
          </span>
        </div>

        <div>
          <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
            <span>Error budget consumed</span>
            <span className="tabular-nums">{Math.round(consumedRatio * 100)}%</span>
          </div>
          <Progress value={Math.min(100, consumedRatio * 100)} indicatorClassName={tone.bar} />
        </div>

        <div className="flex items-center gap-4 text-xs text-muted-foreground">
          <span>Burn rate: <span className={cn("font-medium", burnRate > 1.5 ? "text-destructive" : burnRate > 1 ? "text-warning" : "text-foreground")}>{burnRate.toFixed(2)}x</span></span>
          {exhaustionDate && <span>Projected exhaustion: <span className="font-medium text-foreground">{formatDate(exhaustionDate)}</span></span>}
        </div>

        {explanation && <p className="rounded-md bg-secondary/40 p-2 text-xs text-muted-foreground">{explanation}</p>}

        <div className="flex flex-wrap gap-1">
          {slo.breach_actions.map((a) => (
            <Badge key={a} variant="secondary" className="text-[10px] capitalize">{a.replace(/_/g, " ")}</Badge>
          ))}
        </div>

        <div className="border-t border-border pt-2">
          <p className="mb-1 flex items-center gap-1 text-[11px] font-medium uppercase text-muted-foreground"><History className="h-3 w-3" /> Violation history</p>
          {violations.isLoading && <p className="text-xs text-muted-foreground">Loading…</p>}
          {violations.data && violations.data.length === 0 && <p className="text-xs text-muted-foreground">No violations in the last quarter.</p>}
          {violations.data && violations.data.length > 0 && (
            <ul className="space-y-1">
              {violations.data.map((v) => (
                <li key={v.id} className="flex items-center justify-between text-xs">
                  <span className="text-muted-foreground">{formatDateTime(v.started_at)} — {v.resolved_at ? `resolved ${formatDateTime(v.resolved_at)}` : "ongoing"}</span>
                  <span className="tabular-nums font-medium">{Math.round(v.peak_consumed_ratio * 100)}% peak</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function CountTile({ label, value, icon: Icon, tone }: { label: string; value: number; icon: React.ComponentType<{ className?: string }>; tone?: "success" | "warning" | "destructive" }) {
  return (
    <Card className="p-3.5">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">{label}</span>
        <Icon className={cn("h-3.5 w-3.5", tone === "success" && "text-success", tone === "warning" && "text-warning", tone === "destructive" && "text-destructive", !tone && "text-muted-foreground")} />
      </div>
      <div className="mt-1.5 text-2xl font-semibold tabular-nums tracking-tight">{value}</div>
    </Card>
  );
}
