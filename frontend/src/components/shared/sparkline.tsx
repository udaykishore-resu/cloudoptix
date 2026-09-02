"use client";
import * as React from "react";
import { Area, AreaChart, ResponsiveContainer, YAxis } from "recharts";
import { cn } from "@/lib/utils";

export interface SparklineProps {
  data: number[];
  className?: string;
  color?: "primary" | "success" | "destructive" | "warning";
  height?: number;
}

const COLOR_VAR: Record<NonNullable<SparklineProps["color"]>, string> = {
  primary: "hsl(var(--primary))",
  success: "hsl(var(--success))",
  destructive: "hsl(var(--destructive))",
  warning: "hsl(var(--warning))",
};

/** A minimal trend indicator for dense table cells and stat tiles — no axes,
 * no tooltip, just shape. Use CostSeries mini-charts elsewhere for anything
 * that needs to be read precisely. */
export function Sparkline({ data, className, color = "primary", height = 28 }: SparklineProps) {
  const id = React.useId();
  if (!data.length) return <div className={cn("text-xs text-muted-foreground", className)}>—</div>;
  const points = data.map((v, i) => ({ i, v }));
  return (
    <div className={cn("w-full", className)} style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={points} margin={{ top: 2, right: 0, bottom: 2, left: 0 }}>
          <defs>
            <linearGradient id={`spark-${id}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={COLOR_VAR[color]} stopOpacity={0.35} />
              <stop offset="100%" stopColor={COLOR_VAR[color]} stopOpacity={0} />
            </linearGradient>
          </defs>
          <YAxis hide domain={["dataMin", "dataMax"]} />
          <Area type="monotone" dataKey="v" stroke={COLOR_VAR[color]} strokeWidth={1.5} fill={`url(#spark-${id})`} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
