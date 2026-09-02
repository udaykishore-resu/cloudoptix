import type {
  CostAnomaly,
  CostBreakdown,
  CostExplanation,
  CostForecast,
  CostSeries,
  CostSummary,
} from "@/types/api";
import { getWorld, moneyOf } from "../world";
import { KIND_SERVICE } from "../kinds";
import { rf, resetRng } from "../rng";

const NOW = new Date("2026-08-31T06:00:00Z");

function daysAgo(n: number): Date {
  return new Date(NOW.getTime() - n * 86400000);
}
function isoDay(d: Date): string {
  return d.toISOString().slice(0, 10) + "T00:00:00Z";
}

export function monthlyTotal(): number {
  const w = getWorld();
  return w.resources.reduce((s, r) => s + r.monthly_cost.amount, 0);
}

/** Daily spend series with weekly seasonality, a slow upward trend and two
 * seeded anomaly spikes, built from the world's monthly run-rate so every
 * page's headline total agrees. */
export function buildDailySeries(days: number): { date: Date; amount: number }[] {
  resetRng(77001);
  const monthly = monthlyTotal();
  const dailyBase = monthly / 30.4375;
  const out: { date: Date; amount: number }[] = [];
  for (let i = days - 1; i >= 0; i--) {
    const d = daysAgo(i);
    const dow = d.getUTCDay();
    const weekendFactor = dow === 0 || dow === 6 ? 0.82 : 1.0;
    const trend = 1 + (days - i) * 0.0009; // slow growth toward "today"
    const noise = 1 + (rf() - 0.5) * 0.08;
    let amount = dailyBase * weekendFactor * trend * noise;
    // Two anomaly spikes: a launch-day traffic spike ~18 days ago, and a
    // runaway-logging incident ~6 days ago.
    if (i === 18) amount *= 1.34;
    if (i === 6) amount *= 1.21;
    out.push({ date: d, amount: Math.max(0, amount) });
  }
  return out;
}

export function buildCostSeries(days = 90, groupBy?: string): CostSeries {
  const series = buildDailySeries(days);
  return {
    points: series.map((p) => ({ period: { start: isoDay(p.date), end: isoDay(p.date) }, amount: moneyOf(p.amount) })),
    group_by: groupBy,
    granularity: "daily",
  } as unknown as CostSeries;
}

function groupResources(dimension: "service" | "account" | "region" | "environment" | "application") {
  const w = getWorld();
  const map = new Map<string, { amount: number; count: number; label: string }>();
  for (const r of w.resources) {
    let key: string;
    let label: string;
    switch (dimension) {
      case "service":
        key = KIND_SERVICE(r.kind);
        label = key;
        break;
      case "account":
        key = r.account_id;
        label = w.accounts.find((a) => a.accountId === r.account_id)?.alias ?? r.account_id;
        break;
      case "region":
        key = r.region;
        label = r.region;
        break;
      case "environment":
        key = r.environment;
        label = r.environment;
        break;
      case "application":
        key = r.application ?? "platform";
        label = r.application ?? "Shared platform";
        break;
    }
    const cur = map.get(key) ?? { amount: 0, count: 0, label };
    cur.amount += r.monthly_cost.amount;
    cur.count += 1;
    map.set(key, cur);
  }
  return map;
}

export function buildBreakdown(dimension: "service" | "account" | "region" | "environment" | "application"): CostBreakdown {
  const map = groupResources(dimension);
  const total = [...map.values()].reduce((s, v) => s + v.amount, 0);
  const items = [...map.entries()]
    .map(([key, v]) => ({
      key,
      label: v.label,
      amount: moneyOf(v.amount),
      share: total > 0 ? v.amount / total : 0,
      prior_amount: moneyOf(v.amount * (1 - (rf() - 0.5) * 0.18)),
      change_pct: (rf() - 0.45) * 22,
      resource_count: v.count,
    }))
    .sort((a, b) => b.amount.amount - a.amount.amount);
  return { dimension, items, total: moneyOf(total) } as unknown as CostBreakdown;
}

export function buildForecast(): CostForecast {
  const monthly = monthlyTotal();
  const dayOfMonth = NOW.getUTCDate();
  const daysInMonth = 31;
  const monthToDate = (monthly / daysInMonth) * dayOfMonth;
  const expected = monthly * 1.012;
  return {
    period: { start: "2026-08-01T00:00:00Z", end: "2026-09-01T00:00:00Z" },
    expected: moneyOf(expected),
    low: moneyOf(expected * 0.94),
    high: moneyOf(expected * 1.08),
    method: "holt_winters",
    confidence: 0.87,
    based_on_days: 90,
    note: `Month-to-date spend is ${moneyOf(monthToDate).display}; the model projects month-end from a Holt-Winters fit over the trailing 90 days with weekly seasonality.`,
  } as unknown as CostForecast;
}

export function buildForecastSeries(horizonDays = 30): { points: { date: string; expected: number; low: number; high: number }[] } {
  resetRng(88002);
  const monthly = monthlyTotal();
  const dailyBase = monthly / 30.4375;
  const points = [];
  for (let i = 1; i <= horizonDays; i++) {
    const d = new Date(NOW.getTime() + i * 86400000);
    const dow = d.getUTCDay();
    const weekendFactor = dow === 0 || dow === 6 ? 0.82 : 1.0;
    const expected = dailyBase * weekendFactor * (1 + i * 0.0007);
    const spread = expected * (0.04 + i * 0.0025);
    points.push({ date: isoDay(d), expected, low: expected - spread, high: expected + spread });
  }
  return { points };
}

