import { Cloud } from "lucide-react";

export function Logo({ className }: { className?: string }) {
  return (
    <div className={className ? className : "flex items-center gap-2"}>
      <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground">
        <Cloud className="h-4 w-4" />
      </div>
      <span className="text-sm font-semibold tracking-tight">CloudOptix</span>
    </div>
  );
}
