"use client";
import * as React from "react";
import { AlertTriangle, Clipboard, FileCode2, HelpCircle } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { ConfidenceBadge } from "@/components/shared/confidence-badge";
import { SeverityBadge } from "@/components/shared/risk-badge";
import { LoadingBlock } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useCompile, useRunRegression, buildPrComment } from "@/lib/api/simulate";
import { cn } from "@/lib/utils";
import type { SourceKind } from "@/types/domain";

const SOURCES: { value: SourceKind; label: string }[] = [
  { value: "terraform_plan", label: "Terraform plan (JSON)" },
  { value: "terraform_hcl", label: "Terraform HCL" },
  { value: "cloudformation", label: "CloudFormation template" },
  { value: "kubernetes_manifest", label: "Kubernetes manifest" },
  { value: "helm_release", label: "Helm release" },
];

const SAMPLE_PLAN = `resource "aws_instance" "api" {
  count         = 3
  ami           = "ami-0abcd1234"
  instance_type = "m5.xlarge"
}

resource "aws_db_instance" "primary" {
  engine         = "postgres"
  instance_class = "db.r5.xlarge"
  multi_az       = true
}

resource "aws_nat_gateway" "egress" {
  count = 2
}`;

export default function CompilerPage() {
  const [source, setSource] = React.useState<SourceKind>("terraform_plan");
  const [label, setLabel] = React.useState("checkout-service#412");
  const [text, setText] = React.useState(SAMPLE_PLAN);
  const compile = useCompile();
  const regression = useRunRegression();

  const run = () => {
    compile.mutate(
      { label, source },
      {
        onSuccess: (result) => {
          if (result) regression.mutate({ compilation: result, suiteName: "default" });
        },
      }
    );
  };

  const prComment = compile.data && regression.data ? buildPrComment(regression.data, compile.data) : undefined;

  return (
    <div>
      <PageHeader title="Cost compiler" description="Price infrastructure-as-code changes before merge — Terraform, CloudFormation or Kubernetes." />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-1">
          <CardHeader>
            <CardTitle className="flex items-center gap-1.5"><FileCode2 className="h-4 w-4" /> Input</CardTitle>
            <CardDescription>Paste a plan, template or manifest to price.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <Select value={source} onValueChange={(v) => setSource(v as SourceKind)}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                {SOURCES.map((s) => <SelectItem key={s.value} value={s.value}>{s.label}</SelectItem>)}
              </SelectContent>
            </Select>
            <input
              className="focus-ring h-9 w-full rounded-md border border-input bg-surface px-3 text-sm shadow-sm"
              placeholder="Label (e.g. PR title)"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
            />
            <Textarea value={text} onChange={(e) => setText(e.target.value)} rows={16} className="font-mono text-xs" />
            <Button className="w-full" onClick={run} disabled={compile.isPending || regression.isPending}>
              {compile.isPending ? "Pricing changes…" : regression.isPending ? "Running regression checks…" : "Compile & price"}
            </Button>
          </CardContent>
        </Card>

        <div className="space-y-4 xl:col-span-2">
          {compile.isPending && <LoadingBlock height="h-96" />}
          {!compile.data && !compile.isPending && (
            <div className="flex h-96 flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border text-center">
              <FileCode2 className="h-8 w-8 text-muted-foreground" />
              <p className="text-sm font-medium">Compile a plan to see priced changes</p>
            </div>
          )}
          {compile.data && (
            <>
              <Card>
                <CardHeader>
                  <CardTitle>Summary</CardTitle>
                  <CardDescription>{compile.data.created_count} created · {compile.data.updated_count} updated · {compile.data.deleted_count} deleted · {compile.data.unpriced_count} unpriced</CardDescription>
                </CardHeader>
                <CardContent>
                  <div className="grid grid-cols-3 gap-3">
                    <Stat label="Baseline" value={<MoneyDisplay money={compile.data.baseline_monthly} size="lg" />} />
                    <Stat label="Projected" value={<MoneyDisplay money={compile.data.projected_monthly} size="lg" />} />
                    <Stat label="Monthly delta" value={<MoneyDisplay money={compile.data.monthly_delta} size="lg" signed />} sub={<MoneyDisplay money={compile.data.annual_delta} size="sm" signed suffix="/yr" />} />
                  </div>
                  <div className="mt-3 flex items-center gap-3 text-xs text-muted-foreground">
                    <span>Coverage: {(compile.data.coverage * 100).toFixed(0)}%</span>
                    <ConfidenceBadge confidence={compile.data.confidence} size="sm" />
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader><CardTitle>Priced changes</CardTitle></CardHeader>
                <CardContent className="space-y-2">
                  {compile.data.changes.map((c) => (
                    <div key={c.address} className="rounded-md border border-border p-2.5">
                      <div className="flex items-center justify-between gap-2">
                        <div className="min-w-0">
                          <div className="flex items-center gap-1.5">
                            <Badge variant={c.action === "create" ? "success" : c.action === "delete" ? "destructive" : "secondary"} className="capitalize">{c.action}</Badge>
                            <span className="truncate font-mono text-xs">{c.address}</span>
                          </div>
                          <p className="mt-0.5 text-[11px] text-muted-foreground">{c.resource_type}{c.region ? ` · ${c.region}` : ""}</p>
                        </div>
                        <div className="shrink-0 text-right">
                          {c.unpriced ? (
                            <span className="flex items-center gap-1 text-xs text-warning"><HelpCircle className="h-3 w-3" /> Unpriced</span>
                          ) : (
                            <>
                              <MoneyDisplay money={c.monthly_delta} size="sm" signed />
                              {c.usage_dependent && <p className="text-[10px] text-muted-foreground">usage-dependent</p>}
                            </>
                          )}
                        </div>
                      </div>
                      {c.unpriced && c.unpriced_reason && <p className="mt-1 text-[11px] text-muted-foreground">{c.unpriced_reason}</p>}
                      {c.price_components?.length ? (
                        <ul className="mt-2 space-y-0.5 border-t border-border pt-1.5 text-[11px] text-muted-foreground">
                          {c.price_components.map((pc, i) => (
                            <li key={i} className="flex justify-between"><span>{pc.name} ({pc.quantity} {pc.unit})</span><span className="tabular-nums">{pc.monthly.display}</span></li>
                          ))}
                        </ul>
                      ) : null}
                      {c.warnings?.length ? c.warnings.map((w, i) => <p key={i} className="mt-1 flex items-center gap-1 text-[11px] text-warning"><AlertTriangle className="h-3 w-3" />{w}</p>) : null}
                    </div>
                  ))}
                </CardContent>
              </Card>

              {(compile.data.risks?.length || compile.data.opportunities?.length) ? (
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                  {compile.data.risks?.length ? (
                    <Card>
                      <CardHeader><CardTitle className="text-sm">Cost risks</CardTitle></CardHeader>
                      <CardContent className="space-y-2">
                        {compile.data.risks.map((r, i) => (
                          <div key={i} className="rounded-md border border-border p-2 text-xs">
                            <div className="flex items-center justify-between"><span className="font-medium">{r.summary}</span><SeverityBadge level={r.severity} /></div>
                            {r.detail && <p className="mt-0.5 text-muted-foreground">{r.detail}</p>}
                            {r.remediation && <p className="mt-0.5 text-muted-foreground">Fix: {r.remediation}</p>}
                          </div>
                        ))}
                      </CardContent>
                    </Card>
                  ) : null}
                  {compile.data.opportunities?.length ? (
                    <Card>
                      <CardHeader><CardTitle className="text-sm">Opportunities</CardTitle></CardHeader>
                      <CardContent className="space-y-2">
                        {compile.data.opportunities.map((o, i) => (
                          <div key={i} className="rounded-md border border-border p-2 text-xs">
                            <div className="flex items-center justify-between"><span className="font-medium">{o.summary}</span><MoneyDisplay money={o.monthly_saving} size="sm" /></div>
                            <p className="mt-0.5 text-muted-foreground">{o.change}</p>
                          </div>
                        ))}
                      </CardContent>
                    </Card>
                  ) : null}
                </div>
              ) : null}

              {prComment && (
                <Card>
                  <CardHeader className="flex-row items-center justify-between space-y-0">
                    <div>
                      <CardTitle>Rendered PR comment</CardTitle>
                      <CardDescription>What gets posted back to the pull request, verdict: <span className={cn("font-medium", regression.data!.verdict === "PASS" ? "text-success" : regression.data!.verdict === "WARNING" ? "text-warning" : "text-destructive")}>{regression.data!.verdict}</span></CardDescription>
                    </div>
                    <Button size="sm" variant="outline" onClick={() => navigator.clipboard?.writeText(prComment)}>
                      <Clipboard className="h-3.5 w-3.5" /> Copy
                    </Button>
                  </CardHeader>
                  <CardContent>
                    <pre className="max-h-96 overflow-auto whitespace-pre-wrap rounded-md bg-surface-sunken p-3 font-mono text-xs">{prComment}</pre>
                  </CardContent>
                </Card>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function Stat({ label, value, sub }: { label: string; value: React.ReactNode; sub?: React.ReactNode }) {
  return (
    <div>
      <p className="text-[11px] uppercase text-muted-foreground">{label}</p>
      {value}
      {sub && <p className="mt-0.5">{sub}</p>}
    </div>
  );
}
