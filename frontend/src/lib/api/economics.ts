"use client";
import { useQuery } from "@tanstack/react-query";
import type {
  BusinessTransaction,
  Footprint,
} from "@/types/api";
import type { CostSLO, EconomicErrorBudget, EfficiencyScore, ExecutiveSummary, SavingsFunnel, UnitEconomics } from "@/types/domain";
import { isMock, mockDelay, request } from "./client";
import * as fx from "@/lib/mock/fixtures/economics";

async function fetchFootprints(): Promise<Footprint[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildFootprints();
  }
  return request<Footprint[]>("/economics/footprints");
}
async function fetchTransactions(): Promise<BusinessTransaction[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildTransactions();
  }
  return request<BusinessTransaction[]>("/economics/transactions");
}
async function fetchUnitEconomics(): Promise<UnitEconomics[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildUnitEconomics();
  }
  return request<UnitEconomics[]>("/economics/transactions");
}
async function fetchUnitEconomicsHistory(txId: string): Promise<UnitEconomics[]> {
  if (isMock()) {
    await mockDelay(220);
    return fx.buildUnitEconomicsHistory(txId);
  }
  return request<UnitEconomics[]>(`/economics/transactions/${txId}/unit-economics/history`);
}
async function fetchCostSLOs(): Promise<CostSLO[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildCostSLOs();
  }
  return request<CostSLO[]>("/cost-slos");
}
async function fetchBudgetStates(): Promise<EconomicErrorBudget[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildBudgetStates();
  }
  return request<EconomicErrorBudget[]>("/cost-slos/budget-states");
}
async function fetchEfficiencyScore(): Promise<EfficiencyScore> {
  if (isMock()) {
    await mockDelay();
    return fx.buildEfficiencyScore();
  }
  return request<EfficiencyScore>("/economics/efficiency-score");
}
async function fetchExecutiveSummary(): Promise<ExecutiveSummary> {
  if (isMock()) {
    await mockDelay();
    return fx.buildExecutiveSummary();
  }
  return request<ExecutiveSummary>("/economics/executive-summary");
}
async function fetchSavingsFunnel(): Promise<SavingsFunnel> {
  if (isMock()) {
    await mockDelay();
    return fx.buildSavingsFunnel();
  }
  return request<SavingsFunnel>("/savings/funnel");
}

export function useFootprints() {
  return useQuery({ queryKey: ["economics", "footprints"], queryFn: fetchFootprints });
}
export function useTransactions() {
  return useQuery({ queryKey: ["economics", "transactions"], queryFn: fetchTransactions });
}
export function useUnitEconomics() {
  return useQuery({ queryKey: ["economics", "unit-economics"], queryFn: fetchUnitEconomics });
}
export function useUnitEconomicsHistory(txId: string | undefined) {
  return useQuery({ queryKey: ["economics", "unit-economics-history", txId], queryFn: () => fetchUnitEconomicsHistory(txId as string), enabled: !!txId });
}
export function useCostSLOs() {
  return useQuery({ queryKey: ["economics", "cost-slos"], queryFn: fetchCostSLOs });
}
export function useBudgetStates() {
  return useQuery({ queryKey: ["economics", "budget-states"], queryFn: fetchBudgetStates });
}
export function useEfficiencyScore() {
  return useQuery({ queryKey: ["economics", "efficiency-score"], queryFn: fetchEfficiencyScore });
}
export function useExecutiveSummary() {
  return useQuery({ queryKey: ["economics", "executive-summary"], queryFn: fetchExecutiveSummary });
}
export function useSavingsFunnel() {
  return useQuery({ queryKey: ["economics", "savings-funnel"], queryFn: fetchSavingsFunnel });
}
export function useSloViolations(sloId: string | undefined) {
  return useQuery({
    queryKey: ["economics", "slo-violations", sloId],
    queryFn: async () => {
      await mockDelay(150);
      return fx.buildViolationHistory(sloId as string);
    },
    enabled: !!sloId,
  });
}
