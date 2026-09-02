# Runbook: discovery failures and IAM permission gaps

## Symptom

A discovery run (`POST /discovery/runs`) completes with `jobs_failed` non-empty, or `GET /aws-accounts/{id}` shows `ConnectionState: degraded`, or a tenant reports "the estate looks smaller than I expect."

## Diagnosis

1. **Check the account's connection state first.** `GET /aws-accounts/{id}` — if `ConnectionState` is `degraded` or `failed`, `MissingActions` names the exact denied IAM actions from the last verification probe. This is the fastest path to root cause and should be checked before looking at any individual discovery run.
2. **If connected but a specific run had failed jobs**, `GET /discovery/runs/{runID}` lists per-(service, region) job outcomes. Per `internal/application/discovery`'s design (SPEC-DSC-002), a permission error is **never retried** — it fails fast on the first attempt and reports the exact denied action, so a failed job here is immediately actionable, not the tail end of four wasted retry attempts.
3. **Confirm this is a permission problem, not a throttling problem**, by checking whether the job's error is classified as retryable (`core.Retryable`). A permission error and a throttling error look different in the job detail: a permission error carries a specific denied IAM action string; a throttling error does not (see [`aws-throttling.md`](aws-throttling.md) if it's the latter).
4. **Check whether the gap is scoped to one (service, region) pair or the whole account.** Because tombstoning is scoped to exactly the (kind, region) pairs a successful job covered (SPEC-DSC-002's decision 2), a permission gap in one service never causes another service's resources to be incorrectly marked absent — so "the estate looks smaller" from a permission gap in, say, EKS discovery should show up as EKS-kind resources specifically missing or stale, not the whole inventory shrinking.

## Resolution

1. Compare `MissingActions` (or the failed job's denied action) against the IAM policy actually attached to the role for the affected `cloud.RoleScope` (`read` or `analyze` — a discovery gap is never an `execute`-role problem, since discovery never requests execute-scoped sessions).
2. Regenerate the exact required policy: `GET /aws-accounts/{id}/instructions` returns the current, complete instructions for the account's state, built from every `ResourceDiscoverer.RequiredActions()` in scope — this is the same source of truth the initial onboarding instructions came from, so it will not have drifted from what the platform actually needs.
3. Have the customer apply the missing actions to the role's policy in their own account (CloudOptix has no ability to modify a customer's IAM policy — see [`docs/security-spec.md`](../security-spec.md), `SPEC-SEC-001`).
4. Re-verify: `POST /aws-accounts/{id}/verify` re-runs the permission probe and updates `ConnectionState`/`MissingActions`.
5. Re-run discovery: `POST /discovery/runs`. Only the previously-failed (service, region) pairs need to complete for the gap to close — a full re-scan is not required, though running one is harmless.

## What NOT to do

- Do not manually mark resources as present/absent to work around a permission gap — the tombstone-scoping design exists specifically so a permission gap degrades gracefully (stale-but-not-deleted) without intervention; overriding it defeats that guarantee.
- Do not retry a job that failed on a permission error expecting it to eventually succeed — `core.Retryable` correctly classifies it as non-transient, and the retry loop will not touch it again on its own; only re-verification after the IAM policy is fixed will resolve it.

## Escalation

If `MissingActions` is empty but jobs are still failing (a state the design does not expect — a successful `verify` implies the probed actions are granted), this is a discrepancy between what `verify`'s probe checks and what a specific discoverer's `Discover` call actually needs — likely a `RequiredActions()` declaration that is out of sync with the discoverer's real API usage. Escalate to the team owning `internal/adapters/aws/discovery` with the specific discoverer and denied-action error from the job detail.
