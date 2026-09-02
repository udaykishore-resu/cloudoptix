import type { AutonomousRunResult, ExecutionPlan, LearningResult, PlanState, PlanValidationResult, RollbackPlan, Step, ValidationPlan } from "@/types/domain";
import { moneyOf } from "../world";
import { buildRecommendations } from "./recommendations";
import { ri, rpick, rbool, resetRng, rf } from "../rng";

function stepsFor(rec: ReturnType<typeof buildRecommendations>[number], state: PlanState): Step[] {
  const allDone = ["executed", "validating", "validated", "rolled_back"].includes(state);
  const inProgress = state === "executing";
  const base: Omit<Step, "state" | "started_at" | "finished_at" | "attempts">[] = [
    { id: "st1", ordinal: 1, kind: "precondition", name: "Verify current state", describe: `Confirm ${rec.finding.resource_name} configuration matches the snapshot taken at analysis time`, aws_action: "ec2:DescribeInstances", target: rec.finding.resource_id, idempotency_key: `${rec.id}-pre`, max_retries: 2, abort_on_failure: true },
    { id: "st2", ordinal: 2, kind: "snapshot", name: "Capture rollback snapshot", describe: "Record every attribute this change will modify", aws_action: "ec2:CreateSnapshot", target: rec.finding.resource_id, idempotency_key: `${rec.id}-snap`, max_retries: 1, abort_on_failure: true },
    { id: "st3", ordinal: 3, kind: "mutate", name: rec.title, describe: rec.rationale, aws_action: actionApi(rec.action), target: rec.finding.resource_id, idempotency_key: `${rec.id}-mut`, max_retries: 3, abort_on_failure: true },
    { id: "st4", ordinal: 4, kind: "wait", name: "Settle", describe: "Wait for the resource to reach a stable state", idempotency_key: `${rec.id}-wait`, max_retries: 0, abort_on_failure: false },
    { id: "st5", ordinal: 5, kind: "verify", name: "Verify change took effect", describe: "Confirm the resource now reports the proposed configuration", aws_action: "ec2:DescribeInstances", target: rec.finding.resource_id, idempotency_key: `${rec.id}-verify`, max_retries: 2, abort_on_failure: false },
  ];
  return base.map((s, i) => {
    const done = allDone || (inProgress && i < 3);
    const running = inProgress && i === 3;
    return {
      ...s,
      state: done ? "succeeded" : running ? "running" : "pending",
      attempts: done || running ? 1 : 0,
      started_at: done || running ? "2026-08-31T02:00:00Z" : undefined,
      finished_at: done ? "2026-08-31T02:0" + (2 + i) + ":00Z" : undefined,
    } as Step;
  });
}

function actionApi(action: string): string {
  const map: Record<string, string> = {
    resize_instance: "ec2:ModifyInstanceAttribute",
    stop_instance: "ec2:StopInstances",
    terminate_instance: "ec2:TerminateInstances",
    delete_volume: "ec2:DeleteVolume",
    modify_volume_type: "ec2:ModifyVolume",
    delete_snapshot: "ec2:DeleteSnapshot",
    deregister_ami: "ec2:DeregisterImage",
    release_elastic_ip: "ec2:ReleaseAddress",
    resize_rds_instance: "rds:ModifyDBInstance",
    remove_rds_replica: "rds:DeleteDBInstance",
    apply_s3_lifecycle: "s3:PutLifecycleConfiguration",
    set_log_retention: "logs:PutRetentionPolicy",
    resize_lambda_memory: "lambda:UpdateFunctionConfiguration",
    remove_nat_gateway: "ec2:DeleteNatGateway",
    switch_dynamodb_billing_mode: "dynamodb:UpdateTable",
    adjust_pod_resources: "ecs:UpdateService",
  };
  return map[action] ?? "unknown:Action";
}

function rollbackFor(rec: ReturnType<typeof buildRecommendations>[number], planId: string): RollbackPlan {
  const feasible = rec.reversibility !== "none";
  return {
    id: `rb_${planId}`,
    tenant_id: "tn_01hz3k4x8y",
    plan_id: planId,
    steps: feasible
      ? [{ id: "rb1", ordinal: 1, kind: "mutate", name: "Restore prior configuration", describe: "Apply the captured pre-change snapshot", idempotency_key: `${planId}-rb`, state: "pending", attempts: 0, max_retries: 2, abort_on_failure: true }]
      : [],
    feasible,
    infeasible_reason: feasible ? undefined : "This action deletes state with no equivalent restore path; only a 90-day-retained snapshot is recoverable.",
    estimated_duration: feasible ? (rec.reversibility === "instant" ? 60_000_000_000 : rec.reversibility === "fast" ? 300_000_000_000 : 1_800_000_000_000) : 0,
    data_loss_risk: rec.risk.data_loss_risk,
    summary: feasible ? `Restores ${rec.current_state.instance_type ?? rec.current_state.volume_type ?? "the prior configuration"} from the captured snapshot.` : "No rollback is possible for this action.",
    created_at: rec.created_at,
  };
}

