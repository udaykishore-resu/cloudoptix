"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { isMock, mockDelay, request } from "./client";
import { ACCOUNTS, type Account } from "@/lib/mock/world";

export interface ConnectionCheck {
  scope: string;
  granted: boolean;
  missingActions: string[];
}

export interface VerifyAccountResult {
  account?: Account;
  check: {
    grantedScopes: string[];
    missingActions: string[];
    state: "connected" | "degraded";
  };
}

async function fetchAccounts(): Promise<Account[]> {
  if (isMock()) {
    await mockDelay();
    return ACCOUNTS;
  }
  return request<Account[]>("/aws-accounts");
}

export function useAwsAccounts() {
  return useQuery({ queryKey: ["aws-accounts", "list"], queryFn: fetchAccounts });
}

export function useVerifyAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (accountId: string): Promise<VerifyAccountResult> => {
      if (isMock()) {
        await mockDelay(1200);
        const acct = ACCOUNTS.find((a) => a.id === accountId);
        return {
          account: acct,
          check: {
            grantedScopes: acct?.grantedScopes ?? [],
            missingActions: acct?.missingActions ?? [],
            state: acct?.missingActions.length ? "degraded" : "connected",
          },
        };
      }
      return request<VerifyAccountResult>(`/aws-accounts/${accountId}/verify`, { method: "POST" });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["aws-accounts"] }),
  });
}
