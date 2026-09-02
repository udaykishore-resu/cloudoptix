import {
  Server, HardDrive, Camera, Globe, Layers, Boxes, Database, Table, Archive, Zap,
  Container, Ship, Waypoints, Cloud, Router, Network, Gauge, GitBranch, ListOrdered,
  Radio, Waves, ScrollText, KeyRound, Lock, Box,
} from "lucide-react";
import { KIND_ICON } from "@/lib/mock/kinds";
import { cn } from "@/lib/utils";

const ICONS: Record<string, typeof Server> = {
  Server, HardDrive, Camera, Globe, Layers, Boxes, Database, Table, Archive, Zap,
  Container, Ship, Waypoints, Cloud, Router, Network, Gauge, GitBranch, ListOrdered,
  Radio, Waves, ScrollText, KeyRound, Lock, Box,
};

export function ResourceIcon({ kind, className }: { kind: string; className?: string }) {
  const Icon = ICONS[KIND_ICON(kind)] ?? Box;
  return <Icon className={cn("h-4 w-4", className)} />;
}
