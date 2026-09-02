# Runbook: AWS throttling

## Symptom

Discovery jobs report elevated `Throttled` counts in `DiscoveryOutput`, discovery runs take materially longer than usual to complete, or cost/metrics ingestion falls behind (see [`cost-data-staleness.md`](cost-data-staleness.md) if the symptom is specifically ingestion lag).

## Diagnosis

1. **Confirm it is throttling, not a permission gap.** A throttling error is classified `core.Retryable`; a permission error is not, and reports a specific denied IAM action. If jobs are showing denied actions, this is [`discovery-iam-gaps.md`](discovery-iam-gaps.md) instead.
2. **Check whether throttling is scoped to one account, one service, or platform-wide.** AWS API rate limits are per-account, per-service, per-region — a single large tenant's discovery jobs throttling does not imply every tenant is affected. `DiscoveryOutput.Throttled` is reported per job, so this is directly visible per (service, region, account).
3. **Check the worker-pool concurrency setting** actually in effect for the affected tenant/account — discovery runs one (service × region) job per unit of bounded concurrency; a very high concurrency setting against an account with default (unraised) AWS service quotas is the most common cause of self-inflicted throttling.

## Resolution

**Discovery jobs:** Nothing needs to be done manually for an isolated, low-rate throttling event — `internal/application/discovery`'s per-job retry loop already applies exponential backoff with full jitter specifically for this error class (SPEC-DSC-002, decision 3), and one job throttling does not stop any other job. Let it complete.

**Sustained throttling on one account:**
1. Check whether the account has requested a service-quota increase for the affected API (EC2 `Describe*` calls, CloudWatch `GetMetricData`, and Cost Explorer's own API are the most commonly quota-limited in a large estate) — this is the customer's own AWS account quota, not something CloudOptix controls.
2. If a quota increase is not feasible quickly, reduce the discovery worker pool's concurrency for that tenant, trading discovery run duration for a lower per-second call rate against the account.
3. For CloudWatch metric collection specifically (`internal/adapters/aws/metrics/cloudwatch.go`), confirm metric collection failure isolation is behaving as designed — one resource's metric-collection throttle should not abort the batch for the rest of the resources (`REQ-UTL-007`); if it is aborting the whole batch, this is a defect, not expected throttling behaviour, and should be escalated.

**Sustained throttling affecting the LLM provider layer** (a different kind of throttling — a rate limit from the model provider, not AWS): see `internal/adapters/llm/middleware`'s own rate limiter and circuit breaker; if the deployed rate limit/quota (`tenancy.Quotas.MaxCopilotTokensPerDay`) is set below what the tenant's actual usage needs, this is a configuration change, not an incident.

## What NOT to do

- Do not disable retry/backoff for a throttled job — the full-jitter exponential backoff exists specifically to avoid a synchronized retry storm making the throttling worse.
- Do not raise worker-pool concurrency to "push through" sustained throttling — this typically makes the underlying AWS-side rate limit condition worse, not better.

## Escalation

If throttling persists after a confirmed AWS-side quota increase and no concurrency change resolves it, escalate to the team owning `internal/adapters/aws/discovery`/`internal/infrastructure/resilience` — the backoff parameters (base delay, max attempts, jitter bounds) may need tenant-specific tuning, which is not currently exposed as a per-tenant configuration option in this codebase.
