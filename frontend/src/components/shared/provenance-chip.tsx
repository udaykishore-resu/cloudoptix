"use client";
import { CheckCircle2, HelpCircle, AlertTriangle, CircleDashed } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger, TooltipProvider } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { Provenance } from "@/types/api";

const CONFIG: Record<string, { label: string; icon: typeof CheckCircle2; className: string }> = {
  CONFIRMED: { label: "Confirmed", icon: CheckCircle2, className: "text-success bg-success/10" },
  INFERRED: { label: "Inferred", icon: CircleDashed, className: "text-info bg-info/10" },
  UNKNOWN: { label: "Unknown", icon: HelpCircle, className: "text-muted-foreground bg-muted" },
  REQUIRES_USER_CONFIRMATION: { label: "Needs confirmation", icon: AlertTriangle, className: "text-warning bg-warning/10" },
};

export interface ProvenanceChipProps {
  provenance: Provenance | string;
  rationale?: string;
  source?: string;
  className?: string;
}

/** One of the four provenance buckets (Confirmed / Inferred / Unknown / Needs
 * confirmation), with the inference rationale surfaced on hover — the core
 * mechanism behind onboarding's "what CloudOptix believes and how" promise. */
export function ProvenanceChip({ provenance, rationale, source, className }: ProvenanceChipProps) {
  const cfg = CONFIG[provenance] ?? CONFIG.UNKNOWN;
  const Icon = cfg.icon;
  const chip = (
    <span className={cn("inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium", cfg.className, (rationale || source) && "cursor-help", className)}>
      <Icon className="h-3 w-3" />
      {cfg.label}
    </span>
  );
  if (!rationale && !source) return chip;
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>{chip}</TooltipTrigger>
        <TooltipContent className="w-64 text-xs">
          {rationale && <p>{rationale}</p>}
          {source && <p className="mt-1 text-muted-foreground">Source: {source}</p>}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
