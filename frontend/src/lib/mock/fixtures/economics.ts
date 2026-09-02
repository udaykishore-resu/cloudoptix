import type {
  BusinessTransaction,
  Footprint,
  FootprintComponent,
} from "@/types/api";
import type { CostSLO, Driver, EconomicErrorBudget, EfficiencyScore, ExecutiveSummary, SavingsFunnel, UnitEconomics } from "@/types/domain";
import { getWorld, moneyOf } from "../world";
import { rf, resetRng } from "../rng";
import { buildRecommendations, buildSummary as buildRecSummary } from "./recommendations";
import { monthlyTotal } from "./costs";

export function buildFootprints(): Footprint[] {
  resetRng(41001);
  const w = getWorld();
  const byApp = new Map<string, typeof w.resources>();
  for (const r of w.resources) {
    const key = r.application ?? "__shared__";
    if (!byApp.has(key)) byApp.set(key, []);
    byApp.get(key)!.push(r);
  }
  const totalCost = monthlyTotal();
  const sharedPool = byApp.get("__shared__") ?? [];
  const sharedTotal = sharedPool.reduce((s, r) => s + r.monthly_cost.amount, 0);

  const footprints: Footprint[] = [];
  for (const app of w.applications) {
    const resources = byApp.get(app.name) ?? [];
    const direct = resources.reduce((s, r) => s + r.monthly_cost.amount, 0);
    const indirectShare = 0.06 + rf() * 0.05;
    const indirect = direct * indirectShare;
    const appShare = totalCost > 0 ? direct / (totalCost - sharedTotal) : 0;
    const shared = sharedTotal * appShare;
    const components: FootprintComponent[] = [
      ...resources.slice(0, 8).map((r) => ({
        resource_id: r.id,
        resource_name: r.name,
        kind: r.kind,
        service: r.kind.split(".")[1],
        class: "direct" as const,
        amount: r.monthly_cost,
        allocation_share: 1,
        basis: "exclusive owner",
        provenance: "CONFIRMED" as const,
      })),
      { class: "indirect", amount: moneyOf(indirect), allocation_share: indirectShare, basis: "measured NAT gateway egress bytes attributed to this application's workloads", provenance: "INFERRED", resource_name: "Shared NAT egress", kind: "aws.ec2.nat_gateway", service: "vpc" },
      { class: "shared", amount: moneyOf(shared), allocation_share: appShare, basis: "pod/compute CPU-request share of shared EKS, MSK and observability platform", provenance: "INFERRED", resource_name: "Shared platform", kind: "platform", service: "platform" },
    ];
    const total = direct + indirect + shared;
    const unattributed = total * 0.03;
    footprints.push({
      id: `fp_${app.id}`,
      tenant_id: "tn_01hz3k4x8y",
      scope: "application",
      scope_id: app.id,
      label: app.name,
      period: { start: "2026-08-01T00:00:00Z", end: "2026-08-31T06:00:00Z" },
      direct: moneyOf(direct),
      indirect: moneyOf(indirect),
      shared: moneyOf(shared),
      total: moneyOf(total),
      unattributed: moneyOf(unattributed),
      coverage: total / (total + unattributed),
      components,
      by_service: undefined,
      by_class: undefined,
      prior_total: moneyOf(total * (0.9 + rf() * 0.15)),
      change_pct: (rf() - 0.4) * 20,
      computed_at: "2026-08-31T05:00:00Z",
      confidence: 0.8 + rf() * 0.15,
    } as unknown as Footprint);
  }
  return footprints.sort((a, b) => b.total!.amount - a.total!.amount);
}

export function getFootprint(scopeId: string): Footprint | undefined {
  return buildFootprints().find((f) => f.scope_id === scopeId);
}

