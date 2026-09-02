"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { CompilationResult, Counterfactual, RegressionReport, RegressionSuite, Scenario, Simulation, SourceKind } from "@/types/domain";
import { isMock, mockDelay, request } from "./client";
import * as fx from "@/lib/mock/fixtures/simulate";

let simCache: Simulation | null = null;
let compCache: CompilationResult | null = null;

export function useGenerateCandidates() {
  return useMutation({
    mutationFn: async (scopeName: string) => {
      if (isMock()) {
        await mockDelay(1800);
        simCache = fx.buildSimulation(scopeName);
        return simCache;
      }
      return request<Simulation>("/simulations/mutate", { method: "POST", body: { scope_id: scopeName } });
    },
  });
}
export function useSimulation(id: string | undefined) {
  return useQuery({
    queryKey: ["simulate", "simulation", id],
    queryFn: async () => (isMock() ? simCache ?? fx.buildSimulation() : request<Simulation>(`/simulations/${id}`)),
    enabled: !!id,
  });
}

export function useCounterfactual() {
  return useMutation({
    mutationFn: async (scenario: Scenario) => {
      if (isMock()) {
        await mockDelay(900);
        return fx.buildCounterfactual(scenario);
      }
      return request<Counterfactual>("/simulations/counterfactual", { method: "POST", body: { scenario } });
    },
  });
}

export function useCompile() {
  return useMutation({
    mutationFn: async ({ label, source }: { label: string; source: SourceKind }) => {
      if (isMock()) {
        await mockDelay(1400);
        compCache = fx.buildCompilation(label);
        return compCache;
      }
      return request<CompilationResult>("/compiler/compile", { method: "POST", body: { label, source } });
    },
  });
}
export function useCompilation(id: string | undefined) {
  return useQuery({
    queryKey: ["simulate", "compilation", id],
    queryFn: async () => (isMock() ? compCache ?? fx.buildCompilation("cached") : request<CompilationResult>(`/compiler/compilations/${id}`)),
    enabled: !!id,
  });
}

export function useRegressionSuites() {
  return useQuery({
    queryKey: ["simulate", "regression-suites"],
    queryFn: async () => {
      if (isMock()) {
        await mockDelay();
        return fx.buildRegressionSuites();
      }
      return request<RegressionSuite[]>("/regression/suites");
    },
  });
}

export function useRunRegression() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ compilation, suiteName }: { compilation: CompilationResult; suiteName: string }) => {
      if (isMock()) {
        await mockDelay(700);
        return fx.buildRegressionReport(compilation, suiteName);
      }
      return request<RegressionReport>(`/compiler/compilations/${compilation.id}/regression`, { method: "POST", body: { suite_name: suiteName } });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["simulate", "regression-suites"] }),
  });
}

export function useRegressionHistory() {
  return useQuery({
    queryKey: ["simulate", "regression-history"],
    queryFn: async () => {
      await mockDelay();
      return fx.buildReportHistory();
    },
  });
}

export function buildPrComment(report: RegressionReport, compilation: CompilationResult) {
  return fx.buildPrComment(report, compilation);
}
