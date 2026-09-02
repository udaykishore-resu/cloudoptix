"use client";
import { useQuery } from "@tanstack/react-query";
import type { CostAnomaly, CostBreakdown, CostExplanation, CostForecast, CostSeries, CostSummary } from "@/types/api";
import { isMock, mockDelay, request } from "./client";
import * as fx from "@/lib/mock/fixtures/costs";

export type BreakdownDimension = "service" | "account" | "region" | "environment" | "application";

async function fetchCostSummary(): Promise<CostSummary> {
  if (isMock()) {
    await mockDelay();
    return fx.buildCostSummary();
  }
  return request<CostSummary>("/costs/summary");
}

async function fetchCostSeries(days: number): Promise<CostSeries> {
  if (isMock()) {
    await mockDelay();
    return fx.buildCostSeries(days);
  }
  return request<CostSeries>(`/costs/series?days=${days}`);
}

async function fetchBreakdown(dimension: BreakdownDimension): Promise<CostBreakdown> {
  if (isMock()) {
    await mockDelay();
    return fx.buildBreakdown(dimension);
  }
  return request<CostBreakdown>(`/costs/breakdown?dimension=${dimension}`);
}

async function fetchForecast(): Promise<CostForecast> {
  if (isMock()) {
    await mockDelay();
    return fx.buildForecast();
  }
  return request<CostForecast>("/costs/forecast");
}

async function fetchAnomalies(): Promise<CostAnomaly[]> {
  if (isMock()) {
    await mockDelay();
    return fx.buildAnomalies();
  }
  return request<CostAnomaly[]>("/costs/anomalies");
}

async function fetchExplanation(): Promise<CostExplanation> {
  if (isMock()) {
    await mockDelay();
    return fx.buildExplanation();
  }
  return request<CostExplanation>("/costs/explain");
}

export function useCostSummary() {
  return useQuery({ queryKey: ["costs", "summary"], queryFn: fetchCostSummary });
}
export function useCostSeries(days = 90) {
  return useQuery({ queryKey: ["costs", "series", days], queryFn: () => fetchCostSeries(days) });
}
export function useCostBreakdown(dimension: BreakdownDimension) {
  return useQuery({ queryKey: ["costs", "breakdown", dimension], queryFn: () => fetchBreakdown(dimension) });
}
export function useCostForecast() {
  return useQuery({ queryKey: ["costs", "forecast"], queryFn: fetchForecast });
}
export function useCostForecastSeries(horizonDays = 30) {
  return useQuery({ queryKey: ["costs", "forecast-series", horizonDays], queryFn: async () => {
    if (isMock()) { await mockDelay(); return fx.buildForecastSeries(horizonDays); }
    return request(`/costs/forecast?horizon_days=${horizonDays}`);
  }});
}
export function useCostAnomalies() {
  return useQuery({ queryKey: ["costs", "anomalies"], queryFn: fetchAnomalies });
}
export function useCostExplanation() {
  return useQuery({ queryKey: ["costs", "explain"], queryFn: fetchExplanation });
}
