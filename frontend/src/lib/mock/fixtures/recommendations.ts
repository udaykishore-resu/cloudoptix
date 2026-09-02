import type {
  ActionType,
  BlastRadius,
  Complexity,
  ConfidenceInput,
  Evidence,
  Finding,
  Recommendation,
  RecommendationExplanation,
  RecommendationStatus,
  RecommendationSummary,
  Resource,
  Reversibility,
  RiskAssessment,
  RuleInfo,
  StateSnapshot,
} from "@/types/domain";
import { getWorld, moneyOf } from "../world";
import { getDependents } from "./twin";
import { rbool, rf, ri, rpick, resetRng } from "../rng";

interface RuleTemplate {
  ruleId: string;
  ruleName: string;
  category: Recommendation["finding"]["category"];
  action: ActionType;
  kinds: string[];
  titleFor: (r: Resource) => string;
  rationale: string;
  reversibility: Reversibility;
  complexity: Complexity;
  buildStates: (r: Resource) => { current: StateSnapshot; proposed: StateSnapshot; savingFraction: number };
  evidence: (r: Resource) => Evidence[];
}


const TEMPLATES: RuleTemplate[] = [
  {
    ruleId: "rule.ec2.rightsize.cpu_low",
    ruleName: "EC2 CPU-based rightsizing",
    category: "rightsizing",
    action: "resize_instance",
    kinds: ["aws.ec2.instance"],
    titleFor: (r) => `Rightsize ${r.name} — sustained low CPU`,
    rationale: "Fourteen-day CloudWatch history shows this instance running well below the utilisation a smaller instance type would still comfortably serve, at roughly a third of the monthly cost.",
    reversibility: "fast",
    complexity: "low",
    buildStates: (r) => ({
      current: { instance_type: "m5.2xlarge", vcpu: 8, memory_gib: 32, monthly_cost: r.monthly_cost },
      proposed: { instance_type: "m5.large", vcpu: 2, memory_gib: 8, monthly_cost: moneyOf(r.monthly_cost.amount * 0.35) },
      savingFraction: 0.62,
    }),
    evidence: (r) => [
      { kind: "metric", label: "CPU utilization (p95, 14d)", value: `${(r.cpu?.p95 ?? 18).toFixed(1)}%`, source: "cloudwatch", window: { start: "2026-08-17T00:00:00Z", end: "2026-08-31T00:00:00Z" }, percentiles: r.cpu },
      { kind: "metric", label: "Memory utilization (p95, 14d)", value: `${(r.memory?.p95 ?? 24).toFixed(1)}%`, source: "cloudwatch", percentiles: r.memory },
      { kind: "config", label: "Current instance type", value: "m5.2xlarge", source: "discovery" },
    ],
  },
  {
    ruleId: "rule.ec2.idle.stop",
    ruleName: "Idle instance detection",
    category: "waste",
    action: "stop_instance",
    kinds: ["aws.ec2.instance"],
    titleFor: (r) => `Stop idle instance ${r.name}`,
    rationale: "No meaningful CPU, network or disk activity for the trailing 21 days. This looks like an environment left running after a project wound down.",
    reversibility: "instant",
    complexity: "trivial",
    buildStates: (r) => ({
      current: { instance_type: "m5.large", monthly_cost: r.monthly_cost },
      proposed: { instance_type: "m5.large", monthly_cost: moneyOf(r.monthly_cost.amount * 0.08) },
      savingFraction: 0.9,
    }),
    evidence: (r) => [
      { kind: "metric", label: "CPU utilization (p99, 21d)", value: `${(r.cpu?.p99 ?? 4).toFixed(1)}%`, source: "cloudwatch", percentiles: r.cpu },
      { kind: "metric", label: "Network bytes (sum, 21d)", value: "412 KB", source: "cloudwatch" },
      { kind: "history", label: "Last deploy event", value: "97 days ago", source: "discovery" },
    ],
  },
  {
    ruleId: "rule.ebs.unattached.delete",
    ruleName: "Unattached EBS volume",
    category: "waste",
    action: "delete_volume",
    kinds: ["aws.ebs.volume"],
    titleFor: (r) => `Delete unattached volume ${r.name}`,
    rationale: "This volume has had no attached instance for over 30 days. A final snapshot is taken automatically before deletion so the data is recoverable for 90 days.",
    reversibility: "slow",
    complexity: "trivial",
    buildStates: (r) => ({
      current: { volume_type: "gp3", size_gib: 200, monthly_cost: r.monthly_cost },
      proposed: { monthly_cost: moneyOf(0) },
      savingFraction: 1,
    }),
    evidence: (_r) => [
      { kind: "config", label: "Attachment state", value: "detached", source: "discovery" },
      { kind: "history", label: "Days since detach", value: "34", source: "discovery" },
    ],
  },
  {
    ruleId: "rule.ebs.gp2_to_gp3",
    ruleName: "gp2 → gp3 migration",
    category: "storage",
    action: "modify_volume_type",
    kinds: ["aws.ebs.volume"],
    titleFor: (r) => `Migrate ${r.name} from gp2 to gp3`,
    rationale: "gp3 offers the same baseline performance at roughly 20% lower cost per GB, with no downtime for the migration.",
    reversibility: "instant",
    complexity: "trivial",
    buildStates: (r) => ({
      current: { volume_type: "gp2", size_gib: 100, monthly_cost: r.monthly_cost },
      proposed: { volume_type: "gp3", size_gib: 100, monthly_cost: moneyOf(r.monthly_cost.amount * 0.8) },
      savingFraction: 0.2,
    }),
    evidence: (_r) => [{ kind: "config", label: "Current volume type", value: "gp2", source: "discovery" }],
  },
  {
    ruleId: "rule.snapshot.stale.delete",
    ruleName: "Stale snapshot cleanup",
    category: "storage",
    action: "delete_snapshot",
    kinds: ["aws.ebs.snapshot", "aws.rds.snapshot"],
    titleFor: (r) => `Delete stale snapshot ${r.name}`,
    rationale: "Snapshot is older than the tenant's declared retention policy and is not the most recent snapshot of its source volume.",
    reversibility: "none",
    complexity: "trivial",
    buildStates: (r) => ({ current: { monthly_cost: r.monthly_cost }, proposed: { monthly_cost: moneyOf(0) }, savingFraction: 1 }),
    evidence: (_r) => [{ kind: "config", label: "Snapshot age", value: "212 days", source: "discovery" }, { kind: "config", label: "Retention policy", value: "90 days", source: "discovery" }],
  },
  {
    ruleId: "rule.ec2.elastic_ip.unassociated",
    ruleName: "Unassociated Elastic IP",
    category: "waste",
    action: "release_elastic_ip",
    kinds: ["aws.ec2.elastic_ip"],
    titleFor: (r) => `Release unassociated Elastic IP ${r.name}`,
    rationale: "AWS bills for Elastic IPs that are allocated but not associated with a running instance.",
    reversibility: "instant",
    complexity: "trivial",
    buildStates: (r) => ({ current: { monthly_cost: r.monthly_cost }, proposed: { monthly_cost: moneyOf(0) }, savingFraction: 1 }),
    evidence: () => [{ kind: "config", label: "Association state", value: "unassociated", source: "discovery" }],
  },
  {
    ruleId: "rule.rds.rightsize",
    ruleName: "RDS rightsizing",
    category: "rightsizing",
    action: "resize_rds_instance",
    kinds: ["aws.rds.instance"],
    titleFor: (r) => `Rightsize ${r.name}`,
    rationale: "Connection count and CPU are both well below what the current instance class provides, even at peak business hours.",
    reversibility: "slow",
    complexity: "medium",
    buildStates: (r) => ({
      current: { instance_type: "db.r5.2xlarge", vcpu: 8, memory_gib: 64, monthly_cost: r.monthly_cost },
      proposed: { instance_type: "db.r5.large", vcpu: 2, memory_gib: 16, monthly_cost: moneyOf(r.monthly_cost.amount * 0.32) },
      savingFraction: 0.55,
    }),
    evidence: (r) => [
      { kind: "metric", label: "CPU utilization (p95, 14d)", value: `${(r.cpu?.p95 ?? 22).toFixed(1)}%`, source: "cloudwatch", percentiles: r.cpu },
      { kind: "metric", label: "Peak connections", value: "38 of 5000 max", source: "cloudwatch" },
    ],
  },
  {
    ruleId: "rule.rds.replica.remove",
    ruleName: "Underused read replica",
    category: "rightsizing",
    action: "remove_rds_replica",
    kinds: ["aws.rds.instance"],
    titleFor: (r) => `Remove underused replica on ${r.name}`,
    rationale: "Replica lag and read-query volume indicate this replica is not offloading meaningful traffic from the primary.",
    reversibility: "slow",
    complexity: "medium",
    buildStates: (r) => ({ current: { count: 2, monthly_cost: r.monthly_cost }, proposed: { count: 1, monthly_cost: moneyOf(r.monthly_cost.amount * 0.5) }, savingFraction: 0.4 }),
    evidence: () => [{ kind: "metric", label: "Replica read queries/min", value: "3.2", source: "cloudwatch" }],
  },
  {
    ruleId: "rule.lambda.memory.rightsize",
    ruleName: "Lambda memory rightsizing",
    category: "rightsizing",
    action: "resize_lambda_memory",
    kinds: ["aws.lambda.function"],
    titleFor: (r) => `Right-size memory for ${r.name}`,
    rationale: "Billed duration tracks memory allocation almost 1:1 for this function; power-tuning found a lower memory setting with equal or better duration.",
    reversibility: "instant",
    complexity: "trivial",
    buildStates: (r) => ({
      current: { memory_mb: 1024, monthly_cost: r.monthly_cost },
      proposed: { memory_mb: 512, monthly_cost: moneyOf(r.monthly_cost.amount * 0.58) },
      savingFraction: 0.42,
    }),
    evidence: () => [{ kind: "metric", label: "Power-tuning result", value: "512MB minimizes cost at equal duration", source: "cloudwatch" }],
  },
  {
    ruleId: "rule.logs.retention.unset",
    ruleName: "Unbounded log retention",
    category: "observability_cost",
    action: "set_log_retention",
    kinds: ["aws.logs.log_group"],
    titleFor: (r) => `Set retention on ${r.name}`,
    rationale: "Log group has no expiration policy and is retaining data indefinitely at standard storage rates.",
    reversibility: "instant",
    complexity: "trivial",
    buildStates: (r) => ({ current: { monthly_cost: r.monthly_cost }, proposed: { monthly_cost: moneyOf(r.monthly_cost.amount * 0.35) }, savingFraction: 0.65 }),
    evidence: () => [{ kind: "config", label: "Retention setting", value: "Never expire", source: "discovery" }],
  },
  {
    ruleId: "rule.s3.lifecycle.missing",
    ruleName: "Missing S3 lifecycle policy",
    category: "data_lifecycle",
    action: "apply_s3_lifecycle",
    kinds: ["aws.s3.bucket"],
    titleFor: (r) => `Apply lifecycle policy to ${r.name}`,
    rationale: "Object age distribution shows a large share of infrequently-accessed data still on Standard storage; transitioning to Intelligent-Tiering after 30 days recovers most of the difference automatically.",
    reversibility: "fast",
    complexity: "low",
    buildStates: (r) => ({ current: { monthly_cost: r.monthly_cost }, proposed: { monthly_cost: moneyOf(r.monthly_cost.amount * 0.6) }, savingFraction: 0.4 }),
    evidence: () => [{ kind: "config", label: "Objects > 30d unaccessed", value: "71%", source: "discovery" }],
  },
  {
    ruleId: "rule.network.nat.remove",
    ruleName: "Redundant NAT Gateway",
    category: "network",
    action: "remove_nat_gateway",
    kinds: ["aws.ec2.nat_gateway"],
    titleFor: (r) => `Consolidate NAT Gateway ${r.name}`,
    rationale: "Two NAT Gateways in the same VPC show highly correlated, low aggregate throughput. Consolidating to one and adding a VPC endpoint for S3/DynamoDB traffic removes most of the redundant hourly and data-processing charge.",
    reversibility: "none",
    complexity: "medium",
    buildStates: (r) => ({ current: { monthly_cost: r.monthly_cost }, proposed: { monthly_cost: moneyOf(0) }, savingFraction: 1 }),
    evidence: () => [{ kind: "topology", label: "Redundant NAT in same AZ", value: "confirmed", source: "discovery" }],
  },
  {
    ruleId: "rule.ec2.ami.stale",
    ruleName: "Stale AMI",
    category: "storage",
    action: "deregister_ami",
    kinds: ["aws.ec2.image"],
    titleFor: (r) => `Deregister stale AMI ${r.name}`,
    rationale: "AMI is not referenced by any launch template, Auto Scaling Group or running instance, and is older than the tenant's declared AMI retention window.",
    reversibility: "none",
    complexity: "trivial",
    buildStates: (r) => ({ current: { monthly_cost: r.monthly_cost }, proposed: { monthly_cost: moneyOf(0) }, savingFraction: 1 }),
    evidence: () => [{ kind: "topology", label: "Referenced by launch template", value: "none found", source: "discovery" }],
  },
  {
    ruleId: "rule.dynamodb.billing_mode",
    ruleName: "DynamoDB billing mode",
    category: "waste",
    action: "switch_dynamodb_billing_mode",
    kinds: ["aws.dynamodb.table"],
    titleFor: (r) => `Switch ${r.name} to on-demand billing`,
    rationale: "Provisioned capacity is set well above observed peak consumption with a highly variable access pattern — on-demand billing removes the over-provisioning entirely.",
    reversibility: "fast",
    complexity: "low",
    buildStates: (r) => ({ current: { monthly_cost: r.monthly_cost }, proposed: { monthly_cost: moneyOf(r.monthly_cost.amount * 0.68) }, savingFraction: 0.32 }),
    evidence: () => [{ kind: "metric", label: "Provisioned vs consumed RCU", value: "1000 provisioned / 140 avg consumed", source: "cloudwatch" }],
  },
  {
    ruleId: "rule.ecs.rightsize",
    ruleName: "ECS service rightsizing",
    category: "rightsizing",
    action: "adjust_pod_resources",
    kinds: ["aws.ecs.service"],
    titleFor: (r) => `Right-size task resources for ${r.name}`,
    rationale: "Task-level CPU and memory reservations are set roughly 3x observed peak consumption across the service's tasks.",
    reversibility: "fast",
    complexity: "low",
    buildStates: (r) => ({
      current: { vcpu: 2, memory_gib: 4, monthly_cost: r.monthly_cost },
      proposed: { vcpu: 1, memory_gib: 2, monthly_cost: moneyOf(r.monthly_cost.amount * 0.55) },
      savingFraction: 0.45,
    }),
    evidence: (r) => [{ kind: "metric", label: "Task CPU utilization (p95)", value: `${(r.cpu?.p95 ?? 28).toFixed(1)}%`, source: "cloudwatch", percentiles: r.cpu }],
  },
  {
    ruleId: "rule.elasticache.rightsize",
    ruleName: "ElastiCache rightsizing",
    category: "rightsizing",
    action: "resize_instance",
    kinds: ["aws.elasticache.cluster"],
    titleFor: (r) => `Right-size ${r.name}`,
    rationale: "Cache eviction rate is near zero and memory headroom stays above 60% at peak, indicating the node type is oversized for the working set.",
    reversibility: "slow",
    complexity: "medium",
    buildStates: (r) => ({
      current: { instance_type: "cache.r6g.xlarge", monthly_cost: r.monthly_cost },
      proposed: { instance_type: "cache.r6g.large", monthly_cost: moneyOf(r.monthly_cost.amount * 0.52) },
      savingFraction: 0.48,
    }),
    evidence: () => [{ kind: "metric", label: "Memory headroom at peak", value: "63%", source: "cloudwatch" }],
  },
];

