"use client";
import * as React from "react";
import yaml from "js-yaml";
import { AlertTriangle, CheckCircle2, History, PlayCircle, Save } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { YamlBlock } from "@/components/shared/yaml-block";
import { QueryBoundary, LoadingBlock } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { usePolicy, usePolicyVersions, useValidatePolicy, useSavePolicy, usePolicySimulation } from "@/lib/api/governance";
import { cn, formatDateTime } from "@/lib/utils";
import type { Policy } from "@/types/domain";

function toYamlText(p: Policy): string {
  const plain = {
    name: p.name,
    description: p.description,
    default_effect: p.default_effect,
    rules: p.rules.map((r) => ({ id: r.id, description: r.description, effect: r.effect, match: r.match, approvers: r.approvers, min_approvals: r.min_approvals, reason: r.reason })),
  };
  return yaml.dump(plain, { noRefs: true, lineWidth: 100 });
}

export default function PoliciesPage() {
  const [tab, setTab] = React.useState<"editor" | "versions" | "simulate">("editor");
  const policy = usePolicy();
  const versions = usePolicyVersions();
  const validate = useValidatePolicy();
  const save = useSavePolicy();
  const [text, setText] = React.useState("");
  const [issues, setIssues] = React.useState<{ path?: string; message?: string; severity?: string }[]>([]);
  const [saved, setSaved] = React.useState(false);

  React.useEffect(() => {
    if (policy.data && !text) setText(toYamlText(policy.data));
  }, [policy.data, text]);

  const runValidate = () => {
    if (!policy.data) return;
    validate.mutate(policy.data, {
      onSuccess: (res) => setIssues(res.issues ?? []),
    });
  };

  return (
    <div>
      <PageHeader
        title="Policies"
        description="Governance rules that decide auto-execute / require-approval / prohibit for every recommendation."
        actions={
          <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)}>
            <TabsList>
              <TabsTrigger value="editor">Editor</TabsTrigger>
              <TabsTrigger value="versions"><History className="mr-1 h-3.5 w-3.5" />History</TabsTrigger>
              <TabsTrigger value="simulate"><PlayCircle className="mr-1 h-3.5 w-3.5" />Simulate</TabsTrigger>
            </TabsList>
          </Tabs>
        }
      />

      {tab === "editor" && (
        <QueryBoundary isLoading={policy.isLoading} isError={policy.isError} error={policy.error} data={policy.data} onRetry={() => policy.refetch()}>
          {(p) => (
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
              <Card>
                <CardHeader className="flex-row items-center justify-between space-y-0">
                  <div>
                    <CardTitle>{p.name} <Badge variant="secondary" className="ml-1">v{p.version}</Badge></CardTitle>
                    <CardDescription>Default effect: <span className="font-medium capitalize">{p.default_effect.replace(/_/g, " ")}</span> · {p.rules.length} rules</CardDescription>
                  </div>
                  <div className="flex gap-2">
                    <Button size="sm" variant="outline" onClick={runValidate} disabled={validate.isPending}>Validate</Button>
                    <Button
                      size="sm"
                      onClick={() => save.mutate(p, { onSuccess: () => { setSaved(true); setTimeout(() => setSaved(false), 2000); } })}
                      disabled={save.isPending}
                    >
                      <Save className="h-3.5 w-3.5" /> {saved ? "Saved" : "Save version"}
                    </Button>
                  </div>
                </CardHeader>
                <CardContent>
                  <Textarea value={text} onChange={(e) => setText(e.target.value)} rows={22} className="font-mono text-xs" />
                  {issues.length > 0 && (
                    <ul className="mt-2 space-y-1">
                      {issues.map((iss, i) => (
                        <li key={i} className="flex items-center gap-1.5 text-xs text-warning"><AlertTriangle className="h-3 w-3" />{iss.path ? `${iss.path}: ` : ""}{iss.message}</li>
                      ))}
                    </ul>
                  )}
                  {issues.length === 0 && validate.data && (
                    <p className="mt-2 flex items-center gap-1.5 text-xs text-success"><CheckCircle2 className="h-3 w-3" /> No validation issues</p>
                  )}
                </CardContent>
              </Card>

              <Card>
                <CardHeader><CardTitle>Rules</CardTitle><CardDescription>Evaluated top to bottom; first match with a deny bias wins.</CardDescription></CardHeader>
                <CardContent className="space-y-2">
                  {p.rules.map((r) => (
                    <div key={r.id} className="rounded-md border border-border p-2.5 text-xs">
                      <div className="flex items-center justify-between">
                        <span className="font-mono font-medium">{r.id}</span>
                        <Badge variant={r.effect === "auto_execute" ? "success" : r.effect === "prohibit" ? "destructive" : "warning"} className="capitalize">{r.effect.replace(/_/g, " ")}</Badge>
                      </div>
                      <p className="mt-0.5 text-muted-foreground">{r.description}</p>
                      <div className="mt-1.5 flex flex-wrap gap-1">
                        {r.match.categories?.map((c) => <Badge key={c} variant="secondary" className="text-[10px]">{c}</Badge>)}
                        {r.match.environments?.map((e) => <Badge key={e} variant="secondary" className="text-[10px]">{e}</Badge>)}
                        {r.match.max_risk_level && <Badge variant="secondary" className="text-[10px]">max risk: {r.match.max_risk_level}</Badge>}
                        {r.match.min_confidence !== undefined && <Badge variant="secondary" className="text-[10px]">min confidence: {Math.round(r.match.min_confidence * 100)}%</Badge>}
                      </div>
                    </div>
                  ))}
                </CardContent>
              </Card>
            </div>
          )}
        </QueryBoundary>
      )}

      {tab === "versions" && (
        <QueryBoundary isLoading={versions.isLoading} isError={versions.isError} error={versions.error} data={versions.data} onRetry={() => versions.refetch()}>
          {(list) => (
            <div className="space-y-2">
              {[...list].sort((a, b) => b.version - a.version).map((v) => (
                <Card key={v.id}>
                  <CardHeader className="flex-row items-center justify-between space-y-0">
                    <div>
                      <CardTitle className="text-sm">v{v.version} — {v.name}</CardTitle>
                      <CardDescription>{v.rules.length} rules · created by {v.created_by} · {formatDateTime(v.created_at)}</CardDescription>
                    </div>
                    <Badge variant={v.enabled ? "success" : "muted"}>{v.enabled ? "Active" : "Superseded"}</Badge>
                  </CardHeader>
                  <CardContent>
                    <YamlBlock yaml={toYamlText(v)} className="max-h-56" />
                  </CardContent>
                </Card>
              ))}
            </div>
          )}
        </QueryBoundary>
      )}

      {tab === "simulate" && <SimulateTab />}
    </div>
  );
}

