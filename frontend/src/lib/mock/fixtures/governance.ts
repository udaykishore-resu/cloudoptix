import type { ApprovalContext, ApprovalRequest, Policy, PolicyDecision, PolicySimulation } from "@/types/domain";
import { moneyOf } from "../world";
import { buildRecommendations } from "./recommendations";
import { rpick, resetRng } from "../rng";

export function buildActivePolicy(): Policy {
  return {
    id: "pol_1",
    tenant_id: "tn_01hz3k4x8y",
    name: "balanced",
    description: "Default balanced governance stance: safe, reversible, low-blast-radius changes auto-execute; everything touching production requires approval.",
    version: 4,
    default_effect: "require_approval",
    enabled: true,
    created_by: "onboarding",
    created_at: "2026-03-20T00:00:00Z",
    activated_at: "2026-08-10T00:00:00Z",
    checksum: "8f2c1e9a6b4d7c3f1a9e5b2d8c4f6a1e",
    rules: [
      {
        id: "auto-waste-nonprod",
        description: "Auto-execute reversible waste elimination outside production",
        match: { categories: ["waste"], environments: ["staging", "development"], min_confidence: 0.85, min_reversibility: "fast" },
        effect: "auto_execute",
        reason: "Low-risk, easily reversed, no production exposure",
      },
      {
        id: "auto-storage-migration",
        description: "Auto-execute gp2→gp3 and lifecycle policy changes anywhere",
        match: { actions: ["modify_volume_type", "apply_s3_lifecycle", "set_log_retention"], min_confidence: 0.8 },
        effect: "auto_execute",
        reason: "Instant rollback, no downtime, well-calibrated rule track record",
      },
      {
        id: "approve-rightsizing-prod",
        description: "Require approval for production rightsizing",
        match: { categories: ["rightsizing"], environments: ["production"] },
        effect: "require_approval",
        approvers: ["sre", "finops_analyst"],
        min_approvals: 1,
        maintenance_windows: ["Sun 02:00-04:00 UTC"],
        reason: "Production compute changes require an SRE or FinOps sign-off",
      },
      {
        id: "prohibit-destructive-tier0",
        description: "Never auto-execute destructive actions against Tier-0 services",
        match: { max_critical_services: 0, min_reversibility: "slow" },
        effect: "require_approval",
        approvers: ["tenant_admin"],
        min_approvals: 2,
        require_distinct_approver: true,
        reason: "Destructive changes near critical services always require two-person review",
      },
    ],
  };
}

export function buildPolicyVersions(): Policy[] {
  const active = buildActivePolicy();
  return [
    active,
    { ...active, id: "pol_1_v3", version: 3, activated_at: "2026-06-02T00:00:00Z", checksum: "3a1b9c7e5d2f8a6c4e1b9d7f3a5c8e2b" },
    { ...active, id: "pol_1_v2", version: 2, activated_at: "2026-04-18T00:00:00Z", checksum: "1c8e4a2b6f9d3c7e5a1b8d4f2c6e9a3b" },
    { ...active, id: "pol_1_v1", version: 1, activated_at: "2026-03-20T00:00:00Z", checksum: "9e3c7a1b5d8f2c4e6a9b1d7f3c5e8a2c" },
  ];
}

export function evaluateDecision(recommendationId: string): PolicyDecision | undefined {
  const rec = buildRecommendations().find((r) => r.id === recommendationId);
  if (!rec) return undefined;
  const policy = buildActivePolicy();
  const effect = rec.auto_executable ? "auto_execute" : "require_approval";
  const rule = effect === "auto_execute" ? (rec.finding.category === "waste" ? "auto-waste-nonprod" : "auto-storage-migration") : "approve-rightsizing-prod";
  return {
    id: `pd_${recommendationId}`,
    tenant_id: "tn_01hz3k4x8y",
    recommendation_id: recommendationId,
    policy_id: policy.id,
    policy_version: policy.version,
    policy_checksum: policy.checksum,
    effect,
    matched_rules: [rule],
    deciding_rule: rule,
    reason: policy.rules.find((r) => r.id === rule)?.reason ?? "policy default",
    explanation: [
      `Policy ${policy.name} v${policy.version} evaluated ${policy.rules.length} rules; 1 matched.`,
      effect === "auto_execute" ? "Matched rule permits autonomous execution for this change class." : "Matched rule requires human approval before execution.",
    ],
    requires_approval: effect === "require_approval",
    approvers: effect === "require_approval" ? ["sre", "finops_analyst"] : undefined,
    min_approvals: effect === "require_approval" ? 1 : 0,
    require_distinct_approver: false,
    maintenance_windows: rec.maintenance_window ? [rec.maintenance_window] : undefined,
    input_digest: `digest_${recommendationId}`,
    decided_at: "2026-08-31T05:05:00Z",
  };
}