function validationPlanFor(): ValidationPlan {
  return {
    observation_window: 259_200_000_000_000,
    baseline_window: 604_800_000_000_000,
    checks: [
      { name: "CPU utilization", metric: "cpu_utilization", statistic: "p95", comparison: "no_worse_than_pct", threshold: 15, critical: false, reason: "Rightsizing should not push sustained CPU meaningfully higher" },
      { name: "P99 latency", metric: "p99_latency_ms", statistic: "p99", comparison: "no_worse_than_pct", threshold: 10, critical: true, reason: "Customer-facing latency must not regress" },
      { name: "Error rate", metric: "error_rate", statistic: "avg", comparison: "below_absolute", threshold: 1.0, critical: true, reason: "Error rate must stay under 1%" },
      { name: "Monthly cost", metric: "monthly_cost", statistic: "sum", comparison: "improved_by_pct", threshold: 5, critical: false, reason: "Confirms the saving actually landed on the bill" },
    ],
    auto_rollback_on: ["P99 latency", "Error rate"],
    min_samples: 500,
  };
}

let cache: ExecutionPlan[] | null = null;

export function buildExecutionPlans(): ExecutionPlan[] {
  if (cache) return cache;
  resetRng(61001);
  const recs = buildRecommendations().filter((r) => ["approved", "executing", "executed", "validating", "validated", "scheduled"].includes(r.status));
  const states: PlanState[] = ["approved", "scheduled", "executing", "executed", "validating", "validated"];
  cache = recs.map((rec, i) => {
    const state = rpick(states);
    const steps = stepsFor(rec, state);
    return {
      id: `plan_${rec.id}`,
      tenant_id: "tn_01hz3k4x8y",
      recommendation_id: rec.id,
      action: rec.action,
      title: rec.title,
      account_id: rec.finding.account_id,
      region: rec.finding.region,
      environment: rec.finding.environment,
      resource_ids: [rec.finding.resource_id],
      steps,
      rollback: rollbackFor(rec, `plan_${rec.id}`),
      validation: validationPlanFor(),
      expected_monthly_saving: rec.estimated_monthly_saving,
      baseline_monthly_cost: rec.current_state.monthly_cost,
      state,
      state_reason: state === "scheduled" ? `Awaiting maintenance window ${rec.maintenance_window ?? ""}` : undefined,
      approval_id: rec.requires_approval ? `appr_${rec.id}` : undefined,
      policy_decision_id: `pd_${rec.id}`,
      scheduled_for: state === "scheduled" ? "2026-09-07T02:00:00Z" : undefined,
      dry_run: false,
      requested_by: i % 3 === 0 ? "automation-engine" : "priya.nair@acme.io",
      created_at: rec.created_at,
      started_at: ["executing", "executed", "validating", "validated"].includes(state) ? "2026-08-31T02:00:00Z" : undefined,
      finished_at: ["executed", "validating", "validated"].includes(state) ? "2026-08-31T02:04:00Z" : undefined,
    };
  });
  return cache;
}

export function getExecutionPlan(id: string): ExecutionPlan | undefined {
  return buildExecutionPlans().find((p) => p.id === id);
}

export function buildValidationResult(planId: string): PlanValidationResult | undefined {
  const plan = getExecutionPlan(planId);
  if (!plan || !["validating", "validated"].includes(plan.state)) return undefined;
  resetRng(62002 + planId.length);
  const checks = plan.validation.checks.map((c) => {
    const passed = rbool(0.85);
    return {
      name: c.name,
      metric: c.metric,
      baseline: 100,
      observed: passed ? 96 + rf() * 6 : 78 + rf() * 8,
      threshold: c.threshold,
      change_pct: passed ? -2 - rf() * 3 : 12 + rf() * 8,
      passed,
      critical: c.critical,
      samples: ri(600, 4000),
      detail: passed ? "Within tolerance across the full observation window." : "Exceeded the declared threshold during the observation window.",
    };
  });
  const allPass = checks.every((c) => c.passed);
  return {
    id: `val_${planId}`,
    tenant_id: "tn_01hz3k4x8y",
    plan_id: planId,
    verdict: allPass ? "success" : "partial_success",
    explanation: allPass ? "All validation checks passed and the change is holding." : "Non-critical checks regressed slightly; no critical check failed.",
    baseline_window: { start: "2026-08-23T00:00:00Z", end: "2026-08-30T00:00:00Z" },
    observed_window: { start: "2026-08-31T02:10:00Z", end: "2026-09-03T02:10:00Z" },
    checks,
    predicted_monthly_saving: plan.expected_monthly_saving,
    observed_monthly_saving: moneyOf(plan.expected_monthly_saving.amount * (0.88 + rf() * 0.2)),
    saving_accuracy: 0.88 + rf() * 0.2,
    rollback_triggered: false,
    evaluated_at: "2026-09-03T02:15:00Z",
  };
}

export function buildAutonomousHistory(): AutonomousRunResult[] {
  resetRng(63003);
  return Array.from({ length: 8 }).map(() => ({
    considered: ri(30, 60),
    planned: ri(10, 25),
    executed: ri(8, 22),
    skipped: ri(2, 8),
    failed: ri(0, 2),
    rolled_back: ri(0, 1),
    monthly_saving: moneyOf(ri(1200, 6400)),
    skip_reasons: { "blast radius too large": ri(1, 4), "confidence below threshold": ri(0, 3), "outside maintenance window": ri(0, 2) },
    duration_ms: ri(4000, 22000),
  }));
}

export function buildLearningResult(): LearningResult {
  resetRng(64004);
  return {
    outcomes_considered: 214,
    rules_calibrated: 12,
    mean_accuracy: 0.91,
    calibrations: {},
  };
}
