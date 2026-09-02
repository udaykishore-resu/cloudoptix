import { Boxes, Layers, ShieldAlert, Users } from "lucide-react";
import { RiskBadge } from "./risk-badge";
import { cn } from "@/lib/utils";
import type { BlastRadius } from "@/types/domain";

export function BlastRadiusSummary({ blast, className, compact }: { blast: BlastRadius; className?: string; compact?: boolean }) {
  const items = [
    { icon: Boxes, label: "Resources", value: blast.resources_affected },
    { icon: Layers, label: "Services", value: blast.services_affected },
    { icon: ShieldAlert, label: "Critical services", value: blast.critical_services, warn: blast.critical_services > 0 },
    { icon: Users, label: "Est. users", value: blast.estimated_users >= 1000 ? `${(blast.estimated_users / 1000).toFixed(1)}K` : blast.estimated_users },
  ];
  return (
    <div className={cn("space-y-2", className)}>
      <div className={cn("grid gap-2", compact ? "grid-cols-4" : "grid-cols-2 sm:grid-cols-4")}>
        {items.map((it) => (
          <div key={it.label} className="rounded-md border border-border bg-surface-sunken p-2">
            <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <it.icon className="h-3 w-3" />
              {it.label}
            </div>
            <div className={cn("mt-0.5 text-sm font-semibold tabular-nums", it.warn && "text-destructive")}>{it.value}</div>
          </div>
        ))}
      </div>
      <div className="flex items-center gap-2">
        <RiskBadge level={blast.level} />
        <span className="text-xs text-muted-foreground">{Math.round(blast.completeness * 100)}% graph coverage</span>
      </div>
      {!compact && <p className="text-xs text-muted-foreground">{blast.explanation}</p>}
    </div>
  );
}
