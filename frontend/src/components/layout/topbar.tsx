"use client";
import * as React from "react";
import { Bell, Menu, Search, FlaskConical } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Sheet, SheetContent, SheetTrigger } from "@/components/ui/sheet";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { ThemeToggle } from "./theme-toggle";
import { MobileNav } from "./mobile-nav";
import { isMockMode } from "@/lib/api/config";
import { relativeTime } from "@/lib/utils";

const NOTIFICATIONS = [
  { id: 1, title: "Cost anomaly detected", body: "CloudWatch Logs ingestion is 2.4× baseline for checkout.", at: "2026-08-31T00:00:00Z", tone: "warning" as const },
  { id: 2, title: "Execution plan validated", body: "gp2→gp3 migration for 6 volumes realized $312/mo.", at: "2026-08-30T14:00:00Z", tone: "success" as const },
  { id: 3, title: "Approval requested", body: "Rightsize checkout-order-writer needs SRE sign-off.", at: "2026-08-30T09:00:00Z", tone: "default" as const },
];

export function Topbar({ onOpenPalette }: { onOpenPalette: () => void }) {
  return (
    <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border bg-surface px-3">
      <Sheet>
        <SheetTrigger asChild>
          <Button variant="ghost" size="icon" className="md:hidden" aria-label="Open navigation menu">
            <Menu className="h-4.5 w-4.5" />
          </Button>
        </SheetTrigger>
        <SheetContent side="left" className="w-64 p-0">
          <MobileNav />
        </SheetContent>
      </Sheet>

      <button
        onClick={onOpenPalette}
        className="focus-ring flex h-8 flex-1 max-w-md items-center gap-2 rounded-md border border-border bg-surface-sunken px-2.5 text-xs text-muted-foreground hover:border-border-strong"
      >
        <Search className="h-3.5 w-3.5" />
        <span className="flex-1 text-left">Search pages, resources, recommendations…</span>
        <kbd className="rounded border border-border bg-surface px-1 font-mono text-[10px]">⌘K</kbd>
      </button>

      <div className="ml-auto flex items-center gap-1.5">
        {isMockMode() && (
          <Badge variant="warning" className="hidden items-center gap-1 sm:inline-flex">
            <FlaskConical className="h-3 w-3" /> Mock mode
          </Badge>
        )}
        <Popover>
          <PopoverTrigger asChild>
            <Button variant="ghost" size="icon" aria-label="Notifications" className="relative">
              <Bell className="h-4 w-4" />
              <span className="absolute right-1.5 top-1.5 h-1.5 w-1.5 rounded-full bg-destructive" />
            </Button>
          </PopoverTrigger>
          <PopoverContent align="end" className="w-80 p-0">
            <div className="border-b border-border px-3 py-2 text-xs font-semibold">Notifications</div>
            <ul className="max-h-80 overflow-y-auto">
              {NOTIFICATIONS.map((n) => (
                <li key={n.id} className="border-b border-border px-3 py-2.5 last:border-0 hover:bg-secondary/50">
                  <div className="flex items-start justify-between gap-2">
                    <p className="text-xs font-medium">{n.title}</p>
                    <span className="shrink-0 text-[10px] text-muted-foreground">{relativeTime(n.at)}</span>
                  </div>
                  <p className="mt-0.5 text-[11px] text-muted-foreground">{n.body}</p>
                </li>
              ))}
            </ul>
          </PopoverContent>
        </Popover>
        <ThemeToggle />
        <Avatar className="h-7 w-7">
          <AvatarFallback className="bg-primary/15 text-primary">PN</AvatarFallback>
        </Avatar>
      </div>
    </header>
  );
}
