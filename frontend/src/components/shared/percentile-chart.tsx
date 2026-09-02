import { cn } from "@/lib/utils";
import type { Percentiles } from "@/types/api";

/** Renders a p50/p90/p95/p99/max distribution as a compact horizontal bar
 * with marked percentile ticks — used for CPU/memory utilisation wherever a
 * single "average" figure would hide the tail that actually matters for
 * rightsizing decisions. */
export function PercentileChart({ percentiles, label, unit = "%", className }: { percentiles: Percentiles | undefined; label?: string; unit?: string; className?: string }) {
  if (!percentiles || percentiles.p50 == null) {
    return <div className={cn("text-xs text-muted-foreground", className)}>No data</div>;
  }
  const max = percentiles.max ?? percentiles.p99 ?? 100;
  const marks: { key: keyof Percentiles; label: string }[] = [
    { key: "p50", label: "p50" },
    { key: "p90", label: "p90" },
    { key: "p95", label: "p95" },
    { key: "p99", label: "p99" },
  ];
  return (
    <div className={cn("space-y-1", className)}>
      {label && <div className="flex items-center justify-between text-xs"><span className="text-muted-foreground">{label}</span><span className="tabular-nums font-medium">{percentiles.p50?.toFixed(1)}{unit} p50</span></div>}
      <div className="relative h-2.5 rounded-full bg-muted">
        <div className="absolute inset-y-0 left-0 rounded-full bg-primary/60" style={{ width: `${Math.min(100, ((percentiles.p50 ?? 0) / max) * 100)}%` }} />
        {marks.map((m) => {
          const v = percentiles[m.key];
          if (v == null) return null;
          return (
            <div key={m.key} className="absolute top-0 h-2.5 w-px bg-foreground/40" style={{ left: `${Math.min(100, (v / max) * 100)}%` }} title={`${m.label}: ${v.toFixed(1)}${unit}`} />
          );
        })}
      </div>
      <div className="flex justify-between text-[10px] text-muted-foreground tabular-nums">
        <span>p50 {percentiles.p50?.toFixed(0)}{unit}</span>
        <span>p90 {percentiles.p90?.toFixed(0)}{unit}</span>
        <span>p99 {percentiles.p99?.toFixed(0)}{unit}</span>
        <span>max {percentiles.max?.toFixed(0)}{unit}</span>
      </div>
    </div>
  );
}
