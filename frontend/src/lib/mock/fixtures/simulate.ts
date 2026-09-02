import type {
  Candidate,
  CheckResult,
  CompilationResult,
  Counterfactual,
  Dimension,
  PricedChange,
  RegressionCheck,
  RegressionReport,
  RegressionSuite,
  Scenario,
  Simulation,
  StateProjection,
} from "@/types/domain";
import { getWorld, moneyOf } from "../world";
import { rf, resetRng } from "../rng";

const DIMENSIONS: Dimension[] = ["cost", "performance", "reliability", "scalability", "security", "operational_complexity", "migration_complexity", "risk"];

function scoresFor(base: Partial<Record<Dimension, number>>): Candidate["scores"] {
  return DIMENSIONS.map((d) => ({
    dimension: d,
    score: base[d] ?? 60,
    delta: (base[d] ?? 60) - 60,
    rationale: RATIONALE[d]?.(base[d] ?? 60) ?? "Estimated from comparable migrations.",
    confidence: 0.75 + rf() * 0.2,
  }));
}

const RATIONALE: Partial<Record<Dimension, (s: number) => string>> = {
  cost: (s) => (s > 70 ? "Meaningfully lower run-rate at current traffic." : "Similar or higher run-rate than today."),
  performance: (s) => (s > 65 ? "Lower p99 latency under load-tested traffic." : "Comparable latency profile to the current architecture."),
  reliability: (s) => (s > 65 ? "Removes a single point of failure present today." : "Introduces a new dependency with its own failure modes."),
  scalability: (s) => (s > 65 ? "Scales automatically with no capacity planning." : "Requires manual capacity planning at this growth rate."),
  security: (_s) => "IAM surface area and data-in-transit posture assessed against the current baseline.",
  operational_complexity: (s) => (s > 65 ? "Fewer moving parts to operate day to day." : "Adds an operational surface the team has less experience with."),
  migration_complexity: (s) => (s > 65 ? "Can be rolled out incrementally behind a feature flag." : "Requires a coordinated cutover with a maintenance window."),
  risk: (s) => (s > 65 ? "Well-trodden pattern with strong community track record." : "Newer pattern for this team; higher first-mile risk."),
};

