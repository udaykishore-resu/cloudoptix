"use client";
import * as React from "react";
import { CheckCircle2, ShieldCheck, XCircle } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { QueryBoundary, EmptyState } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { useRegressionSuites, useRegressionHistory } from "@/lib/api/simulate";
import { cn, formatDateTime } from "@/lib/utils";
import type { RegressionVerdict } from "@/types/domain";

const VERDICT_STYLE: Record<RegressionVerdict, { icon: typeof CheckCircle2; className: string }> = {
  PASS: { icon: CheckCircle2, className: "text-success bg-success/15" },
  WARNING: { icon: ShieldCheck, className: "text-warning bg-warning/15" },
  FAIL: { icon: XCircle, className: "text-destructive bg-destructive/15" },
};

export default function RegressionPage() {
  const suites = useRegressionSuites();
  const history = useRegressionHistory();

  return (
    <div>
      <PageHeader title="Cost regression" description="CI-style cost gates: suites, checks, and their pass/warning/fail history." />

      <QueryBoundary isLoading={suites.isLoading} isError={suites.isError} error={suites.error} data={suites.data} onRetry={() => suites.refetch()}>
        {(list) => (
          <div className="mb-4 grid grid-cols-1 gap-4 lg:grid-cols-2">
            {list.map((s) => (
              <Card key={s.id}>
                <CardHeader className="flex-row items-center justify-between space-y-0">
                  <div>
                    <CardTitle className="text-sm">{s.name}</CardTitle>
                    <CardDescription>v{s.version} · {s.checks.length} checks</CardDescription>
                  </div>
                  <Badge variant={s.enabled ? "success" : "muted"}>{s.enabled ? "Enabled" : "Disabled"}</Badge>
                </CardHeader>
                <CardContent>
                  <ul className="space-y-1.5">
                    {s.checks.map((c) => (
                      <li key={c.name} className="flex items-center justify-between text-xs">
                        <span className="text-muted-foreground">{c.name}</span>
                        <Badge variant="outline" className={cn("capitalize", c.on_violation === "FAIL" ? "text-destructive" : "text-warning")}>on violation: {c.on_violation.toLowerCase()}</Badge>
                      </li>
                    ))}
                  </ul>
                </CardContent>
              </Card>
            ))}
          </div>
        )}
      </QueryBoundary>

      <Card>
        <CardHeader>
          <CardTitle>Recent reports</CardTitle>
          <CardDescription>Regression checks evaluated against recent compiler runs.</CardDescription>
        </CardHeader>
        <CardContent>
          <QueryBoundary isLoading={history.isLoading} isError={history.isError} error={history.error} data={history.data} onRetry={() => history.refetch()} isEmpty={(d) => d.length === 0} empty={<EmptyState title="No regression reports yet" />}>
            {(entries) => (
              <ul className="space-y-2">
                {entries.map(({ report, label }) => {
                  const style = VERDICT_STYLE[report.verdict];
                  const Icon = style.icon;
                  return (
                    <li key={report.id} className="rounded-md border border-border p-3">
                      <div className="flex items-center justify-between gap-3">
                        <div className="flex min-w-0 items-center gap-2">
                          <span className={cn("flex h-6 w-6 shrink-0 items-center justify-center rounded-full", style.className)}>
                            <Icon className="h-3.5 w-3.5" />
                          </span>
                          <div className="min-w-0">
                            <p className="truncate text-sm font-medium">{label}</p>
                            <p className="text-[11px] text-muted-foreground">{report.suite_name} suite · {formatDateTime(report.evaluated_at)}</p>
                          </div>
                        </div>
                        <div className="flex shrink-0 items-center gap-3">
                          <MoneyDisplay money={report.monthly_delta} size="sm" signed />
                          <Badge variant={report.verdict === "PASS" ? "success" : report.verdict === "WARNING" ? "warning" : "destructive"}>{report.verdict}</Badge>
                        </div>
                      </div>
                      <p className="mt-1.5 text-xs text-muted-foreground">{report.summary}</p>
                      {report.results.filter((r) => r.verdict !== "PASS").length > 0 && (
                        <ul className="mt-1.5 space-y-1 border-t border-border pt-1.5">
                          {report.results.filter((r) => r.verdict !== "PASS").map((r) => (
                            <li key={r.name} className="flex items-center justify-between text-[11px]">
                              <span className="text-muted-foreground">{r.name}</span>
                              <span className={cn("font-medium", r.verdict === "FAIL" ? "text-destructive" : "text-warning")}>{r.actual}</span>
                            </li>
                          ))}
                        </ul>
                      )}
                      {report.required_action && <p className="mt-1.5 text-[11px] font-medium text-destructive">Required: {report.required_action}</p>}
                    </li>
                  );
                })}
              </ul>
            )}
          </QueryBoundary>
        </CardContent>
      </Card>
    </div>
  );
}
