"use client";
import * as React from "react";
import { Tooltip, TooltipContent, TooltipTrigger, TooltipProvider } from "@/components/ui/tooltip";
import { cn, formatDate } from "@/lib/utils";
import type { Money, Period } from "@/types/api";

export interface MoneyDisplayProps {
  money: Money | undefined;
  period?: Period;
  freshness?: string;
  className?: string;
  size?: "sm" | "md" | "lg" | "xl";
  /** Show the sign explicitly (savings deltas) with green/red coloring. */
  signed?: boolean;
  suffix?: string;
  muted?: boolean;
}

const SIZE_CLASS: Record<NonNullable<MoneyDisplayProps["size"]>, string> = {
  sm: "text-xs",
  md: "text-sm",
  lg: "text-xl font-semibold",
  xl: "text-3xl font-semibold tracking-tight",
};

/**
 * Renders a monetary amount using the platform's own `display` string
 * (never re-derived from the float), and — when a period or freshness is
 * supplied — a tooltip stating exactly what window and how fresh the figure
 * is. Every cost figure in the product should go through this component so
 * a number is never shown without its provenance one hover away.
 */
export function MoneyDisplay({ money, period, freshness, className, size = "md", signed, suffix, muted }: MoneyDisplayProps) {
  if (!money) {
    return <span className={cn("text-muted-foreground", SIZE_CLASS[size], className)}>—</span>;
  }
  const isNeg = money.amount < 0;
  const color = signed ? (isNeg ? "text-success" : money.amount > 0 ? "text-destructive" : "") : muted ? "text-muted-foreground" : "";
  const display = signed && money.amount > 0 ? `+${money.display}` : money.display;
  const hasContext = period || freshness;
  const content = (
    <span className={cn("tabular-nums", SIZE_CLASS[size], color, className)}>
      {display}
      {suffix && <span className="ml-0.5 text-muted-foreground font-normal">{suffix}</span>}
    </span>
  );
  if (!hasContext) return content;
  return (
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="cursor-help underline decoration-dotted decoration-border-strong underline-offset-4">{content}</span>
        </TooltipTrigger>
        <TooltipContent className="text-xs">
          {period && (
            <div>
              Period: {formatDate(period.start)} – {formatDate(period.end)}
            </div>
          )}
          {freshness && <div className="text-muted-foreground">{freshness}</div>}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