function templateFor(kind: string): RuleTemplate | undefined {
  const candidates = TEMPLATES.filter((t) => t.kinds.includes(kind));
  return candidates.length ? rpick(candidates) : undefined;
}

function riskFor(r: Resource, template: RuleTemplate): RiskAssessment {
  const prod = r.environment === "production";
  const critical = r.criticality === "TIER_0" || r.criticality === "TIER_1";
  const base = template.reversibility === "none" ? 0.55 : template.reversibility === "slow" ? 0.35 : 0.15;
  const score = Math.min(1, base + (prod ? 0.15 : 0) + (critical ? 0.12 : 0));
  const level = score > 0.6 ? "HIGH" : score > 0.35 ? "MEDIUM" : score > 0.15 ? "LOW" : "NONE";
  return {
    score,
    level,
    availability_risk: template.action.includes("stop") || template.action.includes("terminate") ? level : "LOW",
    performance_risk: template.action.includes("resize") ? "MEDIUM" : "LOW",
    security_risk: "NONE",
    data_loss_risk: template.reversibility === "none" ? "MEDIUM" : "NONE",
    factors: [
      { name: "Environment", contribution: prod ? 0.3 : 0.05, explanation: prod ? "Production environment" : "Non-production environment" },
      { name: "Reversibility", contribution: base, explanation: `Change is ${template.reversibility === "none" ? "not reversible" : `${template.reversibility} to reverse`}` },
      { name: "Criticality", contribution: critical ? 0.24 : 0.05, explanation: critical ? `${r.criticality} service` : `${r.criticality} service` },
    ],
    mitigations: template.reversibility === "none"
      ? ["A final snapshot/backup is captured automatically before the change executes.", "Change is scheduled inside the declared maintenance window."]
      : ["Rollback restores the prior configuration from the captured snapshot in under two minutes."],
  };
}

