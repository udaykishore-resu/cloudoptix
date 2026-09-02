"use client";
import * as React from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { Cloud, PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { NAV_ENTRIES } from "@/components/shared/command-palette";
import { cn } from "@/lib/utils";

const SECTIONS: { title: string; items: typeof NAV_ENTRIES }[] = [
  { title: "Intelligence", items: NAV_ENTRIES.slice(0, 5) },
  { title: "Economics & reliability", items: NAV_ENTRIES.slice(5, 7) },
  { title: "Planning", items: NAV_ENTRIES.slice(7, 10) },
  { title: "Copilot", items: NAV_ENTRIES.slice(10, 11) },
  { title: "Operate & govern", items: NAV_ENTRIES.slice(11, 15) },
  { title: "Admin", items: NAV_ENTRIES.slice(15) },
];

export function Sidebar() {
  const pathname = usePathname();
  const [collapsed, setCollapsed] = React.useState(false);

  return (
    <aside
      className={cn(
        "hidden shrink-0 flex-col border-r border-border bg-surface transition-all duration-200 md:flex",
        collapsed ? "w-14" : "w-60"
      )}
      aria-label="Primary navigation"
    >
      <div className="flex h-14 items-center gap-2 border-b border-border px-3">
        <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground">
          <Cloud className="h-4 w-4" />
        </div>
        {!collapsed && <span className="text-sm font-semibold tracking-tight">CloudOptix</span>}
      </div>
      <nav className="flex-1 overflow-y-auto py-2">
        {SECTIONS.map((section) => (
          <div key={section.title} className="mb-3">
            {!collapsed && <div className="px-3 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{section.title}</div>}
            <ul className="space-y-0.5 px-2">
              {section.items.map((item) => {
                const active = item.href === "/" ? pathname === "/" : pathname?.startsWith(item.href);
                return (
                  <li key={item.href}>
                    <Link
                      href={item.href}
                      title={collapsed ? item.label : undefined}
                      className={cn(
                        "focus-ring flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors",
                        active ? "bg-primary/12 text-primary font-medium" : "text-muted-foreground hover:bg-secondary hover:text-foreground"
                      )}
                      aria-current={active ? "page" : undefined}
                    >
                      <item.icon className="h-4 w-4 shrink-0" />
                      {!collapsed && <span className="truncate">{item.label}</span>}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>
      <button
        onClick={() => setCollapsed((v) => !v)}
        className="focus-ring flex items-center gap-2 border-t border-border px-3 py-2.5 text-xs text-muted-foreground hover:bg-secondary hover:text-foreground"
      >
        {collapsed ? <PanelLeftOpen className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
        {!collapsed && "Collapse"}
      </button>
    </aside>
  );
}
