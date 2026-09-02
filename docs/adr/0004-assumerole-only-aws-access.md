# ADR-0004: AssumeRole-only AWS access

## Status

Accepted, implemented.

## Context

CloudOptix needs read, analyze, and (optionally) mutation access to a customer's AWS account. The two broad approaches an integration like this typically chooses between are: accept and store long-lived credentials (an IAM user's access key pair) the customer generates for CloudOptix, or use AWS's own cross-account role-assumption mechanism.

## Decision

`internal/adapters/aws/sts` is the platform's only path onto a customer account, and it is built so that accepting a static credential is not merely disallowed but structurally impossible: no function, constructor option, or struct field anywhere in the package accepts an AWS access key ID or secret access key. Every credential CloudOptix holds for a customer account is minted by `sts:AssumeRole` against CloudOptix's own control-plane identity, scoped to exactly one `(account, cloud.RoleScope)` pair at a time — four separate IAM roles (`read`, `analyze`, `plan`, `execute`) rather than one wide role with four permission checks — and every `AssumeRole` call carries a CloudOptix-generated, per-account `ExternalID`.

## Consequences

**Positive:**
- No stored customer credential exists anywhere in CloudOptix to leak, rotate, or accidentally log. A reviewer can verify this by grepping the package for `AccessKeyId`/`SecretAccessKey` as an input parameter, rather than trusting a policy statement.
- The confused-deputy attack (a malicious or compromised third party assuming a customer's trust policy meant for CloudOptix) is defeated by the per-account `ExternalID`, a standard, well-understood AWS mitigation for exactly this pattern.
- Granular scope: a tenant that only wants visibility can create the `read`/`analyze` roles and never the `execute` role, and CloudOptix has no code path that can obtain execute-scoped access without that role existing — `Broker.Assume` fails the same way AssumeRole itself would.
- Every CloudOptix action is attributable in the customer's own CloudTrail, via a `RoleSessionName` built from the CloudOptix principal and requesting scope — not an anonymous `AssumeRole` line.
- A customer revokes access unilaterally, at any time, by deleting or modifying the trust policy on their side — no coordination with CloudOptix required.

**Negative:**
- Every mutating and read operation carries the latency and complexity of session assumption and refresh (mitigated by per-`(account, scope)` session caching and proactive refresh, with concurrent-refresh coalescing to avoid an STS stampede).
- Onboarding requires the customer to create four IAM roles with correctly-scoped trust policies and permission sets before CloudOptix can do anything — a heavier initial setup than "paste an access key," though `GET /aws-accounts/{id}/instructions` exists specifically to make that setup mechanical.
- CloudOptix's own control-plane identity (the base credential `Broker` assumes *from*) becomes a single point of trust; its own compromise is a materially worse event than the compromise of one tenant's stored key would have been under the alternative, since it is the root of every tenant's access.

## Alternatives considered

**Stored access keys per tenant.** Rejected outright — this is exactly the shape of integration that makes a customer's security team the most nervous, and for good reason: a stored, long-lived credential is a permanent liability the moment it exists, regardless of how carefully it is encrypted at rest. AssumeRole sessions are temporary by construction and never persisted past their TTL.

**A single wide IAM role with internal permission checks (rather than four separate roles).** Rejected: a permission check CloudOptix enforces on itself is not a security boundary a customer can independently verify or revoke — a bug in that internal check would silently grant execute-level access the customer believed they had withheld. Four separate IAM roles make the boundary something the *customer's own IAM policy*, not CloudOptix's application code, enforces.
