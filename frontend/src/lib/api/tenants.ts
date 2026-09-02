"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Tenant, User } from "@/types/api";
import type { SpecVersion } from "@/types/api";
import { isMock, mockDelay, request } from "./client";

const MOCK_TENANT: Tenant = { id: "tn_01hz3k4x8y", slug: "acme-corp", name: "Acme Corp", plan: "standard", state: "active", spec_id: "spec_1", active_spec_version: 6, active_policy_id: "pol_1", demo: true };

const MOCK_USERS: User[] = [
  { id: "usr_1", email: "priya.nair@acme.io", name: "Priya Nair", memberships: [{ tenant_id: "tn_01hz3k4x8y", roles: ["tenant_admin", "architect"] }], last_login_at: "2026-08-31T03:20:00Z", disabled: false, created_at: "2026-03-14T09:00:00Z" },
  { id: "usr_2", email: "marcus.reyes@acme.io", name: "Marcus Reyes", memberships: [{ tenant_id: "tn_01hz3k4x8y", roles: ["sre", "developer"] }], last_login_at: "2026-08-30T21:05:00Z", disabled: false, created_at: "2026-03-15T10:00:00Z" },
  { id: "usr_3", email: "sofia.moreau@acme.io", name: "Sofia Moreau", memberships: [{ tenant_id: "tn_01hz3k4x8y", roles: ["finops_analyst"] }], last_login_at: "2026-08-29T15:40:00Z", disabled: false, created_at: "2026-03-18T10:00:00Z" },
  { id: "usr_4", email: "devon.clarke@acme.io", name: "Devon Clarke", memberships: [{ tenant_id: "tn_01hz3k4x8y", roles: ["developer"] }], last_login_at: "2026-08-25T11:12:00Z", disabled: false, created_at: "2026-04-02T10:00:00Z" },
  { id: "usr_5", email: "amara.okafor@acme.io", name: "Amara Okafor", memberships: [{ tenant_id: "tn_01hz3k4x8y", roles: ["auditor", "viewer"] }], last_login_at: "2026-08-10T09:00:00Z", disabled: true, created_at: "2026-04-10T10:00:00Z" },
];

export interface NotificationChannel {
  id: string;
  kind: "slack" | "email" | "pagerduty" | "webhook";
  label: string;
  target: string;
  events: string[];
  enabled: boolean;
}
const MOCK_CHANNELS: NotificationChannel[] = [
  { id: "ch_1", kind: "slack", label: "#cloud-cost-alerts", target: "https://hooks.slack.com/services/…", events: ["anomaly_detected", "budget_at_risk", "approval_requested"], enabled: true },
  { id: "ch_2", kind: "email", label: "FinOps distribution list", target: "finops@acme.io", events: ["weekly_digest", "budget_breached"], enabled: true },
  { id: "ch_3", kind: "pagerduty", label: "SRE on-call", target: "cloudoptix-sre-service", events: ["rollback_triggered", "execution_failed"], enabled: true },
  { id: "ch_4", kind: "webhook", label: "Internal audit sink", target: "https://internal.acme.io/hooks/cloudoptix", events: ["approval_decided", "policy_activated"], enabled: false },
];

const MOCK_SPEC_VERSIONS: SpecVersion[] = Array.from({ length: 6 }).map((_, i) => ({
  id: `spv_${6 - i}`,
  tenant_id: "tn_01hz3k4x8y",
  spec_id: "spec_1",
  version: 6 - i,
  status: i === 0 ? "approved" : "superseded",
  checksum: `chk_${6 - i}`,
  diff: i === 0 ? [] : ([{ path: "objectives.cost_ceiling", before: "$195,000/mo", after: "$210,000/mo", kind: "modified" }] as unknown as SpecVersion["diff"]),
}));

export function useTenant() {
  return useQuery({
    queryKey: ["tenant"],
    queryFn: async () => {
      if (isMock()) { await mockDelay(); return MOCK_TENANT; }
      return request<Tenant>("/tenant");
    },
  });
}
export function useUsers() {
  return useQuery({
    queryKey: ["tenant", "users"],
    queryFn: async () => {
      if (isMock()) { await mockDelay(); return MOCK_USERS; }
      return request<User[]>("/tenant/users");
    },
  });
}
export function useNotificationChannels() {
  return useQuery({
    queryKey: ["tenant", "channels"],
    queryFn: async () => {
      if (isMock()) { await mockDelay(); return MOCK_CHANNELS; }
      return MOCK_CHANNELS; // no backend endpoint declared for this in the OpenAPI contract
    },
  });
}
export function useSpecVersions() {
  return useQuery({
    queryKey: ["specs", "versions"],
    queryFn: async () => {
      if (isMock()) { await mockDelay(); return MOCK_SPEC_VERSIONS; }
      return request<SpecVersion[]>("/specs");
    },
  });
}

export function useInviteUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ email, roles }: { email: string; roles: string[] }) => {
      if (isMock()) {
        await mockDelay(400);
        const u: User = { id: `usr_${Date.now()}`, email, name: email.split("@")[0], memberships: [{ tenant_id: "tn_01hz3k4x8y", roles: roles as NonNullable<User["memberships"]>[number]["roles"] }], disabled: false, created_at: new Date().toISOString() };
        MOCK_USERS.push(u);
        return u;
      }
      return request("/tenant/users", { method: "POST", body: { email, roles } });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tenant", "users"] }),
  });
}
