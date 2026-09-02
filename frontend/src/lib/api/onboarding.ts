"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { AWSOnboardingInstructions, OnboardingState, OnboardingSummary } from "@/types/api";
import { isMock, mockDelay, request } from "./client";
import { mockSseEvents, sseStream, type SseEvent } from "./sse";
import * as fx from "@/lib/mock/fixtures/onboarding";

export function useStartOnboarding() {
  return useMutation({
    mutationFn: async () => {
      if (isMock()) {
        await mockDelay(500);
        return fx.startConversation();
      }
      const state = await request<OnboardingState>("/onboarding", { method: "POST", body: {} });
      return { id: state.conversation_id!, state };
    },
  });
}

export function useOnboardingState(conversationId: string | undefined) {
  return useQuery({
    queryKey: ["onboarding", "state", conversationId],
    queryFn: async () => {
      if (isMock()) {
        await mockDelay(150);
        return fx.getState(conversationId as string);
      }
      return request<OnboardingState>(`/onboarding/${conversationId}`);
    },
    enabled: !!conversationId,
  });
}

export function useSendMessage() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ conversationId, message }: { conversationId: string; message: string }) => {
      if (isMock()) {
        await mockDelay(500);
        return fx.sendMessage(conversationId, message);
      }
      return request<OnboardingState>(`/onboarding/${conversationId}/messages`, { method: "POST", body: { message } });
    },
    onSuccess: (_, vars) => qc.invalidateQueries({ queryKey: ["onboarding", "state", vars.conversationId] }),
  });
}

/** Streams the assistant's reply a few words at a time, ending with the full
 * updated OnboardingState — used by the chat pane so the reply appears to
 * type in, matching how the live SSE endpoint behaves. */
export async function* streamReply(conversationId: string, message: string, signal?: AbortSignal): AsyncGenerator<{ kind: "delta"; text: string } | { kind: "state"; state: OnboardingState }> {
  if (isMock()) {
    const state = fx.sendMessage(conversationId, message);
    const words = (state.reply ?? "").split(" ");
    const chunks: { event?: string; data: { text: string } }[] = [];
    for (let i = 0; i < words.length; i += 3) chunks.push({ event: "delta", data: { text: words.slice(i, i + 3).join(" ") + " " } });
    for await (const evt of mockSseEvents(chunks, signal)) {
      yield { kind: "delta", text: (JSON.parse(evt.data) as { text: string }).text };
    }
    yield { kind: "state", state };
    return;
  }
  for await (const evt of sseStream(`/onboarding/${conversationId}/messages/stream`, { method: "POST", body: { message }, signal })) {
    yield parseLiveEvent(evt);
  }
}

function parseLiveEvent(evt: SseEvent): { kind: "delta"; text: string } | { kind: "state"; state: OnboardingState } {
  const data = JSON.parse(evt.data);
  if (evt.event === "state") return { kind: "state", state: data as OnboardingState };
  return { kind: "delta", text: (data as { text: string }).text ?? "" };
}

export function useOnboardingSummary(conversationId: string | undefined) {
  return useQuery({
    queryKey: ["onboarding", "summary", conversationId],
    queryFn: async () => {
      if (isMock()) {
        await mockDelay(400);
        return fx.buildSummary(conversationId as string);
      }
      return request<OnboardingSummary>(`/onboarding/${conversationId}/summary`);
    },
    enabled: !!conversationId,
  });
}

export function useApproveOnboarding() {
  return useMutation({
    mutationFn: async (conversationId: string) => {
      if (isMock()) {
        await mockDelay(900);
        return {
          tenant: { id: "tn_01hz3k4x8y", slug: "acme-corp", name: "Acme Corp", plan: "standard", state: "active" },
          spec_version: { id: "spv_1", version: 1, status: "approved" },
          next_steps: fx.buildAwsInstructions(),
        };
      }
      return request(`/onboarding/${conversationId}/approve`, { method: "POST", body: {} });
    },
  });
}

export function useAwsInstructions() {
  return useQuery({
    queryKey: ["onboarding", "aws-instructions"],
    queryFn: async () => {
      if (isMock()) {
        await mockDelay(300);
        return fx.buildAwsInstructions();
      }
      return request<AWSOnboardingInstructions>("/aws-accounts/acct_prod01/instructions");
    },
  });
}
