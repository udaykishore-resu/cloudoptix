import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { RiskLevel } from "@/types/api";

const RISK_STYLES: Record<string, string> = {
  NONE: "bg-muted text-muted-foreground border-transparent",
  LOW: "bg-success/15 text-success border-transparent",
  MEDIUM: "bg-warning/15 text-warning border-transparent",
  HIGH: "bg-destructive/15 text-destructive border-transparent",
  CRITICAL: "bg-destructive/25 text-destructive border-transparent font-semibold",
};

export function RiskBadge({ level, className }: { level: RiskLevel | string; className?: string }) {
  return (
    <Badge variant="outline" className={cn(RISK_STYLES[level] ?? RISK_STYLES.NONE, "capitalize", className)}>
      {level.toLowerCase()} risk
    </Badge>
  );
}

const SEVERITY_STYLES: Record<string, string> = {
  INFO: "bg-info/15 text-info border-transparent",
  LOW: "bg-success/15 text-success border-transparent",
  MEDIUM: "bg-warning/15 text-warning border-transparent",
  HIGH: "bg-destructive/15 text-destructive border-transparent",
  CRITICAL: "bg-destructive/25 text-destructive border-transparent font-semibold",
};

export function SeverityBadge({ level, className }: { level: string; className?: string }) {
  return (
    <Badge variant="outline" className={cn(SEVERITY_STYLES[level] ?? SEVERITY_STYLES.INFO, "capitalize", className)}>
      {level.toLowerCase()}
    </Badge>
  );
}
