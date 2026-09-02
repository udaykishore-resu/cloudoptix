"use client";
import { useQuery } from "@tanstack/react-query";
import type { CostFlowGraph, Resource, TwinGraph, TwinNode, TwinView } from "@/types/domain";
import { isMock, mockDelay, request } from "./client";
import * as fx from "@/lib/mock/fixtures/twin";
import { getWorld } from "@/lib/mock/world";

export interface TwinQueryParams {
  view?: TwinView;
  search?: string;
  environments?: string[];
  accountIds?: string[];
  applicationId?: string;
}

async function fetchGraph(params: TwinQueryParams): Promise<TwinGraph> {
  if (isMock()) {
    await mockDelay();
    return fx.buildTwinGraph(params.view ?? "architecture", params);
  }
  const qs = new URLSearchParams();
  if (params.view) qs.set("view", params.view);
  if (params.search) qs.set("search", params.search);
  return request<TwinGraph>(`/architecture/graph?${qs.toString()}`);
}

async function fetchCostFlow(): Promise<CostFlowGraph> {
  if (isMock()) {
    await mockDelay();
    return fx.buildCostFlow();
  }
  return request<CostFlowGraph>("/architecture/cost-flow");
}

async function fetchNode(id: string): Promise<TwinNode | undefined> {
  if (isMock()) {
    await mockDelay(150);
    return fx.getTwinNode(id);
  }
  return request<TwinNode>(`/architecture/nodes/${id}`);
}

async function fetchDependents(id: string): Promise<TwinNode[]> {
  if (isMock()) {
    await mockDelay(150);
    return fx.getDependents(id);
  }
  return request<TwinNode[]>(`/architecture/nodes/${id}/dependents`);
}

export function useTwinGraph(params: TwinQueryParams) {
  return useQuery({ queryKey: ["twin", "graph", params], queryFn: () => fetchGraph(params) });
}
export function useCostFlowGraph() {
  return useQuery({ queryKey: ["twin", "cost-flow"], queryFn: fetchCostFlow });
}
export function useTwinNode(id: string | undefined) {
  return useQuery({ queryKey: ["twin", "node", id], queryFn: () => fetchNode(id as string), enabled: !!id });
}
export function useTwinDependents(id: string | undefined) {
  return useQuery({ queryKey: ["twin", "dependents", id], queryFn: () => fetchDependents(id as string), enabled: !!id });
}
export function useTwinEdgesFor(id: string | undefined) {
  return useQuery({
    queryKey: ["twin", "edges", id],
    queryFn: async () => {
      await mockDelay(80);
      return fx.getEdgesFor(id as string);
    },
    enabled: !!id,
  });
}
export function useTwinDependencies(id: string | undefined) {
  return useQuery({
    queryKey: ["twin", "dependencies", id],
    queryFn: async () => {
      await mockDelay(80);
      return fx.getDependencies(id as string);
    },
    enabled: !!id,
  });
}

// --- Resources (the raw estate; resource explorer reads this, not the graph) --
export interface ResourceFilter {
  search?: string;
  kinds?: string[];
  accountIds?: string[];
  regions?: string[];
  environments?: string[];
  applications?: string[];
  minCost?: number;
  hasFindings?: boolean;
}

async function fetchResources(filter: ResourceFilter): Promise<Resource[]> {
  if (isMock()) {
    await mockDelay();
    let list = getWorld().resources;
    if (filter.search) {
      const q = filter.search.toLowerCase();
      list = list.filter((r) => r.name?.toLowerCase().includes(q) || r.native_id.toLowerCase().includes(q) || r.kind.toLowerCase().includes(q));
    }
    if (filter.kinds?.length) list = list.filter((r) => filter.kinds!.includes(r.kind));
    if (filter.accountIds?.length) list = list.filter((r) => filter.accountIds!.includes(r.account_id));
    if (filter.regions?.length) list = list.filter((r) => filter.regions!.includes(r.region));
    if (filter.environments?.length) list = list.filter((r) => filter.environments!.includes(r.environment));
    if (filter.applications?.length) list = list.filter((r) => r.application && filter.applications!.includes(r.application));
    if (filter.minCost) list = list.filter((r) => r.monthly_cost.amount >= filter.minCost!);
    if (filter.hasFindings) list = list.filter((r) => (r.finding_count ?? 0) > 0);
    return list;
  }
  return request<Resource[]>("/resources");
}

export function useResources(filter: ResourceFilter = {}) {
  return useQuery({ queryKey: ["resources", filter], queryFn: () => fetchResources(filter) });
}
export function useResource(id: string | undefined) {
  return useQuery({
    queryKey: ["resources", "one", id],
    queryFn: async () => {
      await mockDelay(120);
      return getWorld().resources.find((r) => r.id === id);
    },
    enabled: !!id,
  });
}