function blastFor(r: Resource): BlastRadius {
  const dependents = getDependents(r.id, 3);
  const services = new Set(dependents.map((d) => d.service)).size;
  const critical = dependents.filter((d) => d.criticality === "TIER_0" || d.criticality === "TIER_1").length;
  const score = Math.min(1, dependents.length * 0.05 + critical * 0.08);
  const level = score > 0.6 ? "HIGH" : score > 0.3 ? "MEDIUM" : score > 0.1 ? "LOW" : "NONE";
  return {
    resources_affected: dependents.length,
    services_affected: services,
    critical_services: critical,
    workloads_affected: [...new Set(dependents.map((d) => d.workload).filter(Boolean))] as string[],
    apis_affected: dependents.filter((d) => d.category === "network").length,
    transactions_affected: r.application ? [`${r.application}.primary`] : [],
    estimated_users: r.environment === "production" ? ri(1200, 480000) : 0,
    monthly_revenue_at_risk: r.environment === "production" && r.criticality === "TIER_0" ? moneyOf(ri(8000, 90000)) : moneyOf(0),
    environments_affected: [r.environment],
    cross_account: false,
    score,
    level,
    completeness: 0.94,
    explanation: `Computed by walking ${dependents.length} transitive dependents in the architecture twin (94% of the estate is discovered and topology-mapped).`,
  };
}

