# ADR-0006: Hash-chained audit log

## Status

Accepted, implemented (application-layer chain; production object-storage retention-lock write path is a stated design intent, not an implemented adapter — see Consequences).

## Context

CloudOptix's entire safety story — policy-as-code, approvals, execution, rollback — is only as trustworthy as the record of what actually happened. A mutable database row recording "recommendation X was approved by user Y" is, on its own, exactly as editable after the fact as any other database row, which is a problem for a platform whose value proposition includes "an auditor can trust this record."

## Decision

`internal/domain/audit.Record` carries `PrevHash`, the SHA-256 hash of its predecessor record for the same tenant, computed over a canonical field subset (`ComputeHash`). `Record.Seal` sets `PrevHash` and a monotonic `Sequence` at write time. `audit.VerifyChain` walks an ordered slice of records and reports the first break, if any — a single, fast, deterministic verification pass that detects any edit or deletion anywhere in the chain, because altering or removing one record breaks the hash link at every subsequent entry. `Action` is a closed, enumerated vocabulary (not free text), so a query for "every production execution last quarter" is exact.

## Consequences

**Positive:**
- Tampering is *detectable*, not merely *discouraged*. An operator (malicious or careless) editing a database row directly, bypassing the application layer entirely, still breaks verification the next time `VerifyChain` runs — a property that policy or convention alone cannot provide.
- Verification is cheap: `O(n)` over the record set, no external dependency, runnable on demand (`GET /audit/verify`) or on a schedule.
- The closed action vocabulary makes the audit log queryable with the same precision a SQL `WHERE` clause gives a typed column, rather than the recall problems a free-text log invites.

**Negative and explicitly acknowledged:**
- **Hash-chaining does not make the log immutable.** Nothing stored in a mutable database is immutable — an operator with direct database access can still edit *every* record in a chain consistently (recomputing every subsequent hash), which defeats detection entirely if the attacker has that level of access. The mitigation for *that* threat model — true immutability — is writing records to object storage under a retention lock in addition to the mutable database copy, which `internal/domain/audit`'s own package doc states as the production design but which **has no implemented adapter in this codebase** (confirmed: no S3/object-storage/retention-lock-related file exists under `internal/adapters`). The hash chain alone defends against casual or accidental tampering and against detection lag, not against a sufficiently privileged, patient attacker.
- Verification cost grows linearly with tenant history length; a very long-lived tenant's chain becomes progressively more expensive to fully re-verify from genesis (no checkpointing/anchoring mechanism exists in this codebase to bound that cost).
- A chain break tells you tampering happened *somewhere at or after* a given sequence number, not precisely *what* changed — forensic reconstruction of the original content still requires a separate backup/snapshot to diff against.

## Alternatives considered

**A cryptographically signed record per entry (no chaining), verified independently.** Rejected as the sole mechanism: a per-record signature detects that *record* being altered, but not a record being silently *deleted* from the middle of the sequence, which a hash chain does detect via the resulting break.

**External immutable ledger (blockchain-style, or a third-party audit-log-as-a-service).** Considered and deferred, not rejected outright: it would solve the "sufficiently privileged attacker" gap noted above more thoroughly than the current design, at the cost of an external dependency and integration complexity this codebase has not built. The object-storage-with-retention-lock design already documented in the domain package's own comment is the intended middle ground — implemented storage-level immutability without a new external system — and remains the next step, not yet taken.
