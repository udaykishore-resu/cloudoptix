"use client";
import * as React from "react";
import { RadarChart, PolarGrid, PolarAngleAxis, PolarRadiusAxis, Radar, ResponsiveContainer, Legend as RLegend } from "recharts";
import { FlaskConical, Sparkles, Wand2 } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { ConfidenceBadge } from "@/components/shared/confidence-badge";
import { WideViewportGate } from "@/components/shared/wide-viewport-gate";
import { LoadingBlock } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useGenerateCandidates, useCounterfactual } from "@/lib/api/simulate";
import { cn } from "@/lib/utils";
import type { Candidate, Scenario, ScenarioType } from "@/types/domain";

const SCOPES = ["checkout", "catalog", "identity", "fulfillment", "notifications"];

const SCENARIOS: { type: ScenarioType; label: string }[] = [
  { type: "traffic_change", label: "Traffic ×2" },
  { type: "platform_change", label: "Move to managed platform" },
  { type: "database_change", label: "Move to Aurora" },
  { type: "add_cache", label: "Add a cache layer" },
  { type: "remove_nat", label: "Remove NAT Gateway" },
  { type: "spot_adoption", label: "Adopt Spot for stateless compute" },
  { type: "region_change", label: "Add a second region" },
  { type: "commitment_purchase", label: "Purchase a Savings Plan" },
  { type: "storage_class_change", label: "Move cold storage to Glacier" },
  { type: "replica_change", label: "Remove a read replica" },
];

export default function SimulatorPage() {
  const [mode, setMode] = React.useState<"candidates" | "whatif">("candidates");
  const [scope, setScope] = React.useState("checkout");
  const [selectedCandidateIds, setSelectedCandidateIds] = React.useState<string[]>([]);
  const generate = useGenerateCandidates();

  React.useEffect(() => {
    if (generate.data) setSelectedCandidateIds(generate.data.candidates.map((c) => c.id));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [generate.data]);

  return (
    <div>
      <PageHeader
        title="Architecture simulator"
        description="Generate and compare re-architecture candidates, or ask a targeted what-if question."
        actions={
          <Tabs value={mode} onValueChange={(v) => setMode(v as "candidates" | "whatif")}>
            <TabsList>
              <TabsTrigger value="candidates"><FlaskConical className="mr-1 h-3.5 w-3.5" />Candidates</TabsTrigger>
              <TabsTrigger value="whatif"><Wand2 className="mr-1 h-3.5 w-3.5" />What-if</TabsTrigger>
            </TabsList>
          </Tabs>
        }
      />

      {mode === "candidates" ? (
        <div>
          <div className="mb-4 flex items-center gap-2">
            <Select value={scope} onValueChange={setScope}>
              <SelectTrigger className="h-9 w-56"><SelectValue /></SelectTrigger>
              <SelectContent>
                {SCOPES.map((s) => <SelectItem key={s} value={s} className="capitalize">{s}</SelectItem>)}
              </SelectContent>
            </Select>
            <Button onClick={() => generate.mutate(scope)} disabled={generate.isPending}>
              <Sparkles className="h-3.5 w-3.5" /> {generate.isPending ? "Generating candidates…" : "Generate candidates"}
            </Button>
          </div>

          {generate.isPending && <LoadingBlock height="h-96" />}

          {generate.data && (
            <WideViewportGate>
              <div className="space-y-4">
                <Card>
                  <CardHeader>
                    <CardTitle>Dimension comparison</CardTitle>
                    <CardDescription>Baseline: <MoneyDisplay money={generate.data.baseline_cost} size="sm" />/mo · scored 0–100 across 8 dimensions.</CardDescription>
                  </CardHeader>
                  <CardContent>
                    <div className="h-96 w-full">
                      <ResponsiveContainer width="100%" height="100%">
                        <RadarChart data={buildRadarData(generate.data.candidates)}>
                          <PolarGrid stroke="hsl(var(--border))" />
                          <PolarAngleAxis dataKey="dimension" tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }} />
                          <PolarRadiusAxis angle={30} domain={[0, 100]} tick={{ fontSize: 9 }} />
                          {generate.data.candidates.map((c, i) => (
                            selectedCandidateIds.includes(c.id) && (
                              <Radar key={c.id} name={c.name} dataKey={c.id} stroke={CHART_COLORS[i % CHART_COLORS.length]} fill={CHART_COLORS[i % CHART_COLORS.length]} fillOpacity={0.12} />
                            )
                          ))}
                          <RLegend wrapperStyle={{ fontSize: 11 }} />
                        </RadarChart>
                      </ResponsiveContainer>
                    </div>
                  </CardContent>
                </Card>

                <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
                  {generate.data.candidates.map((c, i) => (
                    <CandidateCard
                      key={c.id}
                      candidate={c}
                      color={CHART_COLORS[i % CHART_COLORS.length]}
                      selected={selectedCandidateIds.includes(c.id)}
                      onToggle={() => setSelectedCandidateIds((prev) => (prev.includes(c.id) ? prev.filter((id) => id !== c.id) : [...prev, c.id]))}
                    />
                  ))}
                </div>
              </div>
            </WideViewportGate>
          )}

          {!generate.data && !generate.isPending && (
            <div className="flex min-h-[300px] flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border text-center">
              <FlaskConical className="h-8 w-8 text-muted-foreground" />
              <p className="text-sm font-medium">Pick a scope and generate candidates</p>
              <p className="max-w-sm text-xs text-muted-foreground">Each candidate is scored across cost, performance, reliability, scalability, security, operational complexity, migration complexity and risk.</p>
            </div>
          )}
        </div>
      ) : (
        <WhatIfPanel />
      )}
    </div>
  );
}

