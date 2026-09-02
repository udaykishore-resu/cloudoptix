"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApprovalRequest, Policy, PolicyDecision, PolicySimulation } from "@/types/domain";
import type { ValidationResult } from "@/types/api";
import { isMock, mockDelay, request } from "./client";
import * as fx from "@/lib/mock/fixtures/governance";

async function fetchPolicy(): Promise<Policy> {
  if (isMock()) {
    await mockDelay();
    return fx.buildActivePolicy();
  }
  return request<Policy>("/policies/active");
}
async function fetchPolicyVersions(): Promise<Policy[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildPolicyVersions();
  }
  return request<Policy[]>("/policies/versions");
}
async function fetchDecision(recId: string): Promise<PolicyDecision | undefined> {
  if (isMock()) {
    await mockDelay(200);
    return fx.evaluateDecision(recId);
  }
  return request<PolicyDecision>(`/recommendations/${recId}/policy-decision`);
}
async function fetchSimulation(): Promise<PolicySimulation> {
  if (isMock()) {
    await mockDelay(500);
    return fx.buildPolicySimulation();
  }
  return request<PolicySimulation>("/policies/simulate", { method: "POST", body: {} });
}
async function fetchApprovals(): Promise<ApprovalRequest[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildApprovals();
  }
  return request<ApprovalRequest[]>("/approvals");
}
async function fetchApproval(id: string): Promise<ApprovalRequest | undefined> {
  if (isMock()) {
    await mockDelay(150);
    return fx.getApproval(id);
  }
  return request<ApprovalRequest>(`/approvals/${id}`);
}

export function usePolicy() {
  return useQuery({ queryKey: ["governance", "policy"], queryFn: fetchPolicy });
}
export function usePolicyVersions() {
  return useQuery({ queryKey: ["governance", "policy-versions"], queryFn: fetchPolicyVersions });
}
export function usePolicyDecision(recId: string | undefined) {
  return useQuery({ queryKey: ["governance", "decision", recId], queryFn: () => fetchDecision(recId as string), enabled: !!recId });
}
export function usePolicySimulation(enabled: boolean) {
  return useQuery({ queryKey: ["governance", "simulate"], queryFn: fetchSimulation, enabled });
}
export function useApprovals() {
  return useQuery({ queryKey: ["governance", "approvals"], queryFn: fetchApprovals });
}
export function useApproval(id: string | undefined) {
  return useQuery({ queryKey: ["governance", "approval", id], queryFn: () => fetchApproval(id as string), enabled: !!id });
}

export function useValidatePolicy() {
  return useMutation({
    mutationFn: async (_policy: Policy): Promise<ValidationResult> => {
      if (isMock()) {
        await mockDelay(300);
        return { issues: [] };
      }
      return request<ValidationResult>("/policies/validate", { method: "POST", body: _policy });
    },
  });
}

export function useSavePolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (policy: Policy) => {
      if (isMock()) {
        await mockDelay(400);
        return { ...policy, version: policy.version + 1 };
      }
      return request<Policy>("/policies", { method: "PUT", body: policy });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["governance"] }),
  });
}

export function useDecideApproval() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, approved, comment }: { id: string; approved: boolean; comment?: string }) => {
      if (isMock()) {
        await mockDelay(400);
        const a = fx.getApproval(id);
        if (a) {
          a.state = approved ? "approved" : "rejected";
          a.responses = [...(a.responses ?? []), { principal: "you@acme.io", role: "tenant_admin", approved, comment, at: new Date().toISOString() }];
          a.decided_at = new Date().toISOString();
        }
        return a;
      }
      return request(`/approvals/${id}/decide`, { method: "POST", body: { approved, comment } });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["governance", "approvals"] }),
  });
}
