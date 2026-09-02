"use client";
import * as React from "react";
import { CheckCircle2, Circle, Clock, Loader2, PlayCircle, RotateCcw, XCircle } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { QueryBoundary, EmptyState, LoadingRows } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  useExecutionPlans,
  useExecutionPlan,
  usePlanValidation,
  useAutonomousHistory,
  useExecutePlan,
  useCancelPlan,
  useRollbackPlan,
} from "@/lib/api/automation";
import { cn, formatDateTime } from "@/lib/utils";
import type { PlanState, Step, StepState } from "@/types/domain";

const STATE_TONE: Record<PlanState, string> = {
  draft: "bg-muted text-muted-foreground",
  awaiting_approval: "bg-warning/15 text-warning",
  approved: "bg-info/15 text-info",
  scheduled: "bg-info/15 text-info",
  preflight: "bg-info/15 text-info",
  executing: "bg-warning/15 text-warning",
  executed: "bg-success/15 text-success",
  validating: "bg-info/15 text-info",
  validated: "bg-success/15 text-success",
  failed: "bg-destructive/15 text-destructive",
  rolling_back: "bg-warning/15 text-warning",
  rolled_back: "bg-destructive/15 text-destructive",
  rollback_failed: "bg-destructive/15 text-destructive",
  cancelled: "bg-muted text-muted-foreground",
};

const STEP_ICON: Record<StepState, typeof Circle> = {
  pending: Circle,
  running: Loader2,
  succeeded: CheckCircle2,
  failed: XCircle,
  skipped: Circle,
  rolled_back: RotateCcw,
};

