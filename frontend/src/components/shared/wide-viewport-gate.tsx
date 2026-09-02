"use client";
import * as React from "react";
import { MonitorSmartphone } from "lucide-react";

/** Graph-heavy views (the architecture twin, the radar comparison) are
 * unusable meaningfully compressed. Rather than silently degrade into an
 * unreadable knot of nodes, this says so plainly below the stated
 * breakpoint and still renders the content above it — a deliberate,
 * declared limitation rather than a silent failure. */
export function WideViewportGate({ children, minWidthClass = "min-[900px]:block" }: { children: React.ReactNode; minWidthClass?: string }) {
  return (
    <>
      <div className="flex min-h-[420px] flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-border p-10 text-center min-[900px]:hidden">
        <MonitorSmartphone className="h-8 w-8 text-muted-foreground" />
        <p className="text-sm font-medium">This view needs a wider screen</p>
        <p className="max-w-sm text-xs text-muted-foreground">
          The graph is dense enough that it needs at least ~900px of width to stay legible. Widen your browser window, or view this on a laptop or desktop.
        </p>
      </div>
      <div className={`hidden ${minWidthClass}`}>{children}</div>
    </>
  );
}
