"use client";
import { Tooltip, TooltipContent, TooltipTrigger, TooltipProvider } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { ConfidenceInput } from "@/types/domain";

export interface ConfidenceBadgeProps {
  confidence: number;
  basis?: ConfidenceInput[];
  className?: string;
  size?: "sm" | "md";
}

function tone(c: number): { label: string; className: string } {
  if (c >= 0.85) return { label: "High", className: "bg-success/15 text-success" };
  if (c >= 0.65) return { label: "Medium", className: "bg-warning/15 text-warning" };
  return { label: "Low", className: "bg-destructive/15 text-destructive" };
}

/** A confidence percentage with, on hover, the weighted inputs that produced
 * it — so "87% confident" is always explainable, never asserted. */
export function ConfidenceBadge({ confidence, basis, className, size = "md" }: ConfidenceBadgeProps) {
  const t = tone(confidence);
  const pct = Math.round(confidence * 100);
  const badge = (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md px-1.5 py-0.5 font-medium tabular-nums",
        t.className,
        size === "sm" ? "text-[11px]" : "text-xs",
        basis?.length && "cursor-help",
        className
      )}
    >
      <span className={cn("h-1.5 w-1.5 rounded-full", confidence >= 0.85 ? "bg-success" : confidence >= 0.65 ? "bg-warning" : "bg-destructive")} />
      {pct}% confidence
    </span>
  );
  if (!basis?.length) return badge;
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>{badge}</TooltipTrigger>
        <TooltipContent className="w-64">
          <div className="mb-1.5 font-medium text-foreground">Confidence inputs</div>
          <ul className="space-y-1.5">
            {basis.map((b) => (
              <li key={b.name} className="flex items-start justify-between gap-2">
                <span className="text-muted-foreground">{b.explanation}</span>
                <span className="shrink-0 tabular-nums font-medium">{Math.round(b.value * 100)}%</span>
              </li>
            ))}
          </ul>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
