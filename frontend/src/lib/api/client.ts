import type { Problem, ValidationIssue } from "@/types/api";
import { getApiBaseUrl, getMockLatencyMs, getTenantId, isMockMode } from "./config";

export class ApiError extends Error {
  status: number;
  code: string;
  issues?: ValidationIssue[];
  requestId?: string;
  problem?: Problem;

  constructor(problem: Problem, status: number) {
    super(problem.detail || problem.title || `Request failed with status ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = problem.code || "unknown_error";
    this.issues = problem.issues;
    this.requestId = problem.request_id;
    this.problem = problem;
  }
}

export interface RequestOptions extends Omit<RequestInit, "body"> {
  body?: unknown;
  tenantId?: string;
  /** Skip tenant header injection (e.g. /auth/whoami before a tenant is picked). */
  noTenant?: boolean;
}

/** Simulates network latency in mock mode so loading states are visible. */
export async function mockDelay(ms?: number): Promise<void> {
  const delay = ms ?? getMockLatencyMs();
  if (delay <= 0) return;
  await new Promise((resolve) => setTimeout(resolve, delay));
}

/**
 * Typed fetch wrapper for the live API: injects the tenant header, parses
 * RFC 7807 problem+json error bodies into ApiError, and returns parsed JSON
 * on success. Every typed fetcher in src/lib/api/*.ts calls this when
 * NEXT_PUBLIC_API_MODE=live; in mock mode fetchers short-circuit to the
 * fixtures in src/lib/mock instead and never reach this function.
 */
export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { body, tenantId, noTenant, headers, ...rest } = opts;
  const url = path.startsWith("http") ? path : `${getApiBaseUrl()}${path}`;

  const finalHeaders: Record<string, string> = {
    Accept: "application/json",
    ...(headers as Record<string, string>),
  };
  if (body !== undefined) finalHeaders["Content-Type"] = "application/json";
  if (!noTenant) finalHeaders["X-CloudOptix-Tenant"] = tenantId || getTenantId();

  let res: Response;
  try {
    res = await fetch(url, {
      ...rest,
      headers: finalHeaders,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (err) {
    throw new ApiError(
      {
        type: "https://docs.cloudoptix.io/errors/network",
        title: "Network Error",
        status: 0,
        code: "network_error",
        detail: err instanceof Error ? err.message : "The request could not be sent.",
      },
      0
    );
  }

  if (res.status === 204) return undefined as T;

  const contentType = res.headers.get("content-type") || "";
  const isJson = contentType.includes("application/json") || contentType.includes("problem+json");
  const payload = isJson ? await res.json().catch(() => undefined) : undefined;

  if (!res.ok) {
    const problem: Problem = payload ?? {
      type: "about:blank",
      title: res.statusText || "Request failed",
      status: res.status,
      code: "unknown_error",
    };
    throw new ApiError(problem, res.status);
  }
  return payload as T;
}

export function isMock(): boolean {
  return isMockMode();
}
