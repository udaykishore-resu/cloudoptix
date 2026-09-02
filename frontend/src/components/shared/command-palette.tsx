"use client";
import * as React from "react";
import { useRouter } from "next/navigation";
import {
  LayoutDashboard, TrendingUp, Network, Table2, Lightbulb, Calculator, Gauge, FlaskConical,
  FileCode2, ShieldCheck, Bot, PlayCircle, ClipboardCheck, ScrollText, History, Settings, MessageSquareText, Moon, Sun, Laptop,
} from "lucide-react";
import { useTheme } from "next-themes";
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandSeparator, CommandShortcut } from "@/components/ui/command";

interface NavEntry {
  label: string;
  href: string;
  icon: React.ComponentType<{ className?: string }>;
  keywords?: string;
}

export const NAV_ENTRIES: NavEntry[] = [
  { label: "Overview", href: "/", icon: LayoutDashboard, keywords: "executive dashboard home" },
  { label: "Cost Intelligence", href: "/costs", icon: TrendingUp, keywords: "spend trend forecast anomalies" },
  { label: "Architecture Digital Twin", href: "/architecture", icon: Network, keywords: "graph topology cost flow sankey" },
  { label: "Resource Explorer", href: "/resources", icon: Table2, keywords: "inventory table" },
  { label: "Recommendations", href: "/recommendations", icon: Lightbulb, keywords: "savings opportunities" },
  { label: "Economics", href: "/economics", icon: Calculator, keywords: "footprint unit economics transactions" },
  { label: "Cost SLOs", href: "/slos", icon: Gauge, keywords: "error budget objectives" },
  { label: "Architecture Simulator", href: "/simulator", icon: FlaskConical, keywords: "what-if candidates" },
  { label: "Cost Compiler", href: "/compiler", icon: FileCode2, keywords: "terraform plan pr comment" },
  { label: "Cost Regression", href: "/regression", icon: ShieldCheck, keywords: "suites checks ci" },
  { label: "AI Cost Copilot", href: "/copilot", icon: Bot, keywords: "chat ask" },
  { label: "Automation", href: "/automation", icon: PlayCircle, keywords: "execution plans autonomous" },
  { label: "Approvals", href: "/approvals", icon: ClipboardCheck, keywords: "queue decisions" },
  { label: "Policies", href: "/policies", icon: ScrollText, keywords: "governance yaml" },
  { label: "Audit", href: "/audit", icon: History, keywords: "trail log verify" },
  { label: "Settings", href: "/settings", icon: Settings, keywords: "tenant users accounts" },
];

export interface CommandPaletteProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CommandPalette({ open, onOpenChange: setOpen }: CommandPaletteProps) {
  const router = useRouter();
  const { setTheme } = useTheme();

  React.useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.key === "k" && (e.metaKey || e.ctrlKey)) || e.key === "/") {
        if (e.key === "/" && (e.target as HTMLElement)?.tagName?.match(/INPUT|TEXTAREA/)) return;
        e.preventDefault();
        setOpen(!open);
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [open, setOpen]);

  const go = (href: string) => {
    setOpen(false);
    router.push(href);
  };

  return (
    <CommandDialog open={open} onOpenChange={setOpen} label="Command palette">
      <CommandInput placeholder="Search pages, or type a command…" />
      <CommandList>
        <CommandEmpty>No results found.</CommandEmpty>
        <CommandGroup heading="Navigate">
          {NAV_ENTRIES.map((entry) => (
            <CommandItem key={entry.href} value={`${entry.label} ${entry.keywords ?? ""}`} onSelect={() => go(entry.href)}>
              <entry.icon className="h-4 w-4" />
              {entry.label}
            </CommandItem>
          ))}
        </CommandGroup>
        <CommandSeparator />
        <CommandGroup heading="Ask the copilot">
          <CommandItem value="ask copilot chat question" onSelect={() => go("/copilot")}>
            <MessageSquareText className="h-4 w-4" />
            Ask the AI Cost Copilot a question
          </CommandItem>
        </CommandGroup>
        <CommandSeparator />
        <CommandGroup heading="Theme">
          <CommandItem value="theme light" onSelect={() => { setTheme("light"); setOpen(false); }}>
            <Sun className="h-4 w-4" /> Light
          </CommandItem>
          <CommandItem value="theme dark" onSelect={() => { setTheme("dark"); setOpen(false); }}>
            <Moon className="h-4 w-4" /> Dark
          </CommandItem>
          <CommandItem value="theme system" onSelect={() => { setTheme("system"); setOpen(false); }}>
            <Laptop className="h-4 w-4" /> System
          </CommandItem>
        </CommandGroup>
      </CommandList>
      <div className="flex items-center justify-between border-t border-border px-3 py-2 text-[11px] text-muted-foreground">
        <span>Navigate with ↑↓, select with ↵</span>
        <CommandShortcut>⌘K</CommandShortcut>
      </div>
    </CommandDialog>
  );
}
