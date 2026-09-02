import { getApiBaseUrl, getTenantId } from "./config";
import { mockDelay } from "./client";

export interface SseEvent {
  event?: string;
  data: string;
}

/**
 * Consumes a server-sent-events endpoint via fetch + ReadableStream rather
 * than the native EventSource, because CloudOptix's streaming endpoints
 * (onboarding messages, copilot ask, execution progress) are POST with a
 * JSON body and a tenant header — both outside what EventSource supports.
 * Pass an AbortSignal to let the caller cancel a long-lived stream (e.g. the
 * user navigates away mid-answer).
 */
export async function* sseStream(
  path: string,
  opts: { method?: "GET" | "POST"; body?: unknown; signal?: AbortSignal } = {}
): AsyncGenerator<SseEvent> {
  const url = path.startsWith("http") ? path : `${getApiBaseUrl()}${path}`;
  const res = await fetch(url, {
    method: opts.method ?? "GET",
    headers: {
      Accept: "text/event-stream",
      "Content-Type": "application/json",
      "X-CloudOptix-Tenant": getTenantId(),
    },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
  });
  if (!res.ok || !res.body) {
    throw new Error(`stream request failed: ${res.status}`);
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let sepIdx: number;
      while ((sepIdx = buffer.indexOf("\n\n")) !== -1) {
        const chunk = buffer.slice(0, sepIdx);
        buffer = buffer.slice(sepIdx + 2);
        const evt: SseEvent = { data: "" };
        const dataLines: string[] = [];
        for (const line of chunk.split("\n")) {
          if (line.startsWith("event:")) evt.event = line.slice(6).trim();
          else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
        }
        evt.data = dataLines.join("\n");
        if (evt.data) yield evt;
      }
    }
  } finally {
    reader.releaseLock();
  }
}

/** Mock-mode replacement for sseStream: replays a pre-built script of events
 * with realistic pacing, so streaming UIs (onboarding chat, copilot,
 * execution progress) behave identically whether or not a backend exists. */
export async function* mockSseEvents<T>(
  events: { event?: string; data: T; delayMs?: number }[],
  signal?: AbortSignal
): AsyncGenerator<SseEvent> {
  for (const e of events) {
    if (signal?.aborted) return;
    await mockDelay(e.delayMs ?? 220);
    if (signal?.aborted) return;
    yield { event: e.event, data: JSON.stringify(e.data) };
  }
}
