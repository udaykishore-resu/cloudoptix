"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { DiscoveryRun, DiscoveryStatus } from "@/types/api";
import { isMock, mockDelay, request } from "./client";
import { getWorld } from "@/lib/mock/world";

function buildRuns(): DiscoveryRun[] {
  const w = getWorld();
  return w.accounts.map((a, i) => ({
    id: `run_${a.id}`,
    tenant_id: "tn_01hz3k4x8y",
    account_id: a.accountId,
    regions: a.regions,
    trigger: i === 0 ? "scheduled" : "manual",
    state: a.state === "degraded" ? "partial" : "completed",
    started_at: "2026-08-31T04:00:00Z",
    finished_at: "2026-08-31T04:06:00Z",
    resources_discovered: Math.round(w.resources.filter((r) => r.account_id === a.accountId).length),
    resources_updated: 12,
    resources_removed: 1,
    relationships_found: w.edges.length,
    metrics_collected: 4200,
    service_results: [
      { service: "ec2", region: a.regions[0], succeeded: true, count: 40, duration_ms: 1200, api_call_count: 88 },
      { service: "rds", region: a.regions[0], succeeded: true, count: 6, duration_ms: 600, api_call_count: 22 },
    ],
    errors: a.missingActions.length ? [`Skipped ${a.missingActions.length} resource types: missing IAM permissions`] : [],
    coverage: a.missingActions.length ? 0.81 : 0.98,
    duration_ms: 360000,
  })) as unknown as DiscoveryRun[];
}

export function useDiscoveryRuns() {
  return useQuery({
    queryKey: ["discovery", "runs"],
    queryFn: async () => {
      if (isMock()) { await mockDelay(); return buildRuns(); }
      return request<DiscoveryRun[]>("/discovery/runs");
    },
  });
}

export function useDiscoveryStatus() {
  return useQuery({
    queryKey: ["discovery", "status"],
    queryFn: async () => {
      if (isMock()) {
        await mockDelay();
        const w = getWorld();
        return {
          last_run_at: "2026-08-31T04:06:00Z",
          resource_count: w.resources.length,
          accounts_covered: w.accounts.filter((a) => a.state !== "pending").length,
          accounts_total: w.accounts.length,
          coverage: 0.94,
          in_progress: false,
          recent_runs: buildRuns(),
          permission_issues: w.accounts.filter((a) => a.missingActions.length).map((a) => `${a.alias}: missing ${a.missingActions.join(", ")}`),
        } as unknown as DiscoveryStatus;
      }
      return request<DiscoveryStatus>("/discovery/status");
    },
  });
}

export function useRunDiscovery() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (accountId?: string) => {
      if (isMock()) {
        await mockDelay(2200);
        return buildRuns()[0];
      }
      return request("/discovery/runs", { method: "POST", body: { account_id: accountId } });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["discovery"] }),
  });
}
