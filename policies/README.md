# CloudOptix policy packs

This directory ships four policy documents — `conservative.yaml`,
`balanced.yaml`, `aggressive.yaml`, `regulated.yaml` — plus this README. Each
is a real `govern.Policy` YAML document, loadable as-is through
`governance.Service.LoadPolicyYAML` (see
`internal/application/governance/service.go`), the same entry point a
tenant's own uploaded policy document goes through. A tenant picks one pack
as a starting point during onboarding, or writes their own from scratch
using the same schema; nothing about the schema or the evaluation engine
treats a shipped pack specially.

## The model, in one paragraph

A `Policy` is a versioned, tenant-scoped list of `Rule`s plus a
`default_effect`. Each rule has a `match` (a selector: which actions,
categories, environments, accounts, regions, tags, and numeric guards —
confidence, risk, blast radius, reversibility — a recommendation must
satisfy) and an `effect` (`auto_execute`, `require_approval`, `prohibit`, or
`advisory_only`). `govern.Evaluate(policy, input)` is a pure function: given
a policy and a structured fact set describing one recommendation, it
produces a `Decision` — deterministically, with no AI, no prose, and no
side effect — that a human or an audit can replay from the same inputs
months later and get the identical answer.

## Evaluation order

1. Every rule whose `match` is satisfied by the input is collected.
2. Among rules that matched, the **most restrictive** effect wins —
   `prohibit` beats `require_approval` beats `advisory_only` beats
   `auto_execute` — regardless of the order rules appear in the file. This
   is the deny-bias: a permissive rule added later in the document, or one
   that happens to be listed first, can never quietly widen a more
   restrictive rule that also matched.