const TX_DEFS: { name: string; app: string; volume: number; desc: string }[] = [
  { name: "checkout", app: "checkout", volume: 2_400_000, desc: "One completed cart-to-order checkout" },
  { name: "catalog_search", app: "catalog", volume: 18_500_000, desc: "One product search query" },
  { name: "login", app: "identity", volume: 9_800_000, desc: "One successful authentication" },
  { name: "shipment_update", app: "fulfillment", volume: 3_100_000, desc: "One shipment status transition" },
  { name: "notification_send", app: "notifications", volume: 42_000_000, desc: "One delivered notification (email, SMS or push)" },
];

export function buildTransactions(): BusinessTransaction[] {
  const w = getWorld();
  return TX_DEFS.map((t) => {
    const app = w.applications.find((a) => a.name === t.app);
    return {
      id: `tx_${t.name}`,
      tenant_id: "tn_01hz3k4x8y",
      name: t.name,
      description: t.desc,
      application_id: app?.id,
      workload_ids: app?.workloadIds ?? [],
      volume_source: { kind: "cloudwatch", namespace: "AWS/ApplicationELB", metric_name: "RequestCount" },
      provenance: "CONFIRMED",
      criticality: app?.criticality ?? "TIER_1",
      created_at: "2026-03-20T00:00:00Z",
      updated_at: "2026-08-30T00:00:00Z",
    } as unknown as BusinessTransaction;
  });
}

export function buildUnitEconomics(): UnitEconomics[] {
  resetRng(42002);
  return TX_DEFS.map((t) => {
    const footprint = buildFootprints().find((f) => f.label === t.app);
    const total = footprint?.total?.amount ?? 5000;
    const costPerUnit = total / t.volume;
    const prior = costPerUnit * (0.92 + rf() * 0.14);
    const changePct = ((costPerUnit - prior) / prior) * 100;
    const drivers: Driver[] = [
      { kind: "volume", label: "Transaction volume", impact: moneyOf(total * 0.4 * (changePct > 0 ? -1 : 1)), impact_share: 0.55, explanation: changePct > 0 ? "Volume held roughly flat while cost rose" : "Volume grew faster than cost, diluting the unit rate" },
      { kind: "unit_cost", label: "Cost per transaction", impact: moneyOf(total * 0.6 * (changePct > 0 ? 1 : -1)), impact_share: 0.45, explanation: changePct > 0 ? "Underlying resource cost per call increased" : "Rightsizing reduced the cost per call" },
    ];
    return {
      id: `ue_${t.name}`,
      tenant_id: "tn_01hz3k4x8y",
      transaction_id: `tx_${t.name}`,
      name: t.name,
      period: { start: "2026-08-01T00:00:00Z", end: "2026-08-31T06:00:00Z" },
      volume: t.volume,
      total_cost: moneyOf(total),
      cost_per_unit: moneyOf(costPerUnit),
      direct_per_unit: moneyOf(costPerUnit * 0.7),
      shared_per_unit: moneyOf(costPerUnit * 0.3),
      prior_cost_per_unit: moneyOf(prior),
      change_pct: changePct,
      drivers,
      confidence: 0.82 + rf() * 0.12,
      volume_provenance: "CONFIRMED",
      computed_at: "2026-08-31T05:00:00Z",
    } as unknown as UnitEconomics;
  });
}

export function buildUnitEconomicsHistory(txId: string): UnitEconomics[] {
  resetRng(43003 + txId.length);
  const current = buildUnitEconomics().find((u) => u.transaction_id === txId);
  if (!current) return [];
  const out: UnitEconomics[] = [];
  let cpu = current.cost_per_unit!.amount * 0.82;
  for (let i = 11; i >= 0; i--) {
    cpu *= 1 + (rf() - 0.42) * 0.06;
    const d = new Date("2026-08-31T00:00:00Z");
    d.setUTCMonth(d.getUTCMonth() - i);
    out.push({
      ...current,
      id: `${current.id}_h${i}`,
      period: { start: d.toISOString(), end: d.toISOString() },
      cost_per_unit: moneyOf(cpu),
      computed_at: d.toISOString(),
    } as unknown as UnitEconomics);
  }
  return out;
}

