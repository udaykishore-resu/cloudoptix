"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Recommendation, RecommendationExplanation, RecommendationSummary, RuleInfo } from "@/types/domain";
import { isMock, mockDelay, request } from "./client";
import * as fx from "@/lib/mock/fixtures/recommendations";

export interface RecommendationFilter {
  status?: string[];
  category?: string[];
  risk?: string[];
  environment?: string[];
  search?: string;
}

function applyFilter(list: Recommendation[], f: RecommendationFilter): Recommendation[] {
  let out = list;
  if (f.status?.length) out = out.filter((r) => f.status!.includes(r.status));
  if (f.category?.length) out = out.filter((r) => f.category!.includes(r.finding.category));
  if (f.risk?.length) out = out.filter((r) => f.risk!.includes(r.risk.level));
  if (f.environment?.length) out = out.filter((r) => f.environment!.includes(r.finding.environment));
  if (f.search) {
    const q = f.search.toLowerCase();
    out = out.filter((r) => r.title.toLowerCase().includes(q) || r.finding.resource_name.toLowerCase().includes(q));
  }
  return out;
}

async function fetchList(filter: RecommendationFilter): Promise<Recommendation[]> {
  if (isMock()) {
    await mockDelay();
    return applyFilter(fx.buildRecommendations(), filter);
  }
  return request<Recommendation[]>("/recommendations");
}

async function fetchOne(id: string): Promise<Recommendation | undefined> {
  if (isMock()) {
    await mockDelay(150);
    return fx.getRecommendation(id);
  }
  return request<Recommendation>(`/recommendations/${id}`);
}

async function fetchSummary(): Promise<RecommendationSummary> {
  if (isMock()) {
    await mockDelay();
    return fx.buildSummary();
  }
  return request<RecommendationSummary>("/recommendations/summary");
}

async function fetchExplanation(id: string): Promise<RecommendationExplanation | undefined> {
  if (isMock()) {
    await mockDelay(300);
    return fx.buildExplanation(id);
  }
  return request<RecommendationExplanation>(`/recommendations/${id}/explain`);
}

async function fetchRules(): Promise<RuleInfo[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildRuleCatalog();
  }
  return request<RuleInfo[]>("/recommendations/rules");
}

export function useRecommendations(filter: RecommendationFilter = {}) {
  return useQuery({ queryKey: ["recommendations", "list", filter], queryFn: () => fetchList(filter) });
}
export function useRecommendation(id: string | undefined) {
  return useQuery({ queryKey: ["recommendations", "one", id], queryFn: () => fetchOne(id as string), enabled: !!id });
}
export function useRecommendationSummary() {
  return useQuery({ queryKey: ["recommendations", "summary"], queryFn: fetchSummary });
}
export function useRecommendationExplanation(id: string | undefined) {
  return useQuery({ queryKey: ["recommendations", "explain", id], queryFn: () => fetchExplanation(id as string), enabled: !!id });
}
export function useRules() {
  return useQuery({ queryKey: ["recommendations", "rules"], queryFn: fetchRules });
}

export function useDismissRecommendation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, reason }: { id: string; reason: string }) => {
      if (isMock()) {
        await mockDelay(300);
        const rec = fx.getRecommendation(id);
        if (rec) {
          rec.status = "dismissed";
          rec.status_reason = reason;
        }
        return rec;
      }
      return request(`/recommendations/${id}/dismiss`, { method: "POST", body: { reason } });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["recommendations"] }),
  });
}

/** "Approving" a recommendation in the real API means requesting an
 * execution plan for it (POST /recommendations/{id}/execution-plan) — the
 * policy engine then either auto-executes it or opens an ApprovalRequest,
 * per its policy_decision. There is no separate "approve" endpoint in the
 * contract; this hook models that same transition in mock mode. */
export function useApproveRecommendation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      if (isMock()) {
        await mockDelay(300);
        const rec = fx.getRecommendation(id);
        if (rec) {
          rec.status = rec.auto_executable ? "scheduled" : "approved";
        }
        return rec;
      }
      return request(`/recommendations/${id}/execution-plan`, { method: "POST", body: {} });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["recommendations"] }),
  });
}

export function useSnoozeRecommendation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, until, reason }: { id: string; until: string; reason: string }) => {
      if (isMock()) {
        await mockDelay(300);
        const rec = fx.getRecommendation(id);
        if (rec) {
          rec.status = "snoozed";
          rec.snoozed_until = until;
          rec.status_reason = reason;
        }
        return rec;
      }
      return request(`/recommendations/${id}/snooze`, { method: "POST", body: { until, reason } });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["recommendations"] }),
  });
}

export function useAnalyzeRecommendations() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      if (isMock()) {
        await mockDelay(1400);
        return { run_id: "run_mock", resources_analyzed: 240, rules_evaluated: 16, findings_produced: fx.buildRecommendations().length, recommendations_created: 3, superseded: 1, total_monthly_saving: fx.buildSummary().total_monthly_saving, total_annual_saving: fx.buildSummary().total_monthly_saving, auto_executable: fx.buildSummary().auto_executable, requiring_approval: fx.buildSummary().awaiting_approval, prohibited: 0, duration_ms: 1400 };
      }
      return request("/recommendations/analyze", { method: "POST", body: {} });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["recommendations"] }),
  });
}
