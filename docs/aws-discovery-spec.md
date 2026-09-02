# AWS discovery specification

Covers `SPEC-DSC-001..003`, implemented by `internal/domain/cloud`, `internal/application/discovery`, and `internal/adapters/aws/discovery` (23 per-service discoverers) plus `internal/adapters/awssim`'s simulated equivalent.

## SPEC-DSC-001 — The resource model

Every AWS service returns a differently-shaped object. `cloud.Resource` (`internal/domain/cloud/resource.go`) is the one normalized shape every discoverer produces: a typed `Kind` from a closed enum (`aws.ec2.instance`, `aws.rds.instance`, `aws.s3.bucket`, ...), a common capacity vocabulary, and a service-specific attribute bag. An unmodelled type produces `KindUnknown` plus a warning — never a silently mistyped resource. A new AWS service costs exactly one adapter and zero changes anywhere downstream (the twin, the optimization rules, the economics engine all read `cloud.Resource`, never a raw AWS SDK type).

`cloud.Topology` models relationships as typed edges (`RelationKind`): `contains` (structural ownership — cost flows down it), `runs_on` (placement), `routes_to` (request-path traffic), `depends_on` (runtime dependency, discovered from security groups/config or declared in the spec), `attached_to` (device attachment), `replica_of`, `produces_to`/`consumes_from` (async messaging), and `egress_via` (which NAT gateway or endpoint a resource's internet-bound traffic leaves through — the edge that makes NAT cost attributable to the workload that caused it, not the account that happens to own the gateway). `Topology.Consumers` normalizes a shared component's inbound `shared_by`/`runs_on`/`egress_via`/`attached_to` edges into per-consumer shares summing to one — the exact input Architecture Economics' indirect/shared split reuses rather than reimplementing (see [`architecture-economics-spec.md`](architecture-economics-spec.md)).

## SPEC-DSC-002 — Discovery orchestration

Discovery's job is to make a scan safe on a real, imperfect account: throttled, partially permitted, or interrupted halfway through. Four decisions carry that property (from `internal/application/discovery/doc.go`):

1. **One (service × region) job per unit of concurrency**, run through a bounded worker pool with its own retry loop. One service throttling, or one missing IAM permission, fails that one job — recorded with the exact denied action, never a generic error string — while every other job keeps running. "Scan the estate" is never treated as all-or-nothing.
2. **Scoped tombstoning.** The pass that marks resources absent (not seen this scan) is scoped by construction to exactly the (kind, region) pairs a *successful* job actually covered this run. A failed job contributes zero kinds to that region's tombstone set — its resource kind is left untouched, not deleted for having gone unobserved. The failure mode "a partial scan wipes half the estate" is not a bug to avoid; it is a state the code cannot reach, because the (kind, region) pairs `MarkAbsent` is ever called with are read from the same coverage map only successful jobs write into.
3. **Exponential backoff with full jitter**, only for errors `core.Retryable` classifies as transient (throttling, timeouts, dependency unavailability). A permission error is never retried — no amount of waiting fixes a missing IAM action — so it fails fast and reports the denied action immediately, for the onboarding flow to act on rather than after four wasted attempts.
4. **Attribution resolved once per run** from three sources in trust order (see SPEC-DSC-003).

`ResourceDiscoverer` (`internal/ports/services.go`) is the per-service interface: `Service()`, `Kinds()` (drives what `MarkAbsent` is allowed to touch), `RequiredActions()` (drives both the generated onboarding IAM policy and the permission probe), and `Discover(ctx, DiscoveryInput) (DiscoveryOutput, error)`. `DiscoveryOutput` reports `APICalls`, `Throttled`, and `Warnings` alongside the resources found, so a discovery run's own efficiency is visible, not just its result.

23 discoverers exist in `internal/adapters/aws/discovery` (EC2, ASG, EBS via EC2, RDS, DynamoDB, S3, Lambda, ECS, EKS, ELBv2, CloudFront, API Gateway v2, ElastiCache, SQS, SNS, EventBridge, CloudWatch Logs, KMS, Secrets Manager, Resource Groups Tagging API, and others), each independently unit-tested.

## SPEC-DSC-003 — Attribution

Attribution (environment, application, workload, owner, criticality) is resolved once per run from three sources, in trust order:

1. A recognised tag on the resource itself.
2. A `cloud.AttributionRule`, evaluated in priority order — first match wins.
3. The onboarded account's own declared environment, as the weakest fallback.

Every attributed field records which source won via `core.Provenance`, so a downstream engine — and a human looking at the twin — can see "this is confirmed" versus "this is an account-level guess," rather than one undifferentiated field. See the root README's [Architecture Economics worked example](../README.md#worked-example-the-checkout-capability) for a real instance of this: in one run of the demo estate, `checkout-webhook`, `shopfleet-cart-events-provisioned`, and `shopfleet-checkout-dlq` came up with no `Application` tag at all, and were still correctly attributed via name-pattern inference rather than falling into the unattributed remainder — with their provenance visibly marked `INFERRED`, not silently treated as equally certain as a tagged resource.

## Permission scopes used

Every discovery API call uses only `ScopeRead`/`ScopeAnalyze`-tier sessions (see [`security-spec.md`](security-spec.md)) — discovery never obtains an execute-scoped session, structurally, because nothing in `ResourceDiscoverer`'s signature gives it one.

## Current limitations

- Discovery has been exercised only against `internal/adapters/awssim`'s single-account, single-region demo estate. The (service × region) worker-pool design is built for multi-account/multi-region breadth; that breadth is untested.
- The real AWS discoverers (`internal/adapters/aws/discovery`) are unit-tested against mocked/recorded AWS SDK responses, never against a live account.
- `cloud.AttributionRule` priority-order evaluation is implemented and covered by discovery's own tests, but has not been exercised against the scale or variety of tagging conventions a real multi-team organization would present.
