"use client";
import * as React from "react";
import { CheckCircle2, Clock, ShieldAlert, XCircle } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { MoneyDisplay } from "@/components/shared/money-display";
import { RiskBadge } from "@/components/shared/risk-badge";
import { ConfidenceBadge } from "@/components/shared/confidence-badge";
import { QueryBoundary, EmptyState } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useApprovals, useDecideApproval } from "@/lib/api/governance";
import { cn, formatDateTime, relativeTime } from "@/lib/utils";
import type { ApprovalRequest, ApprovalState } from "@/types/domain";

const STATE_TONE: Record<ApprovalState, string> = {
  pending: "bg-warning/15 text-warning",
  approved: "bg-success/15 text-success",
  rejected: "bg-destructive/15 text-destructive",
  expired: "bg-muted text-muted-foreground",
  withdrawn: "bg-muted text-muted-foreground",
  cancelled: "bg-muted text-muted-foreground",
};

export default function ApprovalsPage() {
  const [filter, setFilter] = React.useState<"pending" | "all">("pending");
  const approvals = useApprovals();
  const [selectedId, setSelectedId] = React.useState<string | undefined>();

  const list = React.useMemo(() => {
    const all = approvals.data ?? [];
    const l = filter === "pending" ? all.filter((a) => a.state === "pending") : all;
    return [...l].sort((a, b) => (a.state === "pending" ? -1 : 1) - (b.state === "pending" ? -1 : 1) || +new Date(b.requested_at) - +new Date(a.requested_at));
  }, [approvals.data, filter]);

  React.useEffect(() => {
    if (!selectedId && list.length) setSelectedId(list[0].id);
  }, [list, selectedId]);

  const selected = (approvals.data ?? []).find((a) => a.id === selectedId);

  return (
    <div>
      <PageHeader
        title="Approvals"
        description="Governance queue for changes that require a human sign-off."
        actions={
          <Tabs value={filter} onValueChange={(v) => setFilter(v as "pending" | "all")}>
            <TabsList>
              <TabsTrigger value="pending">Pending</TabsTrigger>
              <TabsTrigger value="all">All</TabsTrigger>
            </TabsList>
          </Tabs>
        }
      />

      <QueryBoundary isLoading={approvals.isLoading} isError={approvals.isError} error={approvals.error} data={list.length ? list : approvals.data} onRetry={() => approvals.refetch()} isEmpty={() => list.length === 0} empty={<EmptyState title="Nothing to approve" description="The queue is clear." icon={CheckCircle2} />}>
        {() => (
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <div className="space-y-1.5 lg:col-span-1">
              {list.map((a) => (
                <button
                  key={a.id}
                  onClick={() => setSelectedId(a.id)}
                  className={cn(
                    "focus-ring flex w-full flex-col gap-1 rounded-md border px-2.5 py-2 text-left text-xs hover:border-border-strong",
                    selectedId === a.id ? "border-primary bg-primary/5" : "border-border"
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate font-medium">{a.title}</span>
                    <Badge variant="outline" className={cn("shrink-0 capitalize", STATE_TONE[a.state])}>{a.state}</Badge>
                  </div>
                  <div className="flex items-center justify-between text-muted-foreground">
                    <span>{a.subject_kind.replace(/_/g, " ")}</span>
                    {a.context.monthly_saving && <MoneyDisplay money={a.context.monthly_saving} size="sm" />}
                  </div>
                  <span className="text-[10px] text-muted-foreground">Requested {relativeTime(a.requested_at)} · expires {relativeTime(a.expires_at)}</span>
                </button>
              ))}
            </div>

            <div className="lg:col-span-2">
              {selected ? <ApprovalDetail approval={selected} /> : <EmptyState title="Select an item" />}
            </div>
          </div>
        )}
      </QueryBoundary>
    </div>
  );
}

function ApprovalDetail({ approval: a }: { approval: ApprovalRequest }) {
  const decide = useDecideApproval();
  const [comment, setComment] = React.useState("");

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between space-y-0">
        <div>
          <CardTitle>{a.title}</CardTitle>
          <CardDescription>{a.summary}</CardDescription>
        </div>
        <Badge variant="outline" className={cn("shrink-0 capitalize", STATE_TONE[a.state])}>{a.state}</Badge>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-2 gap-3 rounded-md border border-border p-3 text-xs sm:grid-cols-4">
          {a.context.monthly_saving && <Field label="Monthly saving"><MoneyDisplay money={a.context.monthly_saving} size="sm" /></Field>}
          {a.context.annual_saving && <Field label="Annual saving"><MoneyDisplay money={a.context.annual_saving} size="sm" /></Field>}
          {a.context.monthly_cost_delta && <Field label="Monthly delta"><MoneyDisplay money={a.context.monthly_cost_delta} size="sm" signed /></Field>}
          {a.context.risk_level && <Field label="Risk"><RiskBadge level={a.context.risk_level} /></Field>}
          {a.context.confidence !== undefined && <Field label="Confidence"><ConfidenceBadge confidence={a.context.confidence} size="sm" /></Field>}
          {a.context.environment && <Field label="Environment"><span className="capitalize">{a.context.environment}</span></Field>}
        </div>

        {a.context.blast_summary && (
          <div className="rounded-md border border-warning/30 bg-warning/5 p-2.5 text-xs">
            <p className="mb-0.5 flex items-center gap-1 font-medium text-warning"><ShieldAlert className="h-3.5 w-3.5" /> Blast radius</p>
            <p className="text-muted-foreground">{a.context.blast_summary}</p>
          </div>
        )}

        {a.context.affected_resources?.length ? (
          <div>
            <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Affected resources</p>
            <div className="flex flex-wrap gap-1">
              {a.context.affected_resources.map((r) => <Badge key={r} variant="secondary" className="text-[10px]">{r}</Badge>)}
            </div>
          </div>
        ) : null}

        {a.context.rollback_plan && (
          <div>
            <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Rollback plan</p>
            <p className="text-xs text-muted-foreground">{a.context.rollback_plan}</p>
          </div>
        )}
        {a.context.validation_plan && (
          <div>
            <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Validation plan</p>
            <p className="text-xs text-muted-foreground">{a.context.validation_plan}</p>
          </div>
        )}
        {a.context.policy_reason && (
          <div>
            <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Why this needs approval</p>
            <p className="text-xs text-muted-foreground">{a.context.policy_reason}</p>
          </div>
        )}
        {a.context.diff && (
          <div>
            <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Diff</p>
            <pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded-md bg-surface-sunken p-2 font-mono text-[11px]">{a.context.diff}</pre>
          </div>
        )}

        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          <span>Requires {a.min_approvals} approval{a.min_approvals > 1 ? "s" : ""}{a.require_distinct_approver ? " from distinct approvers" : ""}</span>
          {a.required_roles?.length ? <span>· roles: {a.required_roles.join(", ")}</span> : null}
        </div>

        {a.responses?.length ? (
          <div className="border-t border-border pt-2">
            <p className="mb-1 text-[11px] font-medium uppercase text-muted-foreground">Decision history</p>
            <ul className="space-y-1.5">
              {a.responses.map((r, i) => (
                <li key={i} className="flex items-center justify-between text-xs">
                  <span className="flex items-center gap-1.5">
                    {r.approved ? <CheckCircle2 className="h-3 w-3 text-success" /> : <XCircle className="h-3 w-3 text-destructive" />}
                    {r.principal} ({r.role})
                  </span>
                  <span className="text-muted-foreground">{formatDateTime(r.at)}</span>
                </li>
              ))}
            </ul>
          </div>
        ) : null}

        {a.state === "pending" && (
          <div className="space-y-2 border-t border-border pt-3">
            <Textarea placeholder="Optional comment…" value={comment} onChange={(e) => setComment(e.target.value)} rows={2} />
            <div className="flex gap-2">
              <Button size="sm" onClick={() => decide.mutate({ id: a.id, approved: true, comment: comment || undefined })} disabled={decide.isPending}>
                <CheckCircle2 className="h-3.5 w-3.5" /> Approve
              </Button>
              <Button size="sm" variant="destructive" onClick={() => decide.mutate({ id: a.id, approved: false, comment: comment || undefined })} disabled={decide.isPending}>
                <XCircle className="h-3.5 w-3.5" /> Reject
              </Button>
            </div>
          </div>
        )}
        {a.state !== "pending" && a.decided_at && (
          <p className="flex items-center gap-1 text-xs text-muted-foreground"><Clock className="h-3 w-3" /> Decided {formatDateTime(a.decided_at)}</p>
        )}
      </CardContent>
    </Card>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <p className="text-[10px] uppercase text-muted-foreground">{label}</p>
      {children}
    </div>
  );
}