export function buildSimulation(scopeName = "checkout"): Simulation {
  resetRng(71001);
  const w = getWorld();
  const app = w.applications.find((a) => a.name === scopeName) ?? w.applications[0];
  const resources = w.resources.filter((r) => r.application === app.name);
  const baseline = resources.reduce((s, r) => s + r.monthly_cost.amount, 0) || 4200;

  const candidateDefs: Omit<Candidate, "id" | "tenant_id" | "simulation_id" | "scores" | "composite_score" | "recommended">[] = [
    {
      name: "Serverless-first (Lambda + Aurora Serverless v2)",
      summary: "Replace the EC2/ECS fleet with Lambda behind API Gateway and move the primary database to Aurora Serverless v2, scaling to zero in non-peak hours.",
      pattern: "serverless",
      changes: [
        { action: "replace", from: "EC2 Auto Scaling Group", to: "Lambda + API Gateway", component: "Compute", monthly_delta: moneyOf(-baseline * 0.28), rationale: "Pay-per-invocation removes idle capacity cost", effort: "3-4 weeks" },
        { action: "replace", from: "RDS db.r5.xlarge", to: "Aurora Serverless v2 (0.5-8 ACU)", component: "Database", monthly_delta: moneyOf(-baseline * 0.11), rationale: "Scales to near-zero outside business hours", effort: "1-2 weeks" },
        { action: "remove", from: "NAT Gateway", to: undefined, component: "Networking", monthly_delta: moneyOf(-baseline * 0.03), rationale: "VPC-less Lambda removes the NAT dependency for most calls", effort: "included" },
      ],
      current_monthly_cost: moneyOf(baseline),
      projected_monthly_cost: moneyOf(baseline * 0.58),
      monthly_delta: moneyOf(-baseline * 0.42),
      annual_delta: moneyOf(-baseline * 0.42 * 12),
      savings_pct: 42,
      assumptions: [
        { key: "req_rate", label: "Peak requests/sec", value: "340", unit: "req/s", provenance: "INFERRED", sensitivity: 0.6, note: "Derived from 30-day ALB RequestCount" },
        { key: "cold_starts", label: "Acceptable p99 cold-start impact", value: "150", unit: "ms", provenance: "REQUIRES_USER_CONFIRMATION", sensitivity: 0.4 },
      ],
      risks: ["Cold-start latency on the least-frequently-hit endpoints", "Aurora Serverless v2 minimum ACU floor limits savings below a certain traffic level"],
      blockers: [],
      migration_steps: ["Stand up Lambda + API Gateway behind a feature-flagged canary route", "Migrate stateless endpoints first, keep stateful endpoints on ECS", "Cut over the database with a dual-write window", "Decommission the EC2 fleet after a two-week bake"],
      confidence: 0.78,
    },
    {
      name: "Containerize on EKS with Karpenter + Spot",
      summary: "Move the fleet to EKS with Karpenter-driven autoscaling and a Spot-first node group, keeping the current RDS instance.",
      pattern: "containerized",
      changes: [
        { action: "replace", from: "EC2 Auto Scaling Group", to: "EKS + Karpenter (70% Spot)", component: "Compute", monthly_delta: moneyOf(-baseline * 0.22), rationale: "Bin-packing plus Spot pricing on stateless pods", effort: "4-6 weeks" },
        { action: "keep", from: "RDS db.r5.xlarge", to: "RDS db.r5.xlarge", component: "Database", monthly_delta: moneyOf(0), rationale: "No change to the database tier in this candidate" },
      ],
      current_monthly_cost: moneyOf(baseline),
      projected_monthly_cost: moneyOf(baseline * 0.79),
      monthly_delta: moneyOf(-baseline * 0.21),
      annual_delta: moneyOf(-baseline * 0.21 * 12),
      savings_pct: 21,
      assumptions: [{ key: "spot_interruption", label: "Assumed Spot interruption rate", value: "4", unit: "%/week", provenance: "INFERRED", sensitivity: 0.3 }],
      risks: ["Spot interruptions require the workload to tolerate pod eviction gracefully"],
      blockers: [],
      migration_steps: ["Containerize the existing services with minimal code change", "Deploy EKS cluster with Karpenter and a mixed on-demand/Spot node pool", "Shift traffic gradually via weighted target groups"],
      confidence: 0.85,
    },
    {
      name: "Stay on EC2, add Savings Plan + right-size",
      summary: "No architecture change: right-size the current fleet to observed utilisation and cover the resulting steady-state with a 1-year Compute Savings Plan.",
      pattern: "optimize_in_place",
      changes: [
        { action: "resize", from: "m5.2xlarge fleet", to: "m5.xlarge fleet", component: "Compute", monthly_delta: moneyOf(-baseline * 0.14), rationale: "Matches provisioned to p95 observed utilisation", effort: "1 week" },
        { action: "add", from: undefined, to: "1yr No-Upfront Compute Savings Plan", component: "Commitment", monthly_delta: moneyOf(-baseline * 0.09), rationale: "22% effective discount on committed steady-state usage", effort: "included" },
      ],
      current_monthly_cost: moneyOf(baseline),
      projected_monthly_cost: moneyOf(baseline * 0.79),
      monthly_delta: moneyOf(-baseline * 0.21),
      annual_delta: moneyOf(-baseline * 0.21 * 12),
      savings_pct: 21,
      assumptions: [{ key: "commitment_confidence", label: "Confidence steady-state usage holds for 12 months", value: "high", provenance: "CONFIRMED", sensitivity: 0.2 }],
      risks: ["Savings Plan commitment reduces flexibility to scale down further within the term"],
      blockers: [],
      migration_steps: ["Resize during the next maintenance window", "Purchase the Savings Plan sized to the post-resize baseline"],
      confidence: 0.93,
    },
  ];

  const scoreProfiles: Partial<Record<Dimension, number>>[] = [
    { cost: 88, performance: 58, reliability: 60, scalability: 92, security: 70, operational_complexity: 48, migration_complexity: 35, risk: 55 },
    { cost: 68, performance: 74, reliability: 72, scalability: 80, security: 68, operational_complexity: 55, migration_complexity: 50, risk: 62 },
    { cost: 52, performance: 75, reliability: 78, scalability: 55, security: 75, operational_complexity: 90, migration_complexity: 92, risk: 85 },
  ];

  const candidates: Candidate[] = candidateDefs.map((c, i) => ({
    ...c,
    id: `cand_${i + 1}`,
    tenant_id: "tn_01hz3k4x8y",
    simulation_id: "sim_1",
    scores: scoresFor(scoreProfiles[i]),
    composite_score: 0,
    recommended: false,
  }));
  const weights: Simulation["weights"] = { cost: 0.3, reliability: 0.2, performance: 0.15, operational_complexity: 0.12, scalability: 0.1, security: 0.08, migration_complexity: 0.05 };
  candidates.forEach((c) => {
    let sum = 0, total = 0;
    for (const [dim, w2] of Object.entries(weights) as [Dimension, number][]) {
      const s = c.scores.find((x) => x.dimension === dim)?.score ?? 50;
      sum += s * w2;
      total += w2;
    }
    c.composite_score = total > 0 ? sum / total : 0;
  });
  candidates.sort((a, b) => b.composite_score - a.composite_score);
  candidates[0].recommended = true;

  return {
    id: "sim_1",
    tenant_id: "tn_01hz3k4x8y",
    name: `${app.name} architecture options`,
    scope: "application",
    scope_id: app.id,
    kind: "architecture_mutation",
    baseline_cost: moneyOf(baseline),
    candidates,
    weights,
    assumptions: [{ key: "traffic_growth", label: "Assumed traffic growth (12mo)", value: "35", unit: "%", provenance: "INFERRED", sensitivity: 0.5 }],
    requested_by: "priya.nair@acme.io",
    created_at: "2026-08-30T10:00:00Z",
    completed_at: "2026-08-30T10:01:40Z",
    status: "completed",
  };
}