3. If no rule matches, `default_effect` applies. Every pack in this
   directory sets `default_effect: require_approval` — the platform refuses
   to even save a policy whose default is `auto_execute` (see "Platform
   invariants" below), on the reasoning that an unmatched action is by
   definition one nobody has written a rule for yet, and CloudOptix does
   not act alone on the untested case.
4. A short, fixed set of **platform invariants** are applied last, after
   every tenant rule, and cannot be overridden by any policy document:
   - A destructive action (one that deletes state no snapshot can recreate
     — see `optimize.ActionType.Destructive()`) is never auto-executed,
     however a policy is written.
   - Nothing auto-executes while the tenant's own specification has
     automation disabled (`spec.Automation.Enabled == false`).
   - An exhausted economic error budget freezes every cost-increasing
     change outright; a budget under pressure but not exhausted downgrades
     `auto_execute` to `require_approval`.

## Platform invariants a policy cannot weaken

`govern.Policy.Validate()` refuses to save or activate a policy that tries
to:

- Set `default_effect: auto_execute`.
- Give `auto_execute` to a destructive action (see the list above).
- Write an `auto_execute` rule with no `match.actions`, `match.categories`,
  or `match.rule_ids` at all — an auto-execute rule must name what it
  permits, not match everything.
- Give `auto_execute` reach into production (or leave `match.environments`
  empty, which reaches every environment including production) without
  `match.min_confidence >= 0.85`.

These four checks are `SeverityCritical`/`SeverityHigh` validation issues,
which `ValidationResult.HasBlocking()` treats as blocking — a policy that
trips one of them cannot be saved, not merely warned about. Every
`auto_execute` rule in `aggressive.yaml` and `balanced.yaml` is written to
clear all four bars deliberately, not by accident of the specific numbers
chosen.

## The four packs, and when to pick one

| Pack | `auto_execute` at all? | Production posture | Segregation of duties |
|---|---|---|---|
| `conservative.yaml` | Never | 2 approvals, distinct approvers | Yes, production only |
| `balanced.yaml` (default) | Unambiguous waste, non-production only | 1 approval | No |
| `aggressive.yaml` | High-confidence, fast-reversible changes, even in production, inside a maintenance window | 1 approval outside those narrow conditions | No |
| `regulated.yaml` | Never | 2 approvals, distinct approvers, **everywhere** (not only production) | Yes, everywhere, plus a tag-based change-freeze prohibition |

`balanced.yaml` is the platform default. `conservative.yaml` and
`aggressive.yaml` are opposite widenings/narrowings of it along the same
axis (how much unattended action CloudOptix is trusted with).
`regulated.yaml` is not simply "more conservative than conservative" — it
adds two controls the other three packs do not attempt at all: a
tag-triggered change freeze, and approval-role segregation of duties in
*every* environment, which a compliance framework auditor typically wants
to see regardless of an environment's actual blast radius.

## How to write a rule

A rule is a `match` plus an `effect`. Start from the most specific
selector that is actually true of the change class you mean, not the
broadest one that happens to also match it — `match.categories: [waste]`
is looser than `match.actions: [stop_instance, release_elastic_ip]`, and a
reviewer six months from now benefits from the narrower one saying exactly
what it permits.

A few conventions this directory's packs follow, worth keeping if you add
your own rule:

- **Give every rule a stable, dotted `id`** (`pack.category.qualifier`) —
  `Decision.DecidingRule` records which rule id decided a given
  recommendation, and that id is what shows up in the audit trail and in
  `Simulate`'s diff against a previously-active policy.
- **State the destructive-action and advisory-only rules explicitly**, even
  though the platform enforces both invariants on its own. A policy
  document is read by a human reviewer (and, for `regulated.yaml`, an
  auditor) who should not have to trust an invariant they cannot see stated
  anywhere in the file they are signing off on.
- **Put a `reason` on every rule.** `Decision.Reason` surfaces it back to
  whoever is looking at why a specific recommendation landed where it did;
  an empty reason falls back to the rule's `description`, which is usually
  written for a different audience (the policy author, not the decision's
  eventual reader).
- **Guard `auto_execute` with `min_reversibility` and `max_critical_services`
  in addition to `min_confidence`.** Confidence alone answers "how sure is
  the model", not "how bad is it if the model is wrong" — the second
  question is what reversibility and blast-radius guards answer, and an
  autonomy rule should answer both before it is allowed to act alone.

## A schema gap this directory works around: expressing a change freeze

`govern.Match` has selectors for actions, categories, environments,
accounts, regions, resource kinds, applications, and tags, plus numeric
guards — but nothing that means "not during this time window." A
`MaintenanceWindow`-style guard (`match.require_maintenance_window`)
expresses the opposite: an *allowed* window, used to gate `auto_execute`,
not a prohibition outside one. `regulated.yaml` models a change freeze as a
tag instead (`tag_selector: {change_freeze: active}`, `effect: prohibit`):
an operations team applies the tag for the freeze's duration and removes it
afterward, which is also the more honest match for what this schema can
express today — a scheduled freeze *calendar* would need a schema change
(a new `Match` field, or a new input fact the freeze calendar itself
computes), not a cleverer rule.

## A YAML schema gap: `match.max_monthly_saving_impact` does not work

`govern.Match.MaxMonthlySavingImpact` is typed `core.Money`, a struct with
unexported fields (`micros`, `currency`) and a custom `MarshalJSON` /
`UnmarshalJSON` pair — but no `MarshalYAML` / `UnmarshalYAML`. Confirmed
directly: `yaml.Unmarshal` into a `core.Money` field from a plain YAML
scalar (`max_monthly_saving_impact: 5000`) fails outright —
`gopkg.in/yaml.v3` has no exported field to populate and no custom
unmarshaler to fall back to, so it errors rather than silently zeroing the
value. Concretely, this means **no policy document loaded through
`LoadPolicyYAML` can currently set this guard at all** — attempting to
set `max_monthly_saving_impact` on any rule breaks parsing for the whole
document. None of the four packs in this directory use that field for
exactly this reason. Fixing it needs a `core.Money` YAML
(un)marshaler analogous to the existing JSON one, or a different type for
this one field (e.g. a plain float of USD major units) — either is an
`internal/domain` change, outside what this policies directory can work
around on its own.

## `auto_execute` is reachable, and the guards that stop it are separate

An earlier revision of this README documented a bug that made
`auto_execute` unreachable: `govern.Evaluate` seeded its running effect from
`default_effect` and then only let a matching rule *raise* that effect's
rank. Since `EffectAutoExecute` has the lowest rank and `Policy.Validate`
forbids `default_effect: auto_execute`, a matching `auto_execute` rule could
never become the deciding effect. That is fixed: `Evaluate` now tracks the
most restrictive effect *among the rules that matched* separately from the
fallback, and applies the default only when nothing matched at all. Its own
comment explains why the two have to be tracked apart.

What still stops an `auto_execute` decision, deliberately, is a short list
of guards applied *after* tenant policy — so no pack in this directory, and
no pack a tenant writes, can override them:

- **Destructive actions.** `optimize.ActionType.Destructive()` names them
  (terminate instance, delete volume, delete snapshot, deregister AMI,
  remove RDS replica, remove NAT gateway). A rule naming one under
  `auto_execute` fails `Policy.Validate` outright, and even a policy that
  somehow bypassed validation is downgraded to `require_approval` by
  `__platform_destructive_guard__` at evaluation time.
- **The tenant's own master switch.** `automation.enabled: false` in the
  approved specification downgrades every `auto_execute` to
  `require_approval` via `__tenant_automation_disabled__`.
- **An exhausted economic error budget**, which freezes cost-increasing
  changes outright and escalates the rest.

Each is tested in `tests/integration/ai_safety_test.go`, including the
control case that proves the guard — rather than a rule failing to match —
is what produced the refusal.

