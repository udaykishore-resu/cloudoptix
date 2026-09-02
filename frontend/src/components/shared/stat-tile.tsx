import * as React from "react";
import { ArrowDownRight, ArrowUpRight } from "lucide-react";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export function StatTile({
  label,
  value,
  sub,
  changePct,
  changeGoodDirection = "down",
  icon: Icon,
  className,
  tone,
}: {
  label: string;
  value: React.ReactNode;
  sub?: React.ReactNode;
  changePct?: number;
  /** Whether a decrease or increase in this metric is the "good" direction, for coloring. */
  changeGoodDirection?: "down" | "up";
  icon?: React.ComponentType<{ className?: string }>;
  className?: string;
  tone?: "default" | "success" | "warning" | "destructive";
}) {
  const isGood = changePct != null && (changeGoodDirection === "down" ? changePct < 0 : changePct > 0);
  const isBad = changePct != null && (changeGoodDirection === "down" ? changePct > 0 : changePct < 0);
  return (
    <Card className={cn("p-3.5", tone === "destructive" && "border-destructive/40", tone === "warning" && "border-warning/40", className)}>
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">{label}</span>
        {Icon && <Icon className="h-3.5 w-3.5 text-muted-foreground" />}
      </div>
      <div className="mt-1.5 flex items-baseline gap-2">
        <span className="text-2xl font-semibold tabular-nums tracking-tight">{value}</span>
        {changePct != null && (
          <span className={cn("flex items-center text-xs font-medium tabular-nums", isGood && "text-success", isBad && "text-destructive", !isGood && !isBad && "text-muted-foreground")}>
            {changePct > 0 ? <ArrowUpRight className="h-3 w-3" /> : changePct < 0 ? <ArrowDownRight className="h-3 w-3" /> : null}
            {Math.abs(changePct).toFixed(1)}%
          </span>
        )}
      </div>
      {sub && <p className="mt-1 text-[11px] text-muted-foreground">{sub}</p>}
    </Card>
  );
}