// --- Counterfactual (what-if) -----------------------------------------------
export function buildCounterfactual(scenario: Scenario): Counterfactual {
  resetRng(72001 + JSON.stringify(scenario.parameters ?? {}).length);
  const w = getWorld();
  const baseline = w.resources.filter((r) => r.application === "checkout").reduce((s, r) => s + r.monthly_cost.amount, 0) || 5200;

  const effects: Record<Scenario["type"], { deltaPct: number; perf: string; rel: string; sec?: string; question: string; narrative: string }> = {
    traffic_change: { deltaPct: 0.6, perf: "p99 latency rises ~8% until the ASG scales out", rel: "No change to availability target", question: "What happens if traffic doubles?", narrative: "Compute and NAT egress scale roughly linearly with request volume; the database tier absorbs most of the rest via connection pooling headroom." },
    platform_change: { deltaPct: -0.24, perf: "p99 latency improves ~12% on managed compute", rel: "Fewer patching-related incidents", question: "What if this moved to a managed container platform?", narrative: "Moving from self-managed EC2 to Fargate removes patching and capacity-planning overhead at a modest cost premium per vCPU-hour, offset by tighter bin-packing." },
    database_change: { deltaPct: -0.18, perf: "Read latency improves for read-heavy paths", rel: "Automated failover reduces MTTR", question: "What if this moved to Aurora?", narrative: "Aurora's storage-compute separation and read replica autoscaling reduce both cost and failover time versus self-managed RDS Multi-AZ." },
    add_cache: { deltaPct: -0.15, perf: "p99 latency improves ~30% on cache-hit paths", rel: "Reduces database load at peak", question: "What if we added a cache in front of the database?", narrative: "An ElastiCache layer absorbs the majority of read traffic on hot keys, cutting database CPU and read IOPS meaningfully." },
    remove_nat: { deltaPct: -0.07, perf: "No latency impact for VPC endpoint-served traffic", rel: "Slightly reduces blast radius of a NAT outage", question: "What if we removed the NAT Gateway and added VPC endpoints?", narrative: "S3 and DynamoDB traffic moves to VPC endpoints at flat per-hour pricing, and a single consolidated NAT Gateway handles the remainder." },
    add_vpc_endpoint: { deltaPct: -0.04, perf: "No latency impact", rel: "No change", question: "What if we added a VPC endpoint for S3?", narrative: "S3 data-processing charges through the NAT Gateway are eliminated for in-VPC traffic." },
    spot_adoption: { deltaPct: -0.31, perf: "No latency impact for stateless workloads", rel: "Requires graceful eviction handling", question: "What if we moved stateless compute to Spot?", narrative: "70% Spot adoption on interruption-tolerant pools captures most of the discount while keeping a stable on-demand base." },
    region_change: { deltaPct: 0.05, perf: "Improves latency for EU customers by ~40ms", rel: "Adds a second region to operate", question: "What if we added an eu-west-1 region?", narrative: "A second region adds fixed platform cost and cross-region data transfer, offset by latency improvement for EU traffic." },
    commitment_purchase: { deltaPct: -0.19, perf: "No change", rel: "No change", question: "What if we purchased a 1-year Compute Savings Plan?", narrative: "A Savings Plan sized to the trailing 30-day steady-state usage captures roughly a 19% effective discount." },
    storage_class_change: { deltaPct: -0.09, perf: "Retrieval latency increases for cold objects", rel: "No change", question: "What if we moved cold S3 objects to Glacier Instant Retrieval?", narrative: "Objects untouched for 90+ days move to a cheaper storage class with millisecond retrieval, at a small retrieval-fee tradeoff." },
    replica_change: { deltaPct: -0.12, perf: "Read capacity drops slightly at peak", rel: "Slightly reduces failover redundancy", question: "What if we removed one read replica?", narrative: "The second replica currently serves under 8% of read traffic; removing it recovers most of its cost with a small redundancy tradeoff." },
    custom: { deltaPct: 0, perf: "Unknown", rel: "Unknown", question: "Custom scenario", narrative: "Custom scenario parameters were supplied directly." },
  };
  const e = effects[scenario.type];
  const delta = baseline * e.deltaPct;
  const current: StateProjection = { label: "Current architecture", monthly_cost: moneyOf(baseline), p95_latency_ms: 84, availability: 99.95 };
  const proposed: StateProjection = { label: scenario.label || e.question, monthly_cost: moneyOf(baseline + delta), p95_latency_ms: 84 * (1 + (e.deltaPct > 0 ? 0.08 : -0.05)), availability: 99.95, notes: [e.perf, e.rel] };
  return {
    id: `cf_${Date.now()}`,
    tenant_id: "tn_01hz3k4x8y",
    scenario,
    question: scenario.label || e.question,
    current_state: current,
    proposed_state: proposed,
    cost_delta: moneyOf(delta),
    cost_delta_pct: e.deltaPct * 100,
    annual_cost_delta: moneyOf(delta * 12),
    performance_delta: e.perf,
    reliability_delta: e.rel,
    security_delta: e.sec,
    risk: Math.abs(e.deltaPct) > 0.25 ? "MEDIUM" : "LOW",
    confidence: 0.72 + rf() * 0.18,
    assumptions: [{ key: "current_traffic", label: "Current traffic pattern", value: "as observed, trailing 30 days", provenance: "CONFIRMED" }],
    caveats: ["Modelled from current pricing and observed traffic; a sustained pattern change would shift the estimate."],
    narrative: e.narrative,
    computed_at: new Date().toISOString(),
  };
}