export default function AutomationPage() {
  const [tab, setTab] = React.useState<"plans" | "history">("plans");
  const [selectedId, setSelectedId] = React.useState<string | undefined>();
  const plans = useExecutionPlans();

  React.useEffect(() => {
    if (!selectedId && plans.data?.length) setSelectedId(plans.data[0].id);
  }, [plans.data, selectedId]);

  return (
    <div>
      <PageHeader
        title="Automation"
        description="Execution plans, live step progress, validation and rollback, plus the autonomous run history."
        actions={
          <Tabs value={tab} onValueChange={(v) => setTab(v as "plans" | "history")}>
            <TabsList>
              <TabsTrigger value="plans">Execution plans</TabsTrigger>
              <TabsTrigger value="history">Autonomous runs</TabsTrigger>
            </TabsList>
          </Tabs>
        }
      />

      {tab === "plans" ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
          <Card className="lg:col-span-1">
            <CardHeader><CardTitle>Plans</CardTitle></CardHeader>
            <CardContent className="max-h-[70vh] space-y-1.5 overflow-y-auto">
              {plans.isLoading && <LoadingRows rows={6} />}
              {plans.data?.map((p) => (
                <button
                  key={p.id}
                  onClick={() => setSelectedId(p.id)}
                  className={cn(
                    "focus-ring flex w-full flex-col gap-1 rounded-md border px-2.5 py-2 text-left text-xs hover:border-border-strong",
                    selectedId === p.id ? "border-primary bg-primary/5" : "border-border"
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate font-medium">{p.title}</span>
                    <Badge variant="outline" className={cn("shrink-0 capitalize", STATE_TONE[p.state])}>{p.state.replace(/_/g, " ")}</Badge>
                  </div>
                  <div className="flex items-center justify-between text-muted-foreground">
                    <span>{p.environment} · {p.region}</span>
                    <MoneyDisplay money={p.expected_monthly_saving} size="sm" />
                  </div>
                </button>
              ))}
            </CardContent>
          </Card>

          <div className="lg:col-span-2">
            <PlanDetail id={selectedId} />
          </div>
        </div>
      ) : (
        <AutonomousHistory />
      )}
    </div>
  );
}

function PlanDetail({ id }: { id: string | undefined }) {
  const plan = useExecutionPlan(id);
  const validation = usePlanValidation(plan.data?.state === "validating" || plan.data?.state === "validated" || plan.data?.state === "failed" ? id : undefined);
  const execute = useExecutePlan();
  const cancel = useCancelPlan();
  const rollback = useRollbackPlan();

  if (!id) return <EmptyState title="Select a plan" />;

  return (
    <QueryBoundary isLoading={plan.isLoading} isError={plan.isError} error={plan.error} data={plan.data} onRetry={() => plan.refetch()}>
      {(p) => {
        const doneSteps = p.steps.filter((s) => s.state === "succeeded").length;
        return (
          <div className="space-y-4">
            <Card>
              <CardHeader className="flex-row items-start justify-between space-y-0">
                <div>
                  <CardTitle>{p.title}</CardTitle>
                  <CardDescription>{p.action.replace(/_/g, " ")} · {p.account_id} · {p.region} · requested by {p.requested_by}</CardDescription>
                </div>
                <Badge variant="outline" className={cn("shrink-0 capitalize", STATE_TONE[p.state])}>{p.state.replace(/_/g, " ")}</Badge>
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="flex items-center justify-between text-sm">
                  <MoneyDisplay money={p.expected_monthly_saving} size="lg" />
                  <span className="text-xs text-muted-foreground">baseline <MoneyDisplay money={p.baseline_monthly_cost} size="sm" muted /></span>
                </div>
                <Progress value={(doneSteps / Math.max(1, p.steps.length)) * 100} />
                <div className="flex flex-wrap gap-2">
                  {(p.state === "approved" || p.state === "draft" || p.state === "scheduled") && (
                    <Button size="sm" onClick={() => execute.mutate(p.id)} disabled={execute.isPending}>
                      <PlayCircle className="h-3.5 w-3.5" /> Execute
                    </Button>
                  )}
                  {["draft", "awaiting_approval", "approved", "scheduled", "preflight"].includes(p.state) && (
                    <Button size="sm" variant="outline" onClick={() => cancel.mutate({ id: p.id, reason: "Cancelled from Automation view" })} disabled={cancel.isPending}>
                      Cancel
                    </Button>
                  )}
                  {(p.state === "executed" || p.state === "validated" || p.state === "failed") && (
                    <Button size="sm" variant="destructive" onClick={() => rollback.mutate({ id: p.id, reason: "Rolled back from Automation view" })} disabled={rollback.isPending}>
                      <RotateCcw className="h-3.5 w-3.5" /> Rollback
                    </Button>
                  )}
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader><CardTitle>Steps</CardTitle></CardHeader>
              <CardContent>
                <ol className="space-y-2">
                  {p.steps.map((s) => <StepRow key={s.id} step={s} />)}
                </ol>
              </CardContent>
            </Card>

            {p.rollback && (
              <Card>
                <CardHeader><CardTitle>Rollback plan</CardTitle><CardDescription>{p.rollback.summary}</CardDescription></CardHeader>
                <CardContent className="space-y-2 text-xs">
                  <div className="flex items-center gap-3">
                    <Badge variant={p.rollback.feasible ? "success" : "destructive"}>{p.rollback.feasible ? "Feasible" : "Not feasible"}</Badge>
                    <span className="text-muted-foreground">Data loss risk: {p.rollback.data_loss_risk}</span>
                    <span className="text-muted-foreground">Est. duration: {Math.round(p.rollback.estimated_duration / 1e9)}s</span>
                  </div>
                  {p.rollback.infeasible_reason && <p className="text-destructive">{p.rollback.infeasible_reason}</p>}
                </CardContent>
              </Card>
            )}

            {validation.data && (
              <Card>
                <CardHeader className="flex-row items-center justify-between space-y-0">
                  <div>
                    <CardTitle>Validation result</CardTitle>
                    <CardDescription>{validation.data.explanation}</CardDescription>
                  </div>
                  <Badge variant={validation.data.verdict === "success" ? "success" : validation.data.verdict === "failure" ? "destructive" : "warning"} className="capitalize">{validation.data.verdict.replace(/_/g, " ")}</Badge>
                </CardHeader>
                <CardContent className="space-y-2">
                  <div className="flex items-center justify-between text-xs">
                    <span>Predicted saving: <MoneyDisplay money={validation.data.predicted_monthly_saving} size="sm" /></span>
                    <span>Observed: <MoneyDisplay money={validation.data.observed_monthly_saving} size="sm" /></span>
                    <span>Accuracy: {(validation.data.saving_accuracy * 100).toFixed(0)}%</span>
                  </div>
                  <ul className="space-y-1.5">
                    {validation.data.checks.map((c) => (
                      <li key={c.name} className="flex items-center justify-between rounded-md border border-border px-2 py-1.5 text-xs">
                        <span className="flex items-center gap-1.5">
                          {c.passed ? <CheckCircle2 className="h-3 w-3 text-success" /> : <XCircle className="h-3 w-3 text-destructive" />}
                          {c.name} {c.critical && <Badge variant="destructive" className="text-[9px]">critical</Badge>}
                        </span>
                        <span className="tabular-nums text-muted-foreground">{c.observed.toFixed(1)} vs {c.baseline.toFixed(1)} baseline ({c.change_pct > 0 ? "+" : ""}{c.change_pct.toFixed(1)}%)</span>
                      </li>
                    ))}
                  </ul>
                  {validation.data.rollback_triggered && <p className="text-xs font-medium text-destructive">Automatic rollback triggered: {validation.data.rollback_reason}</p>}
                </CardContent>
              </Card>
            )}
          </div>
        );
      }}
    </QueryBoundary>
  );
}

function StepRow({ step }: { step: Step }) {
  const Icon = STEP_ICON[step.state];
  return (
    <li className="flex items-start gap-2.5 rounded-md border border-border px-2.5 py-2">
      <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", step.state === "succeeded" && "text-success", step.state === "failed" && "text-destructive", step.state === "running" && "animate-spin text-primary", step.state === "pending" && "text-muted-foreground")} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-sm font-medium">{step.name}</span>
          <Badge variant="secondary" className="shrink-0 text-[10px] capitalize">{step.kind}</Badge>
        </div>
        <p className="text-xs text-muted-foreground">{step.describe}</p>
        {step.error && <p className="text-xs text-destructive">{step.error}</p>}
        {(step.started_at || step.finished_at) && (
          <p className="text-[10px] text-muted-foreground">
            {step.started_at && formatDateTime(step.started_at)}{step.finished_at && ` → ${formatDateTime(step.finished_at)}`}
          </p>
        )}
      </div>
    </li>
  );
}

function AutonomousHistory() {
  const history = useAutonomousHistory();
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5"><Clock className="h-4 w-4" /> Autonomous run history</CardTitle>
        <CardDescription>Each row is one policy-driven sweep across open recommendations.</CardDescription>
      </CardHeader>
      <CardContent>
        <QueryBoundary isLoading={history.isLoading} isError={history.isError} error={history.error} data={history.data} onRetry={() => history.refetch()}>
          {(runs) => (
            <div className="overflow-x-auto">
              <table className="w-full text-xs">
                <thead>
                  <tr className="border-b border-border text-left text-muted-foreground">
                    <th className="py-1.5 pr-3 font-medium">Considered</th>
                    <th className="py-1.5 pr-3 font-medium">Planned</th>
                    <th className="py-1.5 pr-3 font-medium">Executed</th>
                    <th className="py-1.5 pr-3 font-medium">Skipped</th>
                    <th className="py-1.5 pr-3 font-medium">Failed</th>
                    <th className="py-1.5 pr-3 font-medium">Rolled back</th>
                    <th className="py-1.5 pr-3 font-medium">Saving</th>
                    <th className="py-1.5 pr-3 font-medium">Duration</th>
                  </tr>
                </thead>
                <tbody>
                  {runs.map((r, i) => (
                    <tr key={i} className="border-b border-border">
                      <td className="py-1.5 pr-3 tabular-nums">{r.considered}</td>
                      <td className="py-1.5 pr-3 tabular-nums">{r.planned}</td>
                      <td className="py-1.5 pr-3 tabular-nums text-success">{r.executed}</td>
                      <td className="py-1.5 pr-3 tabular-nums text-muted-foreground">{r.skipped}</td>
                      <td className="py-1.5 pr-3 tabular-nums text-destructive">{r.failed}</td>
                      <td className="py-1.5 pr-3 tabular-nums text-warning">{r.rolled_back}</td>
                      <td className="py-1.5 pr-3"><MoneyDisplay money={r.monthly_saving} size="sm" /></td>
                      <td className="py-1.5 pr-3 tabular-nums text-muted-foreground">{(r.duration_ms / 1000).toFixed(1)}s</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </QueryBoundary>
      </CardContent>
    </Card>
  );
}
