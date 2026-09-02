import type { Citation, CopilotAnswer, ToolResult } from "@/types/api";
import { buildRecommendations, buildSummary } from "./recommendations";
import { monthlyTotal, buildExplanation as buildCostExplanation } from "./costs";
import { buildEfficiencyScore, buildFootprints } from "./economics";

export const SUGGESTED_QUESTIONS = [
  "Why did our AWS bill go up this month?",
  "What are the top 3 things I should do to cut cost this week?",
  "Which recommendations are safe to auto-execute?",
  "What's driving the checkout application's cost?",
  "How is our Cloud Efficiency Score trending?",
  "What's our biggest single source of waste right now?",
];

interface Rule {
  test: RegExp;
  build: () => CopilotAnswer;
}

function toolCall(name: string, result: unknown, ms: number): ToolResult {
  return { tool_call_id: `tc_${name}_${Math.random().toString(36).slice(2, 8)}`, name, result, latency_ms: ms };
}

const RULES: Rule[] = [
  {
    test: /bill.*(up|increase|higher)|why.*cost.*(up|increase)|cost.*change/i,
    build: () => {
      const exp = buildCostExplanation();
      const contributors = exp.contributors ?? [];
      const citations: Citation[] = contributors.slice(0, 3).map((c, i) => ({ kind: "cost_record", id: `contrib_${i}`, label: `${c.key} contribution`, value: c.delta?.display ?? "" }));
      return {
        conversation_id: "conv_copilot",
        answer: `Spend rose ${(exp.delta_pct ?? 0).toFixed(1)}% (${exp.delta?.display ?? ""}) month over month, mostly driven by EC2 scale-out for a product launch, two new RDS read replicas on checkout, and a CloudWatch Logs increase from a debug-logging change left on after a deploy. ${exp.narrative}`,
        citations,
        tool_calls: [toolCall("costs.explain", { current: exp.current_total?.display, baseline: exp.baseline_total?.display }, 210)],
        grounded: true,
        follow_ups: ["Show me the linked executions and PRs behind this", "Which anomaly contributed the most?"],
        data: { contributors },
        latency_ms: 640,
      };
    },
  },
  {
    test: /top.*(3|three).*(thing|action|do)|cut cost|save money|reduce spend/i,
    build: () => {
      const recs = buildRecommendations().filter((r) => r.status === "open").slice(0, 3);
      const citations: Citation[] = recs.map((r) => ({ kind: "recommendation", id: r.id, label: r.title, value: r.estimated_monthly_saving.display }));
      return {
        conversation_id: "conv_copilot",
        answer: `Based on current open recommendations, the three highest-priority actions are:\n\n${recs.map((r, i) => `${i + 1}. **${r.title}** — ${r.estimated_monthly_saving.display}/mo, ${r.risk.level.toLowerCase()} risk, ${(r.confidence * 100).toFixed(0)}% confidence`).join("\n")}\n\nTogether these recover roughly ${recs.reduce((s, r) => s + r.estimated_monthly_saving.amount, 0).toFixed(0)} dollars a month, and all three are reversible.`,
        citations,
        tool_calls: [toolCall("recommendations.list", { count: recs.length, sort: "priority_score" }, 180)],
        grounded: true,
        follow_ups: ["Which of these can auto-execute under our current policy?", "What's the blast radius on the top one?"],
        latency_ms: 520,
      };
    },
  },
  {
    test: /auto.?execute|safe to run|automatic/i,
    build: () => {
      const recs = buildRecommendations().filter((r) => r.auto_executable && r.status === "open").slice(0, 5);
      const citations: Citation[] = recs.map((r) => ({ kind: "recommendation", id: r.id, label: r.title, value: r.estimated_monthly_saving.display }));
      return {
        conversation_id: "conv_copilot",
        answer: `${recs.length} open recommendations are currently policy-eligible for auto-execution — all are reversible, above the 85% confidence threshold, and touch no Tier-0 or Tier-1 service. Together they total ${recs.reduce((s, r) => s + r.estimated_monthly_saving.amount, 0).toFixed(0)} dollars a month.`,
        citations,
        tool_calls: [toolCall("recommendations.list", { filter: "auto_executable=true" }, 160), toolCall("policies.get_active", { policy: "balanced" }, 90)],
        grounded: true,
        follow_ups: ["Show me the policy decision for one of these", "What would change if I switched to the aggressive policy?"],
        latency_ms: 480,
      };
    },
  },
  {
    test: /checkout.*(cost|driving|expensive)/i,
    build: () => {
      const fp = buildFootprints().find((f) => f.label === "checkout");
      const citations: Citation[] = fp ? [{ kind: "resource", id: fp.scope_id, label: "checkout footprint", value: fp.total?.display ?? "" }] : [];
      return {
        conversation_id: "conv_copilot",
        answer: fp
          ? `checkout's total economic footprint is ${fp.total?.display}/mo: ${fp.direct?.display} direct (its own compute and database), ${fp.indirect?.display} indirect (mostly NAT egress it causes), and ${fp.shared?.display} its allocated share of shared platform services. Coverage is ${((fp.coverage ?? 0) * 100).toFixed(0)}%, so ${fp.unattributed?.display} of spend near this application couldn't be confidently attributed.`
          : "I couldn't find a footprint for that application.",
        citations,
        tool_calls: [toolCall("economics.footprint", { scope: "application", scope_id: "checkout" }, 240)],
        grounded: !!fp,
        follow_ups: ["What's checkout's cost per transaction trend?", "What's the biggest indirect cost driver?"],
        latency_ms: 590,
      };
    },
  },
  {
    test: /efficiency score|cloud efficiency/i,
    build: () => {
      const eff = buildEfficiencyScore();
      return {
        conversation_id: "conv_copilot",
        answer: `The Cloud Efficiency Score is currently ${eff.score.toFixed(0)} (grade ${eff.grade}), up ${eff.delta?.toFixed(1)} points from last period. The weakest factor is waste elimination at ${eff.factors.find((f) => f.name === "waste_elimination")?.score}/100 — ${eff.factors.find((f) => f.name === "waste_elimination")?.detail}`,
        citations: [{ kind: "metric", id: "efficiency_score", label: "Cloud Efficiency Score", value: eff.score.toFixed(0) }],
        tool_calls: [toolCall("economics.efficiency_score", { scope: "organization" }, 150)],
        grounded: true,
        follow_ups: ["What would move the score fastest?", "How does this compare to last quarter?"],
        latency_ms: 410,
      };
    },
  },
  {
    test: /waste|biggest.*(source|driver)/i,
    build: () => {
      const recs = buildRecommendations().filter((r) => r.finding.category === "waste" && r.status === "open").sort((a, b) => b.estimated_monthly_saving.amount - a.estimated_monthly_saving.amount);
      const top = recs[0];
      return {
        conversation_id: "conv_copilot",
        answer: top
          ? `The single largest source of identified waste is "${top.title}" on ${top.finding.resource_name}, worth ${top.estimated_monthly_saving.display}/mo. Across all open waste-category recommendations, total identified waste is ${recs.reduce((s, r) => s + r.estimated_monthly_saving.amount, 0).toFixed(0)} dollars a month.`
          : "No open waste-category recommendations right now.",
        citations: top ? [{ kind: "recommendation", id: top.id, label: top.title, value: top.estimated_monthly_saving.display }] : [],
        tool_calls: [toolCall("recommendations.list", { filter: "category=waste" }, 140)],
        grounded: !!top,
        follow_ups: ["Show me all waste recommendations", "How much of this is auto-executable?"],
        latency_ms: 380,
      };
    },
  },
];

export function answer(question: string): CopilotAnswer {
  const rule = RULES.find((r) => r.test.test(question));
  if (rule) return rule.build();
  const summary = buildSummary();
  return {
    conversation_id: "conv_copilot",
    answer: `I wasn't able to ground a confident answer to that in current tenant data — CloudOptix has ${summary.open} open recommendations worth ${summary.total_monthly_saving.display}/mo and total monthly spend around $${Math.round(monthlyTotal()).toLocaleString()}. Try asking about cost changes, top savings opportunities, or a specific application.`,
    citations: [],
    tool_calls: [],
    grounded: false,
    grounding_issues: ["No tool call in the retrieval set matched this question with high confidence."],
    degraded: true,
    follow_ups: SUGGESTED_QUESTIONS.slice(0, 3),
    latency_ms: 220,
  };
}