// --- Cost compiler -----------------------------------------------------------
export function buildCompilation(label: string): CompilationResult {
  resetRng(73001 + label.length);
  const changes: PricedChange[] = [
    {
      address: "module.checkout.aws_instance.api[2]",
      resource_type: "aws_instance",
      kind: "aws.ec2.instance",
      action: "create",
      region: "us-east-1",
      before_monthly: moneyOf(0),
      after_monthly: moneyOf(184.32),
      monthly_delta: moneyOf(184.32),
      usage_dependent: false,
      unpriced: false,
      price_components: [{ name: "instance hours", unit: "hr", quantity: 730, unit_price: moneyOf(0.192), monthly: moneyOf(140.16), price_basis: "on_demand" }, { name: "EBS gp3 100GB", unit: "GB-mo", quantity: 100, unit_price: moneyOf(0.44), monthly: moneyOf(44.16), price_basis: "on_demand" }],
    },
    {
      address: "module.checkout.aws_db_instance.replica",
      resource_type: "aws_db_instance",
      kind: "aws.rds.instance",
      action: "create",
      region: "us-east-1",
      before_monthly: moneyOf(0),
      after_monthly: moneyOf(612.5),
      monthly_delta: moneyOf(612.5),
      usage_dependent: false,
      unpriced: false,
      price_components: [{ name: "db.r5.xlarge hours", unit: "hr", quantity: 730, unit_price: moneyOf(0.84), monthly: moneyOf(612.5), price_basis: "on_demand" }],
    },
    {
      address: "module.checkout.aws_lambda_function.webhook",
      resource_type: "aws_lambda_function",
      kind: "aws.lambda.function",
      action: "update",
      region: "us-east-1",
      before_monthly: moneyOf(38.2),
      after_monthly: moneyOf(61.4),
      monthly_delta: moneyOf(23.2),
      usage_dependent: true,
      assumptions: [{ key: "invocations", label: "Monthly invocations", value: "18,000,000", provenance: "INFERRED", note: "Extrapolated from the current function's trailing 30-day invocation count" }],
      unpriced: false,
    },
    {
      address: "module.checkout.aws_nat_gateway.egress2",
      resource_type: "aws_nat_gateway",
      kind: "aws.ec2.nat_gateway",
      action: "create",
      region: "us-east-1",
      before_monthly: moneyOf(0),
      after_monthly: moneyOf(96.5),
      monthly_delta: moneyOf(96.5),
      usage_dependent: true,
      assumptions: [{ key: "egress_gb", label: "Assumed monthly data processed", value: "2,100", unit: "GB", provenance: "REQUIRES_USER_CONFIRMATION" }],
      unpriced: false,
      warnings: ["A second NAT Gateway already exists in this AZ — confirm this is intentional before merging."],
    },
    {
      address: "module.checkout.aws_cloudwatch_log_group.api",
      resource_type: "aws_cloudwatch_log_group",
      kind: "aws.logs.log_group",
      action: "create",
      region: "us-east-1",
      before_monthly: moneyOf(0),
      after_monthly: moneyOf(0),
      monthly_delta: moneyOf(0),
      usage_dependent: true,
      unpriced: true,
      unpriced_reason: "Log ingestion volume for a not-yet-deployed log group cannot be estimated from the plan alone.",
    },
    {
      address: "module.checkout.aws_elasticache_cluster.session",
      resource_type: "aws_elasticache_cluster",
      kind: "aws.elasticache.cluster",
      action: "delete",
      region: "us-east-1",
      before_monthly: moneyOf(210.4),
      after_monthly: moneyOf(0),
      monthly_delta: moneyOf(-210.4),
      usage_dependent: false,
      unpriced: false,
    },
  ];
  const result: CompilationResult = {
    id: `comp_${Date.now()}`,
    tenant_id: "tn_01hz3k4x8y",
    source: "terraform_plan",
    label,
    changes,
    baseline_monthly: moneyOf(0),
    projected_monthly: moneyOf(0),
    monthly_delta: moneyOf(0),
    annual_delta: moneyOf(0),
    delta_pct: 0,
    created_count: changes.filter((c) => c.action === "create").length,
    updated_count: changes.filter((c) => c.action === "update").length,
    deleted_count: changes.filter((c) => c.action === "delete").length,
    unpriced_count: changes.filter((c) => c.unpriced).length,
    coverage: 0,
    confidence: 0,
    assumptions: changes.flatMap((c) => c.assumptions ?? []),
    risks: [
      { code: "redundant_nat", severity: "MEDIUM", address: "module.checkout.aws_nat_gateway.egress2", summary: "New NAT Gateway in an AZ that already has one", monthly_impact: moneyOf(96.5), remediation: "Route through the existing NAT Gateway or add a VPC endpoint instead." },
      { code: "no_lifecycle_policy", severity: "LOW", summary: "New resources are not tagged with a cost-center, which will show up as unattributed spend", remediation: "Add the CostCenter tag before merging." },
    ],
    opportunities: [{ address: "module.checkout.aws_db_instance.replica", summary: "db.r5.large would still clear the projected connection count with 45% headroom", monthly_saving: moneyOf(280), change: "Use db.r5.large instead of db.r5.xlarge" }],
    pricing_date: "2026-08-30T00:00:00Z",
    compiled_at: new Date().toISOString(),
    duration_ms: 640,
  };
  // Summarize
  let baseline = 0, projected = 0, priced = 0, usageDependentAbs = 0;
  for (const c of changes) {
    baseline += c.before_monthly.amount;
    projected += c.after_monthly.amount;
    if (!c.unpriced) priced++;
    if (c.usage_dependent) usageDependentAbs += Math.abs(c.after_monthly.amount);
  }
  result.baseline_monthly = moneyOf(baseline);
  result.projected_monthly = moneyOf(projected);
  result.monthly_delta = moneyOf(projected - baseline);
  result.annual_delta = moneyOf((projected - baseline) * 12);
  result.delta_pct = baseline !== 0 ? ((projected - baseline) / baseline) * 100 : 0;
  result.coverage = changes.length ? priced / changes.length : 1;
  const share = projected !== 0 ? usageDependentAbs / Math.abs(projected) : 0;
  result.confidence = Math.max(0, Math.min(1, result.coverage * (1 - 0.35 * share)));
  return result;
}