function confidenceFor(r: Resource, template: RuleTemplate): { confidence: number; basis: ConfidenceInput[] } {
  const dataQuality = r.cpu ? 0.9 : 0.6;
  const historyLength = 0.85;
  const ruleMaturity = template.category === "waste" ? 0.95 : 0.8;
  const confidence = dataQuality * 0.4 + historyLength * 0.3 + ruleMaturity * 0.3;
  return {
    confidence,
    basis: [
      { name: "Telemetry data quality", value: dataQuality, weight: 0.4, explanation: r.cpu ? "Full-resolution CloudWatch metrics available for the full window" : "Metrics partially estimated" },
      { name: "Observation window length", value: historyLength, weight: 0.3, explanation: "14+ day observation window exceeds the rule's minimum" },
      { name: "Rule track record", value: ruleMaturity, weight: 0.3, explanation: "Calibrated from prior executions of this rule across the tenant base" },
    ],
  };
}

let cache: Recommendation[] | null = null;

export function buildRecommendations(): Recommendation[] {
  if (cache) return cache;
  resetRng(31337);
  const w = getWorld();
  const out: Recommendation[] = [];
  const now = new Date("2026-08-31T05:00:00Z");
  for (const r of w.resources) {
    if (!r.finding_count || !r.potential_saving || r.potential_saving.amount <= 0) continue;
    const template = templateFor(r.kind);
    if (!template) continue;
    const { current, proposed, savingFraction } = template.buildStates(r);
    const saving = moneyOf(r.monthly_cost.amount * savingFraction);
    const { confidence, basis } = confidenceFor(r, template);
    const risk = riskFor(r, template);
    const blast = blastFor(r);
    const finding: Finding = {
      id: `find_${r.id}`,
      tenant_id: r.tenant_id,
      rule_id: template.ruleId,
      rule_name: template.ruleName,
      category: template.category,
      resource_id: r.id,
      resource_name: r.name || r.native_id,
      resource_kind: r.kind,
      account_id: r.account_id,
      region: r.region,
      environment: r.environment,
      severity: saving.amount > 400 ? "HIGH" : saving.amount > 100 ? "MEDIUM" : "LOW",
      summary: template.titleFor(r),
      detail: template.rationale,
      evidence: template.evidence(r),
      current_monthly_cost: r.monthly_cost,
      estimated_monthly_saving: saving,
      detected_at: new Date(now.getTime() - ri(1, 25) * 86400000).toISOString(),
    };
    const autoOk = template.reversibility !== "none" && risk.level !== "HIGH" && confidence > 0.75 && blast.critical_services === 0;
    const requiresApproval = !autoOk;
    const daysAgoCreated = ri(1, 20);
    const status: RecommendationStatus = rbool(0.7) ? "open" : rpick(["under_review", "approved", "executed", "snoozed", "dismissed"] as RecommendationStatus[]);
    const rec: Recommendation = {
      id: `rec_${r.id}`,
      tenant_id: r.tenant_id,
      finding,
      title: finding.summary,
      rationale: template.rationale,
      action: template.action,
      parameters: { resource_id: r.id, ...proposed.attributes },
      current_state: current,
      proposed_state: proposed,
      estimated_monthly_saving: saving,
      estimated_annual_saving: moneyOf(saving.amount * 12),
      implementation_cost: moneyOf(0),
      payback_days: 0,
      confidence,
      confidence_basis: basis,
      risk,
      blast_radius: blast,
      reversibility: template.reversibility,
      complexity: template.complexity,
      priority_score: 0,
      status,
      requires_approval: requiresApproval,
      auto_executable: autoOk,
      narrative: `${template.rationale} Estimated impact: ${saving.display}/mo (${moneyOf(saving.amount * 12).display}/yr), ${(confidence * 100).toFixed(0)}% confidence.`,
      maintenance_window: r.environment === "production" ? "Sun 02:00–04:00 UTC" : undefined,
      created_at: new Date(now.getTime() - daysAgoCreated * 86400000).toISOString(),
      updated_at: new Date(now.getTime() - ri(0, daysAgoCreated) * 86400000).toISOString(),
    };
    out.push(rec);
  }
  // Priority score: saving axis (saturating) * confidence * reversibility / (1+risk) / (1+blast), *100
  const revFactor: Record<string, number> = { instant: 1, fast: 0.8, slow: 0.45, none: 0.15 };
  const cxFactor: Record<string, number> = { trivial: 1, low: 0.85, medium: 0.6, high: 0.35, project: 0.15 };
  out.forEach((r) => {
    const savingsAxis = (2 * (r.estimated_monthly_saving.amount / 5000)) / (1 + r.estimated_monthly_saving.amount / 5000);
    const num = savingsAxis * r.confidence * revFactor[r.reversibility] * cxFactor[r.complexity];
    const den = (1 + r.risk.score) * (1 + r.blast_radius.score) * (r.finding.environment === "production" ? 1.4 : 1);
    r.priority_score = Math.round((100 * num) / den * 10) / 10;
  });
  out.sort((a, b) => b.priority_score - a.priority_score);
  out.forEach((r, i) => (r.rank = i + 1));
  cache = out;
  return out;
}

