"use client";
import * as React from "react";
import { CheckCircle2, Search, ShieldCheck, XCircle } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { QueryBoundary, EmptyState } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useAuditEntries, useAuditTimeline, useAuditVerify, type AuditFilter } from "@/lib/api/audit";
import { cn, formatDateTime } from "@/lib/utils";

const ACTIONS = ["recommendation", "execution", "policy", "spec", "aws_account"];

export default function AuditPage() {
  const [search, setSearch] = React.useState("");
  const [action, setAction] = React.useState("all");
  const [outcome, setOutcome] = React.useState("all");
  const [selectedSubject, setSelectedSubject] = React.useState<string | undefined>();

  const filter: AuditFilter = {
    search: search || undefined,
    actions: action !== "all" ? [action] : undefined,
    outcomes: outcome !== "all" ? [outcome] : undefined,
  };
  const entries = useAuditEntries(filter);
  const verify = useAuditVerify();
  const timeline = useAuditTimeline(selectedSubject);

  return (
    <div>
      <PageHeader title="Audit" description="Searchable, tamper-evident trail of every governed change." />

      <QueryBoundary isLoading={verify.isLoading} isError={verify.isError} error={verify.error} data={verify.data}>
        {(v) => (
          <Card className="mb-4">
            <CardContent className="flex items-center gap-3 p-3.5">
              <span className={cn("flex h-8 w-8 items-center justify-center rounded-full", v.chain_valid ? "bg-success/15 text-success" : "bg-destructive/15 text-destructive")}>
                {v.chain_valid ? <ShieldCheck className="h-4 w-4" /> : <XCircle className="h-4 w-4" />}
              </span>
              <div>
                <p className="text-sm font-medium">{v.chain_valid ? "Hash chain verified" : "Hash chain verification failed"}</p>
                <p className="text-xs text-muted-foreground">
                  {v.entries_verified} entries verified{v.from && v.to ? ` · ${formatDateTime(v.from)} – ${formatDateTime(v.to)}` : ""} · checked {formatDateTime(v.verified_at)}
                </p>
              </div>
            </CardContent>
          </Card>
        )}
      </QueryBoundary>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <div className="relative w-64">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input placeholder="Search action or message…" value={search} onChange={(e) => setSearch(e.target.value)} className="h-8 pl-8 text-xs" />
        </div>
        <Select value={action} onValueChange={setAction}>
          <SelectTrigger className="h-8 w-44 text-xs"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All action types</SelectItem>
            {ACTIONS.map((a) => <SelectItem key={a} value={a} className="capitalize">{a.replace(/_/g, " ")}</SelectItem>)}
          </SelectContent>
        </Select>
        <Select value={outcome} onValueChange={setOutcome}>
          <SelectTrigger className="h-8 w-36 text-xs"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All outcomes</SelectItem>
            <SelectItem value="success">Success</SelectItem>
            <SelectItem value="failure">Failure</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <QueryBoundary isLoading={entries.isLoading} isError={entries.isError} error={entries.error} data={entries.data} onRetry={() => entries.refetch()} isEmpty={(d) => d.length === 0} empty={<EmptyState title="No matching audit entries" />}>
            {(list) => (
              <ul className="space-y-1.5">
                {list.map((e) => (
                  <li key={e.id}>
                    <button
                      onClick={() => setSelectedSubject(e.subject_id)}
                      className={cn(
                        "focus-ring flex w-full items-start gap-2.5 rounded-md border px-3 py-2 text-left text-xs hover:border-border-strong hover:bg-secondary/30",
                        selectedSubject === e.subject_id ? "border-primary bg-primary/5" : "border-border"
                      )}
                    >
                      <span className="mt-0.5">
                        {e.outcome === "success" ? <CheckCircle2 className="h-3.5 w-3.5 text-success" /> : <XCircle className="h-3.5 w-3.5 text-destructive" />}
                      </span>
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center justify-between gap-2">
                          <span className="truncate font-mono font-medium">{e.action}</span>
                          <span className="shrink-0 text-muted-foreground">{formatDateTime(e.at)}</span>
                        </div>
                        <p className="truncate text-muted-foreground">{e.message}</p>
                        <div className="mt-1 flex items-center gap-1.5">
                          <Badge variant={e.machine ? "secondary" : "outline"} className="text-[9px]">{e.actor}</Badge>
                          <span className="text-[10px] text-muted-foreground">seq #{e.sequence} · {e.hash}</span>
                        </div>
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </QueryBoundary>
        </div>

        <div className="lg:col-span-1">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">Timeline</CardTitle>
              <CardDescription>{selectedSubject ? `Changes to ${selectedSubject}` : "Select an entry to see its full timeline"}</CardDescription>
            </CardHeader>
            <CardContent>
              {!selectedSubject && <p className="text-xs text-muted-foreground">Nothing selected.</p>}
              {selectedSubject && (
                <QueryBoundary isLoading={timeline.isLoading} isError={timeline.isError} error={timeline.error} data={timeline.data} isEmpty={(d) => d.length === 0} empty={<p className="text-xs text-muted-foreground">No timeline events.</p>}>
                  {(events) => (
                    <ol className="relative space-y-3 border-l border-border pl-4">
                      {[...events].reverse().map((e) => (
                        <li key={e.id} className="relative">
                          <span className={cn("absolute -left-[1.15rem] top-0.5 h-2 w-2 rounded-full", e.outcome === "success" ? "bg-success" : "bg-destructive")} />
                          <p className="text-xs font-medium">{e.action}</p>
                          <p className="text-[11px] text-muted-foreground">{e.message}</p>
                          <p className="text-[10px] text-muted-foreground">{formatDateTime(e.at)} · {e.actor}</p>
                        </li>
                      ))}
                    </ol>
                  )}
                </QueryBoundary>
              )}
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
