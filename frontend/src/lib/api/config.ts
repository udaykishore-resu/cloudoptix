export type ApiMode = "mock" | "live";

export function getApiMode(): ApiMode {
  const v = process.env.NEXT_PUBLIC_API_MODE;
  return v === "live" ? "live" : "mock";
}

export function isMockMode(): boolean {
  return getApiMode() === "mock";
}

export function getApiBaseUrl(): string {
  return process.env.NEXT_PUBLIC_API_BASE_URL || "http://localhost:8080/api/v1";
}

export function getTenantId(): string {
  return process.env.NEXT_PUBLIC_TENANT_ID || "tn_01hz3k4x8y";
}

/** Simulated network latency for mock-mode calls, so loading skeletons and
 * suspense states are visible during development rather than flashing by. */
export function getMockLatencyMs(): number {
  const v = process.env.NEXT_PUBLIC_MOCK_LATENCY_MS;
  const n = v ? Number(v) : 380;
  return Number.isFinite(n) ? n : 380;
}
