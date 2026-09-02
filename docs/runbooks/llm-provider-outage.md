# Runbook: LLM provider outage

## Symptom

Onboarding conversations stall, copilot answers degrade to short fixed replies, or `ports.LLMProvider.Healthy()` reports `false` for the configured primary provider (Anthropic or Bedrock).

## Diagnosis

1. **Confirm the platform is already degrading gracefully.** `internal/adapters/llm/fallback` is designed to route every call to the `deterministic` provider the moment the primary reports unhealthy — the platform should *not* be down, only running its AI-touched paths in scripted/degraded mode. If requests are failing outright rather than degrading, the fallback wiring itself is the incident, not the provider outage — escalate immediately (see below).
2. **Distinguish a provider-side outage from a CloudOptix-side misconfiguration.** Check `internal/adapters/llm/middleware`'s circuit-breaker state and recent error codes: a provider outage typically presents as connection timeouts or 5xx from the provider; a misconfiguration (expired API key, wrong Bedrock region/model ID) typically presents as consistent 401/403/404 from the very first call, not an outage pattern.
3. **Check rate-limit/quota exhaustion as a distinct possibility.** `tenancy.Quotas.MaxCopilotTokensPerDay` being exhausted for a tenant looks similar to a provider outage from that tenant's perspective (calls stop succeeding) but is a per-tenant condition, not a platform-wide one — check whether the degradation is scoped to one tenant or all of them.

## Resolution

**While the primary provider is down:** No action is required for the platform to keep functioning — this is the entire point of `fallback`. Onboarding still extracts structured fields (via the deterministic provider's regex slot-filling, applying the identical interpreter the real model's output would go through), and the copilot still answers from real tool results (via the deterministic provider's keyword-routed tool selection and templated answer composition) — degraded in narrative quality, not broken. Communicate to affected tenants that responses will read as more scripted/templated than usual until the primary provider recovers.

**Confirming recovery:** Once the provider's own status is healthy again, confirm `Healthy()` returns `true` and that `fallback` has stopped routing to `deterministic` — check recent completion request metrics (`internal/adapters/llm/middleware`'s tracing/metrics decorator) for calls actually reaching the primary provider again.

**If the outage is prolonged:** Consider whether Bedrock (if Anthropic direct is the primary) or vice versa is a viable temporary primary — both serve the same Anthropic model family through different transports, so switching which one `internal/infrastructure/config`'s `LLMConfig` points to as primary is a configuration change, not a code change.

## What NOT to do

- Do not disable the circuit breaker to "force" calls through to a known-unhealthy provider — this extends user-facing latency (every call pays the full timeout) without improving success rate.
- Do not bypass `fallback` and route directly to the deterministic provider as a manual workaround — `fallback` already does this automatically and additionally handles recovery detection; a manual bypass would need to be manually reverted.

## Escalation

If `fallback` itself is not correctly routing to `deterministic` on a confirmed primary-provider outage (requests fail outright instead of degrading), this is a defect in `internal/adapters/llm/fallback`, not a provider-outage response question — escalate immediately as a P0, since it means the platform's stated AI-outage resilience property is not holding.