// --- Regression ---------------------------------------------------------------
const HISTORY_LABELS = [
  "checkout-service#412",
  "catalog-api#198",
  "fulfillment-worker#87",
  "identity-service#233",
  "notifications#56",
  "checkout-service#398",
];

export interface RegressionHistoryEntry {
  report: RegressionReport;
  label: string;
}

export function buildReportHistory(): RegressionHistoryEntry[] {
  return HISTORY_LABELS.map((label, i) => {
    const compilation = buildCompilation(label);
    const report = buildRegressionReport(compilation, i % 3 === 0 ? "production-strict" : "default");
    report.id = `rr_hist_${i + 1}`;
    report.evaluated_at = new Date(Date.now() - (i + 1) * 5 * 86400000).toISOString();
    return { report, label };
  });
}

export function buildRegressionSuites(): RegressionSuite[] {
  const checks: RegressionCheck[] = [
    { name: "No more than 15% monthly increase", kind: "max_monthly_increase_pct", threshold: 15, on_violation: "FAIL" },
    { name: "No absolute increase over $2,000/mo", kind: "max_monthly_increase_abs", amount: moneyOf(2000), on_violation: "WARNING" },
    { name: "No new NAT Gateways without review", kind: "forbidden_resource", resource_types: ["aws_nat_gateway"], on_violation: "WARNING", message: "New NAT Gateways require architecture review — see #cloud-architecture." },
    { name: "Production resources must carry CostCenter and Application tags", kind: "require_tags", required_tags: ["CostCenter", "Application"], environments: ["production"], on_violation: "FAIL" },
    { name: "At least 80% of changes must be priced", kind: "max_unpriced_ratio", threshold: 0.2, on_violation: "WARNING" },
    { name: "Change must not exhaust the production cost error budget", kind: "budget_headroom", on_violation: "FAIL" },
  ];
  return [
    { id: "suite_default", tenant_id: "tn_01hz3k4x8y", name: "default", version: 3, checks, enabled: true, created_at: "2026-04-01T00:00:00Z" },
    { id: "suite_prod", tenant_id: "tn_01hz3k4x8y", name: "production-strict", version: 2, checks: checks.map((c) => ({ ...c, on_violation: "FAIL" as const })), enabled: true, created_at: "2026-05-10T00:00:00Z" },
  ];
}

