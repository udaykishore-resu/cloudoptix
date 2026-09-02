"use client";
import { cn } from "@/lib/utils";
import { MoneyDisplay } from "./money-display";
import type { SavingsFunnel } from "@/types/domain";

const STAGES: { key: keyof SavingsFunnel; label: string; description: string }[] = [
  { key: "potential_monthly", label: "Potential", description: "A recommendation exists" },
  { key: "approved_monthly", label: "Approved", description: "A human or policy authorised it" },
  { key: "planned_monthly", label: "Planned", description: "An execution plan with a rollback is scheduled" },
  { key: "executed_monthly", label: "Executed", description: "The change succeeded against AWS" },
  { key: "validated_monthly", label: "Validated", description: "No critical regression in the observation window" },
  { key: "realized_monthly", label: "Realized", description: "Billing data confirms the reduction" },
];

/** The six-stage savings funnel: potential → approved → planned → executed
 * → validated → realized. Every rung is shown, including leakage, because a
 * "potential savings" headline with no funnel behind it is a marketing
 * number, not an operating one. */
export function SavingsFunnelChart({ funnel, className }: { funnel: SavingsFunnel; className?: string }) {
  const max = funnel.potential_monthly.amount || 1;
  return (
    <div className={cn("space-y-2", className)}>
      {STAGES.map((s, i) => {
        const money = funnel[s.key] as SavingsFunnel["potential_monthly"];
        const widthPct = Math.max(4, (money.amount / max) * 100);
        const leak = funnel.leakage[i - 1];
        return (
          <div key={s.key}>
            <div className="flex items-center justify-between text-xs mb-1">
              <span className="font-medium">{s.label}</span>
              <MoneyDisplay money={money} size="sm" />
            </div>
            <div className="h-6 rounded-md bg-muted overflow-hidden">
              <div
                className="h-full rounded-md bg-gradient-to-r from-primary/70 to-primary transition-all"
                style={{ width: `${widthPct}%` }}
              />
            </div>
            {leak && leak.amount.amount > 1 && (
              <div className="mt-1 flex items-center justify-between text-[11px] text-muted-foreground">
                <span>
                  Leakage to {s.label.toLowerCase()}: <MoneyDisplay money={leak.amount} size="sm" muted /> ({Math.round((1 - leak.conversion_rate) * 100)}% lost)
                </span>
                {leak.top_reasons?.[0] && <span className="italic truncate max-w-[50%]">{leak.top_reasons[0]}</span>}
              </div>
            )}
          </div>
        );
      })}
      <div className="flex items-center justify-between border-t border-border pt-2 text-xs text-muted-foreground">
        <span>Prediction accuracy (realized / executed)</span>
        <span className={cn("font-medium tabular-nums", funnel.prediction_accuracy < 0.85 ? "text-warning" : "text-success")}>
          {Math.round(funnel.prediction_accuracy * 100)}%
        </span>
      </div>
    </div>
  );
}
