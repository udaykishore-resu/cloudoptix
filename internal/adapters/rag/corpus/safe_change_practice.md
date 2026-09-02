---
title: Safe Change Practice for Cost Optimization
source: safe_change
---

# Safe Change Practice for Cost Optimization

A cost optimization that causes an outage is not a saving; it is an incident
with a rebate. CloudOptix's execution engine is built around a small set of
safe-change practices, most of them borrowed directly from SRE change
management, applied specifically to cost-motivated changes.

## Every change carries a rollback plan before it runs

A recommendation is not executable until a rollback procedure exists for it.
For most infrastructure changes this means a pre-change snapshot of the
resource's configuration (and, where the action is destructive, its data);
for a resize it means the ability to resize back; for a policy or
configuration toggle it means recording the prior value. A change whose
undo path cannot be constructed is downgraded to advisory-only regardless of
its confidence or savings.

## Destructive actions never auto-execute

Deleting a volume, deregistering an AMI, deleting a snapshot, terminating an
instance, or removing an RDS replica or a NAT gateway are all irreversible
or expensive to reverse. These action types are excluded from
auto-execution as a platform invariant, independent of any tenant policy —
a policy author cannot opt back into it, by design.

## Maintenance windows and blast radius gate automation

An automated change against a production resource requires both a declared
maintenance window and a blast-radius calculation that accounts for what the
change could affect if the prediction is wrong — not just what it is
expected to affect if the prediction is right. Blast radius is computed by
walking the actual dependency graph discovered from the estate, never
estimated from the resource type alone; a resource whose dependents cannot
be determined (a partially discovered graph) is treated as higher risk, not
assumed safe.

## Validate after every change, not just before

A change that passes pre-flight checks can still regress the system it
touches — a rightsized instance that turns out to be CPU-bound under a
traffic pattern the observation window didn't capture, for example. Every
executed change has a validation window during which the relevant SLO
signals (latency, error rate, availability) are watched; a regression inside
that window triggers automatic rollback rather than waiting for a human to
notice.

## Confidence is earned, not asserted

A rule's predicted saving and its actual, observed effect are compared after
every execution, and this outcome history recalibrates the rule's stated
confidence going forward. A rule with a strong track record earns a higher
ceiling for what it is allowed to auto-execute; a rule whose predictions
have proven unreliable is throttled back to requiring approval even where
policy would otherwise permit automation. This is the same idea as an SRE
error budget: past reliability, not stated intent, is what earns autonomy.

## Segregation of duties on approval

Where a tenant's policy requires a distinct approver, the person who
requested a change may not also approve it. This is enforced structurally
in the approval workflow, not left to reviewer discipline.

## Economic error budgets can freeze changes outright

A cost SLO with an exhausted error budget can, if the tenant's breach
actions say so, freeze all further cost-increasing changes until the budget
window resets or the position is manually cleared — even changes that would
otherwise be policy-approved. This mirrors the standard SRE practice of an
exhausted reliability error budget freezing risky deploys, applied to
spend instead of availability.

## Change review reads plainly, not just numerically

A reviewer approving a change should be told, in one sentence, what will
happen if they approve — not only the diff. "Automation is enabled against a
production account with no approval requirement" is more useful to a
reviewer than the raw boolean it summarises, which is why every consequential
diff (a specification revision, a policy change, an execution plan) carries
a plain-language impact statement alongside its technical delta.