export function buildPolicySimulation(): PolicySimulation {
  resetRng(51001);
  const recs = buildRecommendations().filter((r) => r.status === "open");
  const changes = recs.slice(0, 5).map((r) => ({
    recommendation_id: r.id,
    title: r.title,
    from: (r.auto_executable ? "require_approval" : "auto_execute") as NonNullable<PolicySimulation["changes"]>[number]["from"],
    to: (r.auto_executable ? "auto_execute" : "require_approval") as NonNullable<PolicySimulation["changes"]>[number]["to"],
    monthly_saving: r.estimated_monthly_saving,
  }));
  const autoExecutableSaving = recs.filter((r) => r.auto_executable).reduce((s, r) => s + r.estimated_monthly_saving.amount, 0);
  return {
    policy_name: "balanced (draft)",
    evaluated: recs.length,
    auto_execute: recs.filter((r) => r.auto_executable).length,
    require_approval: recs.filter((r) => !r.auto_executable).length,
    prohibited: 0,
    advisory: 0,
    changes,
    auto_executable_saving: moneyOf(autoExecutableSaving),
    warnings: recs.some((r) => r.risk.level === "HIGH") ? ["3 open recommendations touch a Tier-0 service and will still require two-person approval under any draft."] : [],
  };
}

function contextFor(rec: ReturnType<typeof buildRecommendations>[number]): ApprovalContext {
  return {
    monthly_saving: rec.estimated_monthly_saving,
    annual_saving: rec.estimated_annual_saving,
    confidence: rec.confidence,
    risk_level: rec.risk.level,
    blast_summary: rec.blast_radius.explanation,
    environment: rec.finding.environment,
    affected_resources: [rec.finding.resource_name],
    rollback_plan: rec.reversibility === "none" ? "Not reversible — final snapshot retained 90 days" : `Rollback restores prior configuration (${rec.reversibility})`,
    validation_plan: "72h observation window, CPU/latency/error-rate checks",
    policy_reason: "Production rightsizing requires SRE or FinOps sign-off",
  };
}

export function buildApprovals(): ApprovalRequest[] {
  resetRng(52002);
  const recs = buildRecommendations().filter((r) => r.requires_approval).slice(0, 14);
  return recs.map((rec, i) => {
    const decided = i % 4 === 0;
    const state = decided ? rpick(["approved", "rejected"] as const) : i % 5 === 0 ? "expired" : "pending";
    return {
      id: `appr_${rec.id}`,
      tenant_id: "tn_01hz3k4x8y",
      subject_kind: "recommendation",
      subject_id: rec.id,
      title: rec.title,
      summary: rec.rationale,
      context: contextFor(rec),
      policy_decision_id: `pd_${rec.id}`,
      required_roles: ["sre", "finops_analyst"],
      min_approvals: 1,
      require_distinct_approver: rec.risk.level === "HIGH",
      state,
      responses:
        state === "approved" || state === "rejected"
          ? [{ principal: "sre@acme.io", role: "sre", approved: state === "approved", comment: state === "approved" ? "Confirmed against the runbook, looks safe." : "Needs a maintenance window, not now.", at: "2026-08-29T14:00:00Z" }]
          : [],
      requested_by: "recommendation-engine",
      requested_at: rec.created_at,
      expires_at: new Date(new Date(rec.created_at).getTime() + 7 * 86400000).toISOString(),
      decided_at: state === "approved" || state === "rejected" ? "2026-08-29T14:00:00Z" : undefined,
      execute_after: rec.maintenance_window ? "2026-09-01T02:00:00Z" : undefined,
    };
  });
}

export function getApproval(id: string): ApprovalRequest | undefined {
  return buildApprovals().find((a) => a.id === id);
}