export function buildRegressionReport(compilation: CompilationResult, suiteName = "default"): RegressionReport {
  const suite = buildRegressionSuites().find((s) => s.name === suiteName) ?? buildRegressionSuites()[0];
  const results: CheckResult[] = suite.checks.map((c) => {
    let verdict: CheckResult["verdict"] = "PASS";
    let actual = "n/a";
    const expected = String(c.threshold ?? c.amount?.display ?? "n/a");
    switch (c.kind) {
      case "max_monthly_increase_pct":
        actual = compilation.delta_pct.toFixed(1) + "%";
        verdict = compilation.delta_pct > (c.threshold ?? 100) ? c.on_violation : "PASS";
        break;
      case "max_monthly_increase_abs":
        actual = compilation.monthly_delta.display;
        verdict = compilation.monthly_delta.amount > (c.amount?.amount ?? Infinity) ? c.on_violation : "PASS";
        break;
      case "forbidden_resource": {
        const offenders = compilation.changes.filter((ch) => c.resource_types?.includes(ch.resource_type) && ch.action === "create");
        actual = offenders.length ? `${offenders.length} found` : "none found";
        verdict = offenders.length ? c.on_violation : "PASS";
        return { name: c.name, kind: c.kind, verdict, expected: "none", actual, message: c.message ?? "", offenders: offenders.map((o) => o.address) };
      }
      case "require_tags":
        actual = "2 resources missing CostCenter";
        verdict = "WARNING";
        break;
      case "max_unpriced_ratio":
        actual = `${(1 - compilation.coverage).toFixed(2)} unpriced`;
        verdict = 1 - compilation.coverage > (c.threshold ?? 1) ? c.on_violation : "PASS";
        break;
      case "budget_headroom":
        actual = "62% of production budget remaining";
        verdict = "PASS";
        break;
      case "max_cost_per_transaction":
        actual = "n/a for this change set";
        verdict = "PASS";
        break;
    }
    return { name: c.name, kind: c.kind, verdict, expected, actual, message: c.message ?? "" };
  });
  const report: RegressionReport = {
    id: `rr_${Date.now()}`,
    tenant_id: "tn_01hz3k4x8y",
    compilation_id: compilation.id,
    suite_name: suite.name,
    verdict: "PASS",
    results,
    monthly_delta: compilation.monthly_delta,
    annual_delta: compilation.annual_delta,
    summary: "",
    evaluated_at: new Date().toISOString(),
  };
  const rank = { PASS: 0, WARNING: 1, FAIL: 2 } as const;
  let worst: RegressionReport["verdict"] = "PASS";
  for (const r of results) if (rank[r.verdict] > rank[worst]) worst = r.verdict;
  report.verdict = worst;
  const fails = results.filter((r) => r.verdict === "FAIL").length;
  const warns = results.filter((r) => r.verdict === "WARNING").length;
  if (worst === "PASS") report.summary = `All ${results.length} cost checks passed. Monthly impact ${compilation.monthly_delta.display} (${compilation.annual_delta.display}/year).`;
  else if (worst === "WARNING") { report.summary = `${warns} cost check(s) raised a warning. Monthly impact ${compilation.monthly_delta.display} (${compilation.annual_delta.display}/year).`; report.required_action = "Review the flagged items before merging."; }
  else { report.summary = `COST TEST FAILED: ${fails} check(s) failed. Monthly impact ${compilation.monthly_delta.display} (${compilation.annual_delta.display}/year).`; report.required_action = "Architecture review required."; }
  return report;
}