const SLO_DEFS = [
  { name: "Production infrastructure ceiling", kind: "absolute_spend", target: 210000, scope: "environment", scopeLabel: "production", errorBudgetPct: 0.06 },
  { name: "Checkout cost per transaction", kind: "cost_per_transaction", target: 0.045, scope: "transaction", scopeLabel: "checkout", errorBudgetPct: 0.08, txId: "tx_checkout" },
  { name: "Waste ratio ceiling", kind: "waste_ratio", target: 0.12, scope: "organization", scopeLabel: "organization", errorBudgetPct: 0.1, ratio: true },
  { name: "Cloud Efficiency Score floor", kind: "efficiency_score", target: 78, scope: "organization", scopeLabel: "organization", errorBudgetPct: 0.05, ratio: true, atLeast: true },
];

export function buildCostSLOs(): CostSLO[] {
  return SLO_DEFS.map((d, i) => ({
    id: `slo_${i + 1}`,
    tenant_id: "tn_01hz3k4x8y",
    name: d.name,
    description: `Declared during onboarding governance review`,
    kind: d.kind,
    direction: (d as { atLeast?: boolean }).atLeast ? "at_least" : "at_most",
    scope: d.scope,
    scope_id: (d as { txId?: string }).txId,
    transaction_id: (d as { txId?: string }).txId,
    target: (d as { ratio?: boolean }).ratio ? undefined : moneyOf(d.target),
    target_ratio: (d as { ratio?: boolean }).ratio ? d.target : undefined,
    window: "calendar_month",
    error_budget_pct: d.errorBudgetPct,
    breach_actions: i === 0 ? ["require_approval", "notify"] : ["notify", "generate_recommendations"],
    owner: "finops@acme.io",
    enabled: true,
    created_at: "2026-03-20T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  })) as unknown as CostSLO[];
}

export function buildBudgetStates(): EconomicErrorBudget[] {
  resetRng(44004);
  const total = monthlyTotal();
  const prodActual = total * 0.86;
  const elapsed = 28 / 31;
  return SLO_DEFS.map((d, i) => {
    const target = (d as { ratio?: boolean }).ratio ? d.target : d.target;
    const actualVal = i === 0 ? prodActual : i === 1 ? 0.0468 : i === 2 ? 0.102 : 81.4;
    const budgetAmount = i <= 1 ? target * d.errorBudgetPct : d.errorBudgetPct * 100;
    const proRated = i <= 1 ? target * elapsed : target;
    const overage = Math.max(0, actualVal - proRated);
    const consumedRatio = budgetAmount > 0 ? overage / budgetAmount : 0;
    const state = actualVal > target && !(d as { atLeast?: boolean }).atLeast ? "breached" : consumedRatio >= 1 ? "exhausted" : consumedRatio >= 0.75 ? "at_risk" : consumedRatio >= 0.5 ? "watch" : "healthy";
    const burnRate = consumedRatio / elapsed;
    return {
      id: `eeb_${i + 1}`,
      tenant_id: "tn_01hz3k4x8y",
      slo_id: `slo_${i + 1}`,
      slo_name: d.name,
      kind: d.kind,
      period: { start: "2026-08-01T00:00:00Z", end: "2026-09-01T00:00:00Z" },
      target: i <= 1 ? moneyOf(target) : undefined,
      budget_amount: i <= 1 ? moneyOf(budgetAmount) : undefined,
      actual: i <= 1 ? moneyOf(actualVal) : undefined,
      consumed: i <= 1 ? moneyOf(overage) : undefined,
      remaining: i <= 1 ? moneyOf(Math.max(0, budgetAmount - overage)) : undefined,
      consumed_ratio: consumedRatio,
      burn_rate: burnRate,
      state,
      triggered_actions: state === "at_risk" || state === "exhausted" || state === "breached" ? d.errorBudgetPct ? ["notify", "require_approval"] : [] : [],
      explanation:
        state === "healthy"
          ? `${(elapsed * 100).toFixed(0)}% of the window elapsed, ${(consumedRatio * 100).toFixed(0)}% of budget consumed.`
          : `${(consumedRatio * 100).toFixed(0)}% of budget consumed at ${(elapsed * 100).toFixed(0)}% of the window (burn rate ${burnRate.toFixed(1)}x).`,
      evaluated_at: "2026-08-31T05:00:00Z",
    } as EconomicErrorBudget;
  });
}

export interface SloViolation {
  id: string;
  slo_id: string;
  started_at: string;
  resolved_at?: string;
  peak_consumed_ratio: number;
  actions_triggered: string[];
}

export function buildViolationHistory(sloId: string): SloViolation[] {
  const idx = Number(sloId.split("_")[1] ?? 1);
  resetRng(47000 + idx);
  if (idx === 3) return []; // waste ratio ceiling has stayed healthy all quarter
  const count = idx === 1 ? 2 : 1;
  const out: SloViolation[] = [];
  for (let i = 0; i < count; i++) {
    const daysAgo = 12 + i * 34 + Math.round(rf() * 10);
    const start = new Date(Date.now() - daysAgo * 86400000);
    const durationDays = 2 + Math.round(rf() * 4);
    const resolved = i > 0 || idx !== 1;
    out.push({
      id: `viol_${sloId}_${i + 1}`,
      slo_id: sloId,
      started_at: start.toISOString(),
      resolved_at: resolved ? new Date(start.getTime() + durationDays * 86400000).toISOString() : undefined,
      peak_consumed_ratio: 0.9 + rf() * 0.6,
      actions_triggered: idx === 1 ? ["notify", "require_approval"] : ["notify", "generate_recommendations"],
    });
  }
  return out;
}

export function buildEfficiencyScore(): EfficiencyScore {
  resetRng(45005);
  const factors = [
    { name: "resource_utilization", score: 71, weight: 0.22, detail: "Median compute utilisation is 34% p50 against a 55% target band", opportunity: moneyOf(9200) },
    { name: "waste_elimination", score: 64, weight: 0.2, detail: "23 idle or unattached resources identified across all accounts", opportunity: moneyOf(14300) },
    { name: "commitment_coverage", score: 58, weight: 0.15, detail: "41% of steady-state compute covered by a Savings Plan or Reserved Instance", opportunity: moneyOf(11800) },
    { name: "storage_efficiency", score: 77, weight: 0.1, detail: "gp2 migration and S3 lifecycle coverage both above target", opportunity: moneyOf(2100) },
    { name: "network_efficiency", score: 62, weight: 0.1, detail: "Two NAT Gateways carry redundant, low-utilisation egress", opportunity: moneyOf(3400) },
    { name: "architecture_efficiency", score: 80, weight: 0.1, detail: "Serverless adoption on non-critical paths is ahead of peer estates", opportunity: moneyOf(1600) },
    { name: "automation_maturity", score: 69, weight: 0.07, detail: "38% of open recommendations are policy-eligible for auto-execution", opportunity: moneyOf(0) },
    { name: "governance_maturity", score: 88, weight: 0.06, detail: "Policy and approval coverage is comprehensive across all four accounts", opportunity: moneyOf(0) },
  ];
  const weighted = factors.reduce((s, f) => s + f.score * f.weight, 0) / factors.reduce((s, f) => s + f.weight, 0);
  const grade = weighted >= 90 ? "A" : weighted >= 80 ? "B" : weighted >= 70 ? "C" : weighted >= 60 ? "D" : "F";
  const total = monthlyTotal();
  const waste = total * 0.148;
  return {
    id: "ces_1",
    tenant_id: "tn_01hz3k4x8y",
    scope: "organization",
    label: "Acme Corp — all accounts",
    period: { start: "2026-08-01T00:00:00Z", end: "2026-08-31T06:00:00Z" },
    score: weighted,
    grade,
    factors,
    prior_score: weighted - 2.1,
    delta: 2.1,
    waste_ratio: waste / total,
    total_spend: moneyOf(total),
    identified_waste: moneyOf(waste),
    computed_at: "2026-08-31T05:00:00Z",
  };
}

export function buildSavingsFunnel(): SavingsFunnel {
  resetRng(46006);
  const recs = buildRecommendations();
  const potential = recs.reduce((s, r) => s + r.estimated_monthly_saving.amount, 0);
  const approved = potential * 0.71;
  const planned = approved * 0.88;
  const executed = planned * 0.91;
  const validated = executed * 0.95;
  const realized = validated * 0.93;
  const counts = { potential: recs.length, approved: Math.round(recs.length * 0.71), planned: Math.round(recs.length * 0.63), executed: Math.round(recs.length * 0.57), validated: Math.round(recs.length * 0.54), realized: Math.round(recs.length * 0.5) };
  return {
    tenant_id: "tn_01hz3k4x8y",
    period: { start: "2026-08-01T00:00:00Z", end: "2026-08-31T06:00:00Z" },
    potential_monthly: moneyOf(potential),
    approved_monthly: moneyOf(approved),
    planned_monthly: moneyOf(planned),
    executed_monthly: moneyOf(executed),
    validated_monthly: moneyOf(validated),
    realized_monthly: moneyOf(realized),
    realized_annual: moneyOf(realized * 12),
    counts,
    leakage: [
      { from: "potential", to: "approved", amount: moneyOf(potential - approved), count: counts.potential - counts.approved, conversion_rate: approved / potential, top_reasons: ["Blast radius touches a Tier-0 service", "Awaiting FinOps review", "Snoozed pending Q3 capacity planning"] },
      { from: "approved", to: "planned", amount: moneyOf(approved - planned), count: counts.approved - counts.planned, conversion_rate: planned / approved, top_reasons: ["Maintenance window not yet reached"] },
      { from: "planned", to: "executed", amount: moneyOf(planned - executed), count: counts.planned - counts.executed, conversion_rate: executed / planned, top_reasons: ["Precondition check failed — resource state changed", "Execution role missing an action"] },
      { from: "executed", to: "validated", amount: moneyOf(executed - validated), count: counts.executed - counts.validated, conversion_rate: validated / executed, top_reasons: ["Insufficient telemetry in the observation window"] },
      { from: "validated", to: "realized", amount: moneyOf(validated - realized), count: counts.validated - counts.realized, conversion_rate: realized / validated, top_reasons: ["Realized saving trailed the estimate — usage pattern changed post-change"] },
    ],
    prediction_accuracy: realized / executed,
    computed_at: "2026-08-31T05:00:00Z",
  } as unknown as SavingsFunnel;
}

export function buildExecutiveSummary(): ExecutiveSummary {
  const eff = buildEfficiencyScore();
  const funnel = buildSavingsFunnel();
  const budgets = buildBudgetStates();
  const total = monthlyTotal();
  const recSummary = buildRecSummary();
  const top = buildRecommendations().filter((r) => r.status === "open").slice(0, 6);
  return {
    period: { start: "2026-08-01T00:00:00Z", end: "2026-08-31T06:00:00Z" },
    monthly_spend: moneyOf(total * (28 / 31)),
    forecast_month_end: moneyOf(total * 1.012),
    prior_month_spend: moneyOf(total * 0.94),
    spend_change_pct: 6.4,
    potential_savings: recSummary.total_monthly_saving,
    realized_savings: funnel.realized_monthly,
    realized_annualized: funnel.realized_annual,
    waste_pct: eff.waste_ratio * 100,
    efficiency_score: eff.score,
    efficiency_grade: eff.grade,
    cost_slos_healthy: budgets.filter((b) => b.state === "healthy").length,
    cost_slos_at_risk: budgets.filter((b) => b.state === "watch" || b.state === "at_risk").length,
    cost_slos_breached: budgets.filter((b) => b.state === "breached" || b.state === "exhausted").length,
    budget_states: budgets,
    top_opportunities: top,
    top_transactions: buildUnitEconomics(),
    savings_funnel: funnel,
    generated_at: "2026-08-31T05:30:00Z",
  } as unknown as ExecutiveSummary;
}
