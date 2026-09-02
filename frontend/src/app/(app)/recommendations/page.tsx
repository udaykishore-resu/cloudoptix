"use client";
import * as React from "react";
import { useSearchParams, useRouter } from "next/navigation";
import { CheckCircle2, Clock, Search, ShieldCheck, XCircle } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { StatTile } from "@/components/shared/stat-tile";
import { MoneyDisplay } from "@/components/shared/money-display";
import { RiskBadge } from "@/components/shared/risk-badge";
import { ConfidenceBadge } from "@/components/shared/confidence-badge";
import { BlastRadiusSummary } from "@/components/shared/blast-radius-summary";
import { ResourceIcon } from "@/components/shared/resource-icon";
import { QueryBoundary, EmptyState, LoadingRows } from "@/components/shared/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import {
  useRecommendations,
  useRecommendationSummary,
  useRecommendationExplanation,
  useDismissRecommendation,
  useSnoozeRecommendation,
  useApproveRecommendation,
  type RecommendationFilter,
} from "@/lib/api/recommendations";
import { usePolicyDecision } from "@/lib/api/governance";
import { cn } from "@/lib/utils";

const STATUS_TONE: Record<string, string> = {
  open: "bg-info/15 text-info",
  approved: "bg-primary/15 text-primary",
  scheduled: "bg-primary/15 text-primary",
  executing: "bg-warning/15 text-warning",
  executed: "bg-success/15 text-success",
  validated: "bg-success/15 text-success",
  failed: "bg-destructive/15 text-destructive",
  rolled_back: "bg-destructive/15 text-destructive",
  dismissed: "bg-muted text-muted-foreground",
  snoozed: "bg-muted text-muted-foreground",
  superseded: "bg-muted text-muted-foreground",
};

export default function RecommendationsPage() {
  return (
    <React.Suspense fallback={<LoadingRows rows={8} />}>
      <RecommendationsInner />
    </React.Suspense>
  );
}

