"use client";
import { useQuery } from "@tanstack/react-query";
import type { AuditEntry } from "@/types/domain";
import { isMock, mockDelay, request } from "./client";
import { buildRecommendations } from "@/lib/mock/fixtures/recommendations";
import { buildExecutionPlans } from "@/lib/mock/fixtures/automation";
import { resetRng } from "@/lib/mock/rng";

let cache: AuditEntry[] | null = null;

function buildEntries(): AuditEntry[] {
  if (cache) return cache;
  resetRng(81001);
  const entries: AuditEntry[] = [];
  let seq = 1;
  const push = (e: Omit<AuditEntry, "id" | "sequence" | "hash">) => {
    entries.push({ ...e, id: `ae_${seq}`, sequence: seq, hash: `h${seq.toString(16).padStart(8, "0")}` });
    seq++;
  };
  const recs = buildRecommendations().slice(0, 40);
  for (const r of recs) {
    push({ action: "recommendation.created", outcome: "success", actor: "recommendation-engine", machine: true, subject: "recommendation", subject_id: r.id, message: `Created recommendation "${r.title}"`, after: { status: "open" }, at: r.created_at });
    if (r.status !== "open") {
      push({ action: `recommendation.${r.status}`, outcome: "success", actor: r.status === "dismissed" ? "priya.nair@acme.io" : "policy-engine", machine: r.status !== "dismissed", subject: "recommendation", subject_id: r.id, message: `Recommendation transitioned to ${r.status}`, before: { status: "open" }, after: { status: r.status }, at: r.updated_at });
    }
  }
  const plans = buildExecutionPlans().slice(0, 20);
  for (const p of plans) {
    push({ action: "execution.planned", outcome: "success", actor: "automation-engine", machine: true, subject: "execution_plan", subject_id: p.id, message: `Execution plan created for "${p.title}"`, metadata: { steps: p.steps.length }, at: p.created_at });
    if (p.started_at) push({ action: "execution.started", outcome: "success", actor: p.requested_by, machine: p.requested_by === "automation-engine", subject: "execution_plan", subject_id: p.id, message: "Execution started", at: p.started_at });
    if (p.finished_at) push({ action: "execution.finished", outcome: p.state === "failed" ? "failure" : "success", actor: "automation-engine", machine: true, subject: "execution_plan", subject_id: p.id, message: `Execution reached state ${p.state}`, at: p.finished_at });
  }
  push({ action: "policy.activated", outcome: "success", actor: "priya.nair@acme.io", machine: false, subject: "policy", subject_id: "pol_1", message: "Activated policy \"balanced\" v4", at: "2026-08-10T00:00:00Z" });
  push({ action: "spec.approved", outcome: "success", actor: "priya.nair@acme.io", machine: false, subject: "spec_version", subject_id: "spv_6", message: "Approved specification version 6", at: "2026-08-01T00:00:00Z" });
  push({ action: "aws_account.connected", outcome: "success", actor: "priya.nair@acme.io", machine: false, subject: "aws_account", subject_id: "acct_shared01", message: "Connected AWS account shared-services (degraded — missing 4 IAM actions)", at: "2026-03-14T09:07:00Z" });
  entries.sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime());
  entries.forEach((e, i) => (e.sequence = entries.length - i));
  cache = entries;
  return entries;
}

export interface AuditFilter {
  actions?: string[];
  actors?: string[];
  outcomes?: string[];
  search?: string;
}

async function fetchEntries(filter: AuditFilter): Promise<AuditEntry[]> {
  if (isMock()) {
    await mockDelay();
    let list = buildEntries();
    if (filter.actions?.length) list = list.filter((e) => filter.actions!.some((a) => e.action.startsWith(a)));
    if (filter.actors?.length) list = list.filter((e) => filter.actors!.includes(e.actor));
    if (filter.outcomes?.length) list = list.filter((e) => filter.outcomes!.includes(e.outcome));
    if (filter.search) {
      const q = filter.search.toLowerCase();
      list = list.filter((e) => e.message.toLowerCase().includes(q) || e.action.toLowerCase().includes(q));
    }
    return list;
  }
  return request<AuditEntry[]>("/audit");
}

async function fetchTimeline(recommendationId: string): Promise<AuditEntry[]> {
  if (isMock()) {
    await mockDelay(200);
    return buildEntries().filter((e) => e.subject_id === recommendationId || (e.subject === "execution_plan" && e.subject_id?.includes(recommendationId)));
  }
  return request<AuditEntry[]>(`/audit/recommendations/${recommendationId}/timeline`);
}

export function useAuditEntries(filter: AuditFilter = {}) {
  return useQuery({ queryKey: ["audit", "entries", filter], queryFn: () => fetchEntries(filter) });
}
export function useAuditTimeline(recommendationId: string | undefined) {
  return useQuery({ queryKey: ["audit", "timeline", recommendationId], queryFn: () => fetchTimeline(recommendationId as string), enabled: !!recommendationId });
}
export interface ChainVerification {
  chain_valid: boolean;
  entries_verified: number;
  from?: string;
  to?: string;
  verified_at: string;
}

export function useAuditVerify() {
  return useQuery({
    queryKey: ["audit", "verify"],
    queryFn: async (): Promise<ChainVerification> => {
      if (isMock()) {
        await mockDelay(600);
        const entries = buildEntries();
        return { chain_valid: true, entries_verified: entries.length, from: entries[entries.length - 1]?.at, to: entries[0]?.at, verified_at: new Date().toISOString() };
      }
      return request<ChainVerification>("/audit/verify");
    },
  });
}