export function buildPrComment(report: RegressionReport, compilation: CompilationResult): string {
  const icon = report.verdict === "PASS" ? "✅" : report.verdict === "WARNING" ? "⚠️" : "❌";
  const lines = [
    `## ${icon} CloudOptix Cost Report — ${report.verdict}`,
    "",
    report.summary,
    "",
    `| | Monthly | Annual |`,
    `|---|---|---|`,
    `| Baseline | ${compilation.baseline_monthly.display} | ${moneyOfAnnual(compilation.baseline_monthly.amount)} |`,
    `| Projected | ${compilation.projected_monthly.display} | ${moneyOfAnnual(compilation.projected_monthly.amount)} |`,
    `| **Delta** | **${compilation.monthly_delta.display}** | **${compilation.annual_delta.display}** |`,
    "",
    `Coverage: ${(compilation.coverage * 100).toFixed(0)}% of changes priced · Confidence: ${(compilation.confidence * 100).toFixed(0)}%`,
    "",
    "### Checks",
    ...report.results.map((r) => `- ${r.verdict === "PASS" ? "✅" : r.verdict === "WARNING" ? "⚠️" : "❌"} **${r.name}** — ${r.actual} (expected ${r.expected})`),
  ];
  if (report.required_action) lines.push("", `**Required action:** ${report.required_action}`);
  return lines.join("\n");
}
function moneyOfAnnual(monthly: number) {
  return moneyOf(monthly * 12).display;
}