function RecommendationsInner() {
  const router = useRouter();
  const params = useSearchParams();
  const [search, setSearch] = React.useState("");
  const [status, setStatus] = React.useState<string>("open");
  const [category, setCategory] = React.useState<string>("all");
  const [risk, setRisk] = React.useState<string>("all");
  const selectedId = params.get("id") ?? undefined;

  const filter: RecommendationFilter = {
    search: search || undefined,
    status: status !== "all" ? [status] : undefined,
    category: category !== "all" ? [category] : undefined,
    risk: risk !== "all" ? [risk] : undefined,
  };
  const list = useRecommendations(filter);
  const summary = useRecommendationSummary();

  const sorted = React.useMemo(() => [...(list.data ?? [])].sort((a, b) => b.priority_score - a.priority_score), [list.data]);

  const setSelectedId = (id: string | undefined) => {
    const q = new URLSearchParams(params.toString());
    if (id) q.set("id", id); else q.delete("id");
    router.push(`/recommendations${q.toString() ? `?${q}` : ""}`, { scroll: false });
  };

  return (
    <div>
      <PageHeader title="Recommendations" description="Ranked optimization opportunities with full evidence, confidence and blast-radius reasoning." />

      <QueryBoundary isLoading={summary.isLoading} isError={summary.isError} error={summary.error} data={summary.data} loading={<div className="grid grid-cols-2 gap-3 lg:grid-cols-4"><div className="h-24" /></div>}>
        {(s) => (
          <div className="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
            <StatTile label="Open" value={s.open} icon={Clock} />
            <StatTile label="Total monthly saving" value={<MoneyDisplay money={s.total_monthly_saving} size="xl" />} icon={CheckCircle2} tone="success" />
            <StatTile label="Auto-executable" value={s.auto_executable} sub="Policy-eligible without manual approval" icon={ShieldCheck} />
            <StatTile label="Awaiting approval" value={s.awaiting_approval} icon={Clock} tone={s.awaiting_approval > 0 ? "warning" : "default"} />
          </div>
        )}
      </QueryBoundary>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="relative w-64">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input placeholder="Search title or resource…" value={search} onChange={(e) => setSearch(e.target.value)} className="h-8 pl-8 text-xs" />
        </div>
        <Select value={status} onValueChange={setStatus}>
          <SelectTrigger className="h-8 w-40 text-xs"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            {["open", "approved", "scheduled", "executed", "validated", "failed", "dismissed", "snoozed"].map((s) => (
              <SelectItem key={s} value={s} className="capitalize">{s.replace(/_/g, " ")}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={category} onValueChange={setCategory}>
          <SelectTrigger className="h-8 w-40 text-xs"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All categories</SelectItem>
            {["rightsizing", "waste", "storage", "commitment", "network", "architecture", "scheduling", "licensing", "data_lifecycle", "observability_cost"].map((c) => (
              <SelectItem key={c} value={c} className="capitalize">{c.replace(/_/g, " ")}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={risk} onValueChange={setRisk}>
          <SelectTrigger className="h-8 w-36 text-xs"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All risk</SelectItem>
            {["LOW", "MEDIUM", "HIGH", "CRITICAL"].map((r) => (
              <SelectItem key={r} value={r} className="capitalize">{r.toLowerCase()}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {list.isLoading && <LoadingRows rows={8} />}
      {list.data && sorted.length === 0 && <EmptyState title="No recommendations match these filters" />}
      {list.data && sorted.length > 0 && (
        <ul className="space-y-1.5">
          {sorted.map((r) => (
            <li key={r.id}>
              <button
                onClick={() => setSelectedId(r.id)}
                className="focus-ring flex w-full items-center gap-3 rounded-lg border border-border bg-card px-3 py-2.5 text-left text-sm hover:border-border-strong hover:bg-secondary/30"
              >
                <ResourceIcon kind={r.finding.resource_kind} className="shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <p className="truncate font-medium">{r.title}</p>
                    <Badge variant="outline" className={cn("shrink-0 capitalize", STATUS_TONE[r.status])}>{r.status.replace(/_/g, " ")}</Badge>
                    {r.auto_executable && <Badge variant="secondary" className="shrink-0">auto-executable</Badge>}
                  </div>
                  <p className="truncate text-xs text-muted-foreground">{r.finding.resource_name} · {r.finding.environment} · {r.finding.category.replace(/_/g, " ")}</p>
                </div>
                <div className="flex shrink-0 items-center gap-3">
                  <ConfidenceBadge confidence={r.confidence} size="sm" />
                  <RiskBadge level={r.risk.level} />
                  <span className="w-9 text-right text-xs tabular-nums text-muted-foreground" title="Priority score">{r.priority_score.toFixed(0)}</span>
                  <MoneyDisplay money={r.estimated_monthly_saving} size="sm" className="w-20 text-right" />
                </div>
              </button>
            </li>
          ))}
        </ul>
      )}

      <RecommendationDetailSheet id={selectedId} onClose={() => setSelectedId(undefined)} />
    </div>
  );
}

function RecommendationDetailSheet({ id, onClose }: { id: string | undefined; onClose: () => void }) {
  const explanation = useRecommendationExplanation(id);
  const decision = usePolicyDecision(id);
  const dismiss = useDismissRecommendation();
  const snooze = useSnoozeRecommendation();
  const approve = useApproveRecommendation();
  const [dismissOpen, setDismissOpen] = React.useState(false);
  const [dismissReason, setDismissReason] = React.useState("");
  const [snoozeOpen, setSnoozeOpen] = React.useState(false);
  const [snoozeDays, setSnoozeDays] = React.useState(14);

  return (
    <Sheet open={!!id} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-xl">
        <QueryBoundary isLoading={explanation.isLoading} isError={explanation.isError} error={explanation.error} data={explanation.data} onRetry={() => explanation.refetch()}>
          {(e) => {
            const r = e.recommendation;
            return (
              <>
                <SheetHeader>
                  <SheetTitle>{r.title}</SheetTitle>
                  <SheetDescription>{r.finding.resource_name} · {r.finding.resource_kind} · {r.finding.environment}</SheetDescription>
                </SheetHeader>
                <div className="space-y-5 px-4 pb-8">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="outline" className={cn("capitalize", STATUS_TONE[r.status])}>{r.status.replace(/_/g, " ")}</Badge>
                    <ConfidenceBadge confidence={r.confidence} basis={r.confidence_basis} />
                    <RiskBadge level={r.risk.level} />
                    <Badge variant="secondary" className="capitalize">{r.reversibility} to reverse</Badge>
                    <Badge variant="secondary" className="capitalize">{r.complexity} complexity</Badge>
                  </div>

                  <div className="rounded-md border border-border p-3">
                    <div className="flex items-baseline justify-between">
                      <MoneyDisplay money={r.estimated_monthly_saving} size="lg" />
                      <span className="text-xs text-muted-foreground">/mo · <MoneyDisplay money={r.estimated_annual_saving} size="sm" muted />/yr</span>
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">{r.rationale}</p>
                  </div>

                  <Section title="Proposed change">
                    <div className="grid grid-cols-2 gap-3 text-xs">
                      <StateBox label="Current" state={r.current_state} />
                      <StateBox label="Proposed" state={r.proposed_state} />
                    </div>
                  </Section>

                  <Section title="Evidence">
                    <ul className="space-y-1.5">
                      {e.evidence.map((ev, i) => (
                        <li key={i} className="flex items-center justify-between rounded-md border border-border px-2 py-1.5 text-xs">
                          <span className="text-muted-foreground">{ev.label}</span>
                          <span className="font-medium tabular-nums">{ev.value}</span>
                        </li>
                      ))}
                    </ul>
                  </Section>

                  <Section title="Confidence inputs">
                    <ul className="space-y-1.5">
                      {e.confidence_inputs.map((c) => (
                        <li key={c.name} className="text-xs">
                          <div className="flex items-center justify-between">
                            <span className="text-muted-foreground">{c.explanation}</span>
                            <span className="font-medium tabular-nums">{Math.round(c.value * 100)}%</span>
                          </div>
                          <div className="mt-0.5 h-1 w-full rounded-full bg-secondary"><div className="h-1 rounded-full bg-primary" style={{ width: `${c.value * 100}%` }} /></div>
                        </li>
                      ))}
                    </ul>
                  </Section>

                  <Section title="Risk factors">
                    <ul className="space-y-1.5">
                      {e.risk_factors.map((f) => (
                        <li key={f.name} className="flex items-center justify-between text-xs">
                          <span className="text-muted-foreground">{f.explanation}</span>
                          <span className="font-medium tabular-nums">+{f.contribution}</span>
                        </li>
                      ))}
                    </ul>
                    {r.risk.mitigations?.length ? (
                      <ul className="mt-2 space-y-1 border-t border-border pt-2 text-xs text-muted-foreground">
                        {r.risk.mitigations.map((m, i) => <li key={i}>· {m}</li>)}
                      </ul>
                    ) : null}
                  </Section>

                  <Section title="Blast radius">
                    <BlastRadiusSummary blast={e.blast_radius} />
                  </Section>

                  {decision.data && (
                    <Section title="Policy decision">
                      <div className="space-y-1.5 text-xs">
                        <div className="flex items-center gap-2">
                          <Badge variant={decision.data.effect === "auto_execute" ? "success" : decision.data.effect === "prohibit" ? "destructive" : "warning"} className="capitalize">{decision.data.effect.replace(/_/g, " ")}</Badge>
                          <span className="text-muted-foreground">by rule <code className="font-mono">{decision.data.deciding_rule}</code></span>
                        </div>
                        <p className="text-muted-foreground">{decision.data.reason}</p>
                        <ul className="space-y-0.5 text-muted-foreground">
                          {decision.data.explanation.map((line, i) => <li key={i}>· {line}</li>)}
                        </ul>
                      </div>
                    </Section>
                  )}

                  {e.rollback_summary && (
                    <Section title="Rollback plan">
                      <p className="text-xs text-muted-foreground">{e.rollback_summary}</p>
                    </Section>
                  )}

                  {r.status === "open" && (
                    <div className="sticky bottom-0 -mx-4 flex items-center gap-2 border-t border-border bg-card px-4 py-3">
                      <Button size="sm" onClick={() => approve.mutate(r.id)} disabled={approve.isPending}>
                        <CheckCircle2 className="h-3.5 w-3.5" /> Approve
                      </Button>
                      <Button size="sm" variant="outline" onClick={() => setSnoozeOpen(true)}>
                        <Clock className="h-3.5 w-3.5" /> Snooze
                      </Button>
                      <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setDismissOpen(true)}>
                        <XCircle className="h-3.5 w-3.5" /> Dismiss
                      </Button>
                    </div>
                  )}
                </div>

                <Dialog open={dismissOpen} onOpenChange={setDismissOpen}>
                  <DialogContent>
                    <DialogHeader><DialogTitle>Dismiss recommendation</DialogTitle></DialogHeader>
                    <Textarea placeholder="Why are you dismissing this? (required — feeds rule calibration)" value={dismissReason} onChange={(e2) => setDismissReason(e2.target.value)} />
                    <DialogFooter>
                      <Button variant="outline" onClick={() => setDismissOpen(false)}>Cancel</Button>
                      <Button
                        variant="destructive"
                        disabled={!dismissReason.trim() || dismiss.isPending}
                        onClick={() => dismiss.mutate({ id: r.id, reason: dismissReason }, { onSuccess: () => { setDismissOpen(false); setDismissReason(""); } })}
                      >
                        Dismiss
                      </Button>
                    </DialogFooter>
                  </DialogContent>
                </Dialog>

                <Dialog open={snoozeOpen} onOpenChange={setSnoozeOpen}>
                  <DialogContent>
                    <DialogHeader><DialogTitle>Snooze recommendation</DialogTitle></DialogHeader>
                    <div className="flex items-center gap-2 text-sm">
                      <span>Re-surface in</span>
                      <Input type="number" className="w-20" value={snoozeDays} onChange={(e2) => setSnoozeDays(Number(e2.target.value))} min={1} />
                      <span>days</span>
                    </div>
                    <DialogFooter>
                      <Button variant="outline" onClick={() => setSnoozeOpen(false)}>Cancel</Button>
                      <Button
                        disabled={snooze.isPending}
                        onClick={() => {
                          const until = new Date(Date.now() + snoozeDays * 86400000).toISOString();
                          snooze.mutate({ id: r.id, until, reason: "Snoozed from recommendations list" }, { onSuccess: () => setSnoozeOpen(false) });
                        }}
                      >
                        Snooze
                      </Button>
                    </DialogFooter>
                  </DialogContent>
                </Dialog>
              </>
            );
          }}
        </QueryBoundary>
      </SheetContent>
    </Sheet>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <p className="mb-1.5 text-[11px] font-medium uppercase text-muted-foreground">{title}</p>
      {children}
    </div>
  );
}

function StateBox({ label, state }: { label: string; state: { instance_type?: string; volume_type?: string; size_gib?: number; count?: number; monthly_cost: { display: string } } }) {
  return (
    <div className="rounded-md border border-border p-2">
      <p className="mb-1 text-[10px] uppercase text-muted-foreground">{label}</p>
      {state.instance_type && <p>{state.instance_type}</p>}
      {state.volume_type && <p>{state.volume_type}{state.size_gib ? ` · ${state.size_gib} GiB` : ""}</p>}
      {state.count !== undefined && <p>count: {state.count}</p>}
      <p className="mt-1 font-medium tabular-nums">{state.monthly_cost.display}/mo</p>
    </div>
  );
}
