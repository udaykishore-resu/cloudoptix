"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { AutonomousRunResult, ExecutionPlan, LearningResult, PlanValidationResult } from "@/types/domain";
import { isMock, mockDelay, request } from "./client";
import * as fx from "@/lib/mock/fixtures/automation";

async function fetchPlans(): Promise<ExecutionPlan[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildExecutionPlans();
  }
  return request<ExecutionPlan[]>("/executions");
}
async function fetchPlan(id: string): Promise<ExecutionPlan | undefined> {
  if (isMock()) {
    await mockDelay(150);
    return fx.getExecutionPlan(id);
  }
  return request<ExecutionPlan>(`/executions/${id}`);
}
async function fetchValidation(planId: string): Promise<PlanValidationResult | undefined> {
  if (isMock()) {
    await mockDelay(300);
    return fx.buildValidationResult(planId);
  }
  return request<PlanValidationResult>(`/executions/${planId}/validate`, { method: "POST" });
}
async function fetchAutonomousHistory(): Promise<AutonomousRunResult[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildAutonomousHistory();
  }
  return request<AutonomousRunResult[]>("/automation/process");
}
async function fetchLearning(): Promise<LearningResult> {
  if (isMock()) {
    await mockDelay();
    return fx.buildLearningResult();
  }
  return request<LearningResult>("/automation/learn", { method: "POST" });
}

export function useExecutionPlans() {
  return useQuery({ queryKey: ["automation", "plans"], queryFn: fetchPlans });
}
export function useExecutionPlan(id: string | undefined) {
  return useQuery({ queryKey: ["automation", "plan", id], queryFn: () => fetchPlan(id as string), enabled: !!id, refetchInterval: (q) => (q.state.data?.state === "executing" ? 2000 : false) });
}
export function usePlanValidation(planId: string | undefined) {
  return useQuery({ queryKey: ["automation", "validation", planId], queryFn: () => fetchValidation(planId as string), enabled: !!planId });
}
export function useAutonomousHistory() {
  return useQuery({ queryKey: ["automation", "autonomous-history"], queryFn: fetchAutonomousHistory });
}
export function useLearningResult() {
  return useQuery({ queryKey: ["automation", "learning"], queryFn: fetchLearning, enabled: false });
}

export function useExecutePlan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      if (isMock()) {
        await mockDelay(400);
        const plan = fx.getExecutionPlan(id);
        if (plan) plan.state = "executing";
        return plan;
      }
      return request(`/executions/${id}/execute`, { method: "POST" });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["automation"] }),
  });
}
export function useCancelPlan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, reason }: { id: string; reason: string }) => {
      if (isMock()) {
        await mockDelay(300);
        const plan = fx.getExecutionPlan(id);
        if (plan) {
          plan.state = "cancelled";
          plan.state_reason = reason;
        }
        return plan;
      }
      return request(`/executions/${id}/cancel`, { method: "POST", body: { reason } });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["automation"] }),
  });
}
export function useRollbackPlan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, reason }: { id: string; reason: string }) => {
      if (isMock()) {
        await mockDelay(500);
        const plan = fx.getExecutionPlan(id);
        if (plan) plan.state = "rolled_back";
        return plan;
      }
      return request(`/executions/${id}/rollback`, { method: "POST", body: { reason } });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["automation"] }),
  });
}