export function getRecommendation(id: string): Recommendation | undefined {
  return buildRecommendations().find((r) => r.id === id);
}

export function buildSummary(): RecommendationSummary {
  const recs = buildRecommendations().filter((r) => r.status === "open" || r.status === "under_review");
  const byCategory: Record<string, number> = {};
  const savingByCategory: Record<string, ReturnType<typeof moneyOf>> = {};
  const byRisk: Record<string, number> = {};
  let totalSaving = 0;
  let autoExec = 0;
  let awaitingApproval = 0;
  for (const r of recs) {
    byCategory[r.finding.category] = (byCategory[r.finding.category] ?? 0) + 1;
    savingByCategory[r.finding.category] = moneyOf((savingByCategory[r.finding.category]?.amount ?? 0) + r.estimated_monthly_saving.amount);
    byRisk[r.risk.level] = (byRisk[r.risk.level] ?? 0) + 1;
    totalSaving += r.estimated_monthly_saving.amount;
    if (r.auto_executable) autoExec++;
    if (r.requires_approval) awaitingApproval++;
  }
  return {
    open: recs.length,
    total_monthly_saving: moneyOf(totalSaving),
    by_category: byCategory,
    saving_by_category: savingByCategory,
    by_risk: byRisk,
    auto_executable: autoExec,
    awaiting_approval: awaitingApproval,
  };
}

