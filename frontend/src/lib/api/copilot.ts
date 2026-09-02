"use client";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { CopilotAnswer } from "@/types/api";
import { isMock, mockDelay, request } from "./client";
import { mockSseEvents, sseStream } from "./sse";
import * as fx from "@/lib/mock/fixtures/copilot";

export function useCopilotSuggestions() {
  return useQuery({
    queryKey: ["copilot", "suggestions"],
    queryFn: async () => {
      if (isMock()) { await mockDelay(200); return fx.SUGGESTED_QUESTIONS; }
      return request<string[]>("/copilot/suggestions");
    },
  });
}

export function useAskCopilot() {
  return useMutation({
    mutationFn: async (question: string) => {
      if (isMock()) {
        await mockDelay(900);
        return fx.answer(question);
      }
      return request<CopilotAnswer>("/copilot/ask", { method: "POST", body: { question } });
    },
  });
}

/** Streams the answer word-by-word, then a final citations/tool-calls event —
 * mirrors the shape of the live /copilot/ask/stream endpoint. */
export async function* streamAsk(question: string, signal?: AbortSignal): AsyncGenerator<{ kind: "delta"; text: string } | { kind: "final"; answer: CopilotAnswer }> {
  if (isMock()) {
    const full = fx.answer(question);
    const words = (full.answer ?? "").split(" ");
    const chunks = [];
    for (let i = 0; i < words.length; i += 4) chunks.push({ data: { text: words.slice(i, i + 4).join(" ") + " " } });
    for await (const evt of mockSseEvents(chunks, signal)) {
      yield { kind: "delta", text: (JSON.parse(evt.data) as { text: string }).text };
    }
    yield { kind: "final", answer: full };
    return;
  }
  for await (const evt of sseStream("/copilot/ask/stream", { method: "POST", body: { question }, signal })) {
    const data = JSON.parse(evt.data);
    if (evt.event === "final") yield { kind: "final", answer: data as CopilotAnswer };
    else yield { kind: "delta", text: (data as { text: string }).text ?? "" };
  }
}