const CHART_COLORS = ["hsl(var(--chart-1))", "hsl(var(--chart-3))", "hsl(var(--chart-4))", "hsl(var(--chart-5))"];

function buildRadarData(candidates: Candidate[]) {
  const dims = candidates[0]?.scores.map((s) => s.dimension) ?? [];
  return dims.map((d) => {
    const row: Record<string, string | number> = { dimension: d.replace(/_/g, " ") };
    candidates.forEach((c) => {
      const s = c.scores.find((sc) => sc.dimension === d);
      row[c.id] = s?.score ?? 0;
    });
    return row;
  });
}

function CandidateCard({ candidate: c, color, selected, onToggle }: { candidate: Candidate; color: string; selected: boolean; onToggle: () => void }) {
  return (
    <Card className={cn("cursor-pointer transition-opacity", !selected && "opacity-50")} onClick={onToggle}>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <span className="h-2.5 w-2.5 rounded-full" style={{ background: color }} />
            {c.name}
          </CardTitle>
          {c.recommended && <Badge>Recommended</Badge>}
        </div>
        <CardDescription>{c.summary}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-baseline justify-between">
          <MoneyDisplay money={c.projected_monthly_cost} size="lg" />
          <span className={cn("text-xs font-medium tabular-nums", c.savings_pct > 0 ? "text-success" : "text-destructive")}>
            {c.savings_pct > 0 ? "−" : "+"}{Math.abs(c.savings_pct).toFixed(0)}% vs current
          </span>
        </div>
        <ConfidenceBadge confidence={c.confidence} size="sm" />
        <div>
          <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Key changes</p>
          <ul className="space-y-1 text-xs">
            {c.changes.slice(0, 4).map((ch, i) => (
              <li key={i} className="flex items-start justify-between gap-2">
                <span className="text-muted-foreground">{ch.rationale}</span>
                <MoneyDisplay money={ch.monthly_delta} size="sm" signed className="shrink-0" />
              </li>
            ))}
          </ul>
        </div>
        {c.risks?.length ? (
          <div>
            <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Risks</p>
            <ul className="space-y-0.5 text-xs text-muted-foreground">{c.risks.map((r, i) => <li key={i}>· {r}</li>)}</ul>
          </div>
        ) : null}
        {c.blockers?.length ? (
          <div>
            <p className="mb-1 text-[11px] font-medium uppercase text-destructive">Blockers</p>
            <ul className="space-y-0.5 text-xs text-destructive/90">{c.blockers.map((r, i) => <li key={i}>· {r}</li>)}</ul>
          </div>
        ) : null}
        {c.migration_steps?.length ? (
          <div>
            <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Migration outline</p>
            <ol className="list-decimal space-y-0.5 pl-4 text-xs text-muted-foreground">{c.migration_steps.map((s, i) => <li key={i}>{s}</li>)}</ol>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function WhatIfPanel() {
  const [scenarioType, setScenarioType] = React.useState<ScenarioType>("traffic_change");
  const [multiplier, setMultiplier] = React.useState(2);
  const counterfactual = useCounterfactual();

  const run = () => {
    const scenario: Scenario = { type: scenarioType, parameters: scenarioType === "traffic_change" ? { multiplier } : {} };
    counterfactual.mutate(scenario);
  };

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <Card className="lg:col-span-1">
        <CardHeader>
          <CardTitle>Ask a what-if question</CardTitle>
          <CardDescription>Single-variable scenario, evaluated against the checkout application&rsquo;s current footprint.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <Select value={scenarioType} onValueChange={(v) => setScenarioType(v as ScenarioType)}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              {SCENARIOS.map((s) => <SelectItem key={s.type} value={s.type}>{s.label}</SelectItem>)}
            </SelectContent>
          </Select>
          {scenarioType === "traffic_change" && (
            <div className="flex items-center gap-2 text-sm">
              <span>Multiplier</span>
              <Input type="number" step={0.1} min={0.1} className="w-24" value={multiplier} onChange={(e) => setMultiplier(Number(e.target.value))} />
              <span>×</span>
            </div>
          )}
          <Button className="w-full" onClick={run} disabled={counterfactual.isPending}>
            {counterfactual.isPending ? "Evaluating…" : "Run scenario"}
          </Button>
        </CardContent>
      </Card>

      <div className="lg:col-span-2">
        {counterfactual.isPending && <LoadingBlock height="h-80" />}
        {!counterfactual.data && !counterfactual.isPending && (
          <div className="flex h-80 flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border text-center">
            <Wand2 className="h-8 w-8 text-muted-foreground" />
            <p className="text-sm font-medium">Pick a scenario and run it</p>
          </div>
        )}
        {counterfactual.data && (
          <Card>
            <CardHeader>
              <CardTitle>{counterfactual.data.question}</CardTitle>
              <CardDescription>{counterfactual.data.narrative}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="grid grid-cols-2 gap-3">
                <StateCol label={counterfactual.data.current_state.label} state={counterfactual.data.current_state} />
                <StateCol label={counterfactual.data.proposed_state.label} state={counterfactual.data.proposed_state} highlight />
              </div>
              <div className="flex items-center justify-between rounded-md border border-border p-3">
                <div>
                  <p className="text-[11px] uppercase text-muted-foreground">Monthly delta</p>
                  <MoneyDisplay money={counterfactual.data.cost_delta} size="lg" signed />
                </div>
                <div className="text-right">
                  <p className="text-[11px] uppercase text-muted-foreground">Annual delta</p>
                  <MoneyDisplay money={counterfactual.data.annual_cost_delta} size="lg" signed />
                </div>
                <Badge variant={counterfactual.data.risk === "LOW" ? "success" : counterfactual.data.risk === "MEDIUM" ? "warning" : "destructive"} className="capitalize">{counterfactual.data.risk.toLowerCase()} risk</Badge>
                <ConfidenceBadge confidence={counterfactual.data.confidence} />
              </div>
              {counterfactual.data.caveats?.length ? (
                <div>
                  <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Caveats</p>
                  <ul className="space-y-0.5 text-xs text-muted-foreground">{counterfactual.data.caveats.map((c, i) => <li key={i}>· {c}</li>)}</ul>
                </div>
              ) : null}
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}

function StateCol({ label, state, highlight }: { label: string; state: { monthly_cost: { display: string }; p95_latency_ms?: number; availability?: number; notes?: string[] }; highlight?: boolean }) {
  return (
    <div className={cn("rounded-md border p-3", highlight ? "border-primary/40 bg-primary/5" : "border-border")}>
      <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">{label}</p>
      <p className="text-lg font-semibold tabular-nums">{state.monthly_cost.display}/mo</p>
      {state.p95_latency_ms !== undefined && <p className="text-xs text-muted-foreground">p95 latency: {state.p95_latency_ms.toFixed(0)} ms</p>}
      {state.availability !== undefined && <p className="text-xs text-muted-foreground">Availability: {state.availability.toFixed(2)}%</p>}
      {state.notes?.map((n, i) => <p key={i} className="mt-1 text-xs text-muted-foreground">· {n}</p>)}
    </div>
  );
}