export function buildExplanation(id: string): RecommendationExplanation | undefined {
  const rec = getRecommendation(id);
  if (!rec) return undefined;
  const dependents = getDependents(rec.finding.resource_id, 3);
  return {
    recommendation: rec,
    evidence: rec.finding.evidence,
    confidence_inputs: rec.confidence_basis ?? [],
    risk_factors: rec.risk.factors,
    blast_radius: rec.blast_radius,
    affected_nodes: dependents,
    policy_decision: undefined,
    calibration: {
      rule_id: rec.finding.rule_id,
      tenant_id: rec.tenant_id,
      samples: ri(24, 340),
      success_rate: 0.86 + rf() * 0.1,
      rollback_rate: rf() * 0.04,
      mean_saving_ratio: 0.9 + rf() * 0.2,
      median_saving_ratio: 0.93 + rf() * 0.12,
      confidence_multiplier: 0.95 + rf() * 0.1,
      saving_multiplier: 0.9 + rf() * 0.15,
      updated_at: "2026-08-30T04:00:00Z",
    },
    rollback_summary: rec.reversibility === "none"
      ? "This action cannot be undone through the platform. A final snapshot is captured before execution and retained for 90 days as the only recovery path."
      : `Rollback restores the captured pre-change snapshot (${rec.current_state.instance_type ?? rec.current_state.volume_type ?? "prior configuration"}) with an estimated ${rec.reversibility === "instant" ? "under one minute" : rec.reversibility === "fast" ? "2-5 minute restart" : "15-30 minute restore"} of impact.`,
    narrative: rec.narrative,
    similar_outcomes: Array.from({ length: 4 }).map((_, i) => ({
      id: `oc_${id}_${i}`,
      tenant_id: rec.tenant_id,
      rule_id: rec.finding.rule_id,
      action: rec.action,
      resource_kind: rec.finding.resource_kind,
      environment: rpick(["production", "staging", "development"]),
      predicted_monthly_saving: moneyOf(rec.estimated_monthly_saving.amount * (0.85 + rf() * 0.3)),
      actual_monthly_saving: moneyOf(rec.estimated_monthly_saving.amount * (0.8 + rf() * 0.35)),
      predicted_confidence: rec.confidence,
      predicted_risk: rec.risk.level,
      verdict: rpick(["success", "success", "success", "partial_success"]),
      rolled_back: rbool(0.05),
      performance_impact_pct: rf() * 3 - 1,
      availability_impact_pct: rf() * 0.5,
      saving_ratio: 0.85 + rf() * 0.3,
      observed_at: new Date(Date.now() - ri(5, 120) * 86400000).toISOString(),
    })),
  };
}

const RULE_CATALOG: RuleInfo[] = Array.from(new Map(TEMPLATES.map((t) => [t.ruleId, t])).values()).map((t) => ({
  id: t.ruleId,
  name: t.ruleName,
  category: t.category,
  action: t.action,
  description: t.rationale,
  kinds: t.kinds,
  enabled: true,
  thresholds: {},
  calibration: {
    rule_id: t.ruleId,
    tenant_id: "tn_01hz3k4x8y",
    samples: ri(30, 400),
    success_rate: 0.82 + rf() * 0.15,
    rollback_rate: rf() * 0.05,
    mean_saving_ratio: 0.88 + rf() * 0.2,
    median_saving_ratio: 0.9 + rf() * 0.15,
    confidence_multiplier: 0.9 + rf() * 0.15,
    saving_multiplier: 0.85 + rf() * 0.2,
    updated_at: "2026-08-30T04:00:00Z",
  },
}));

export function buildRuleCatalog(): RuleInfo[] {
  return RULE_CATALOG;
}
