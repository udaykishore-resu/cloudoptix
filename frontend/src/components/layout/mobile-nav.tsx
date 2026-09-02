"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { Cloud } from "lucide-react";
import { NAV_ENTRIES } from "@/components/shared/command-palette";
import { cn } from "@/lib/utils";

export function MobileNav() {
  const pathname = usePathname();
  return (
    <div className="flex h-full flex-col">
      <div className="flex h-14 items-center gap-2 border-b border-border px-4">
        <div className="flex h-7 w-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
          <Cloud className="h-4 w-4" />
        </div>
        <span className="text-sm font-semibold">CloudOptix</span>
      </div>
      <nav className="flex-1 overflow-y-auto p-2">
        <ul className="space-y-0.5">
          {NAV_ENTRIES.map((item) => {
            const active = item.href === "/" ? pathname === "/" : pathname?.startsWith(item.href);
            return (
              <li key={item.href}>
                <Link
                  href={item.href}
                  className={cn(
                    "focus-ring flex items-center gap-2.5 rounded-md px-3 py-2 text-sm",
                    active ? "bg-primary/12 text-primary font-medium" : "text-muted-foreground hover:bg-secondary hover:text-foreground"
                  )}
                >
                  <item.icon className="h-4 w-4" />
                  {item.label}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>
    </div>
  );
}