const ANOMALY_SEEDS = [
  {
    dimension: "service" as const,
    key: "CloudWatch",
    daysAgo: 6,
    expectedMult: 1,
    actualMult: 2.4,
    severity: "HIGH" as const,
    explanation:
      "Log ingestion for the checkout service's cart-api workload jumped after a debug-level logging change shipped in build #4821, quadrupling CloudWatch Logs ingestion volume in us-east-1.",
  },
  {
    dimension: "service" as const,
    key: "EC2",
    daysAgo: 18,
    expectedMult: 1,
    actualMult: 1.6,
    severity: "MEDIUM" as const,
    explanation:
      "A product-launch traffic spike drove the catalog-api Auto Scaling Group to scale out to its maximum instance count for 30 hours in us-west-2.",
  },
  {
    dimension: "account" as const,
    key: "411223344558",
    daysAgo: 2,
    expectedMult: 1,
    actualMult: 1.9,
    severity: "MEDIUM" as const,
    explanation:
      "The dev account's nightly integration-test suite left three RDS instances and an EKS node group running over a holiday weekend instead of scaling to zero.",
  },
];

export function buildAnomalies(): CostAnomaly[] {
  resetRng(99003);
  return ANOMALY_SEEDS.map((seed, i) => {
    const d = daysAgo(seed.daysAgo);
    const baseline = monthlyTotal() / 30.4375 / (seed.dimension === "service" ? 6 : 4);
    const expected = baseline * seed.expectedMult;
    const actual = baseline * seed.actualMult;
    const delta = actual - expected;
    return {
      id: `anom_${1000 + i}`,
      tenant_id: "tn_01hz3k4x8y",
      detected_at: d.toISOString(),
      period: { start: isoDay(d), end: isoDay(daysAgo(seed.daysAgo - 1)) },
      dimension: seed.dimension,
      key: seed.key,
      expected: moneyOf(expected),
      actual: moneyOf(actual),
      delta: moneyOf(delta),
      delta_pct: (delta / expected) * 100,
      score: 2.1 + i * 0.6,
      severity: seed.severity,
      explanation: seed.explanation,
      contributors: [
        { dimension: "region", key: "us-east-1", delta: moneyOf(delta * 0.62), share: 0.62, note: "Primary region for the affected workload" },
        { dimension: "resource_kind", key: seed.dimension === "service" ? "log_group" : "instance", delta: moneyOf(delta * 0.38), share: 0.38, note: "Concentrated in a small number of resources" },
      ],
    };
  }) as unknown as CostAnomaly[];
}

export function buildExplanation(): CostExplanation {
  const current = monthlyTotal();
  const baseline = current * 0.918;
  const delta = current - baseline;
  const contributors = [
    { dimension: "service", key: "EC2", delta: moneyOf(delta * 0.34), share: 0.34, note: "Auto Scaling Group scale-out for the catalog-api launch" },
    { dimension: "service", key: "RDS", delta: moneyOf(delta * 0.21), share: 0.21, note: "Two new read replicas added to checkout's order database" },
    { dimension: "service", key: "CloudWatch", delta: moneyOf(delta * 0.18), share: 0.18, note: "Debug-level log retention left enabled after a deploy" },
    { dimension: "service", key: "S3", delta: moneyOf(delta * 0.11), share: 0.11, note: "Analytics event archive growing faster than its lifecycle policy assumed" },
    { dimension: "service", key: "Lambda", delta: moneyOf(-delta * 0.09), share: -0.09, note: "Notification dispatch cold-start optimisation reduced duration" },
    { dimension: "other", key: "all_other", delta: moneyOf(delta * 0.05), share: 0.05, note: "Distributed across smaller changes" },
  ];
  return {
    current_period: { start: "2026-08-01T00:00:00Z", end: "2026-08-31T06:00:00Z" },
    baseline_period: { start: "2026-07-01T00:00:00Z", end: "2026-07-31T00:00:00Z" },
    current_total: moneyOf(current),
    baseline_total: moneyOf(baseline),
    delta: moneyOf(delta),
    delta_pct: (delta / baseline) * 100,
    contributors,
    narrative: `Spend rose ${((delta / baseline) * 100).toFixed(1)}% month over month, driven mainly by EC2 (catalog-api's launch-day scale-out) and two new RDS read replicas provisioned for checkout. A CloudWatch Logs increase from a debug-logging change and faster-than-planned S3 growth in the analytics pipeline account for most of the rest; a Lambda architecture change partially offset the total.`,
    linked_changes: [
      { kind: "execution", id: "exec_9001", label: "Resized notifications dispatch-worker Lambda to arm64", at: "2026-08-24T14:12:00Z", cost_impact: moneyOf(-410), correlation: 0.71 },
      { kind: "compilation", id: "comp_4471", label: "PR #2281 — add checkout order-writer read replica", at: "2026-08-19T09:44:00Z", cost_impact: moneyOf(1120), correlation: 0.86 },
    ],
  } as unknown as CostExplanation;
}

export function buildCostSummary(): CostSummary {
  const total = monthlyTotal();
  const forecast = buildForecast();
  return {
    period: { start: "2026-08-01T00:00:00Z", end: "2026-08-31T06:00:00Z" },
    total: moneyOf(total * (28 / 31)),
    daily_average: moneyOf(total / 30.4375),
    month_to_date: moneyOf(total * (28 / 31)),
    prior_month: moneyOf(total * 0.94),
    change_pct: 6.4,
    forecast,
    by_service: buildBreakdown("service"),
    by_account: buildBreakdown("account"),
    by_environment: buildBreakdown("environment"),
    by_application: buildBreakdown("application"),
    trend: buildCostSeries(30),
    open_anomalies: buildAnomalies().length,
    last_ingested_at: "2026-08-31T05:10:00Z",
    freshness: "12h Cost & Usage Report lag",
  } as unknown as CostSummary;
}