function SimulateTab() {
  const [enabled, setEnabled] = React.useState(false);
  const sim = usePolicySimulation(enabled);

  return (
    <div>
      {!enabled && (
        <div className="flex min-h-[240px] flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-border text-center">
          <PlayCircle className="h-8 w-8 text-muted-foreground" />
          <p className="text-sm font-medium">Simulate the draft policy against current recommendations</p>
          <Button onClick={() => setEnabled(true)}>Run simulation</Button>
        </div>
      )}
      {enabled && sim.isLoading && <LoadingBlock height="h-64" />}
      {enabled && sim.data && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
            <MiniStat label="Evaluated" value={sim.data.evaluated} />
            <MiniStat label="Auto-execute" value={sim.data.auto_execute} tone="success" />
            <MiniStat label="Require approval" value={sim.data.require_approval} tone="warning" />
            <MiniStat label="Prohibited" value={sim.data.prohibited} tone="destructive" />
            <MiniStat label="Advisory" value={sim.data.advisory} />
          </div>
          <Card>
            <CardHeader>
              <CardTitle>What would change</CardTitle>
              <CardDescription>Recommendations whose governance outcome would differ under this draft. Auto-executable saving impacted: <MoneyDisplay money={sim.data.auto_executable_saving} size="sm" /></CardDescription>
            </CardHeader>
            <CardContent>
              {!sim.data.changes?.length && <p className="text-sm text-muted-foreground">No changes from the currently active policy.</p>}
              <ul className="space-y-1.5">
                {sim.data.changes?.map((c) => (
                  <li key={c.recommendation_id} className="flex items-center justify-between rounded-md border border-border px-2.5 py-1.5 text-xs">
                    <span className="truncate">{c.title}</span>
                    <span className="flex shrink-0 items-center gap-2">
                      <Badge variant="secondary" className="capitalize">{c.from.replace(/_/g, " ")}</Badge>
                      <span className="text-muted-foreground">→</span>
                      <Badge variant="outline" className="capitalize">{c.to.replace(/_/g, " ")}</Badge>
                      <MoneyDisplay money={c.monthly_saving} size="sm" />
                    </span>
                  </li>
                ))}
              </ul>
              {sim.data.warnings?.length ? (
                <ul className="mt-2 space-y-1 border-t border-border pt-2">
                  {sim.data.warnings.map((w, i) => <li key={i} className="flex items-center gap-1.5 text-xs text-warning"><AlertTriangle className="h-3 w-3" />{w}</li>)}
                </ul>
              ) : null}
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}

function MiniStat({ label, value, tone }: { label: string; value: number; tone?: "success" | "warning" | "destructive" }) {
  return (
    <Card className="p-3">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className={cn("text-xl font-semibold tabular-nums", tone === "success" && "text-success", tone === "warning" && "text-warning", tone === "destructive" && "text-destructive")}>{value}</p>
    </Card>
  );
}
