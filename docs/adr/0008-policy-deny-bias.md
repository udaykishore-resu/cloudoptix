# ADR-0008: Policy deny-bias

## Status

Accepted, implemented (and, per the note below, verified corrected against a stale claim in `policies/README.md` that the mechanism was broken).

## Context

A tenant's policy document (`govern.Policy`) is a list of rules, each with a `Match` selector and an `Effect`. More than one rule can legitimately match the same recommendation — a rule targeting `categories: [waste]` and a rule targeting `environments: [production]` might both match a waste finding in production. The engine has to decide, in that case, which rule's effect wins, and the order rules happen to be written in a YAML file must not be allowed to silently change the platform's safety posture.

## Decision

`govern.Evaluate` applies **deny-bias**: among every rule that matched, the most restrictive effect wins, using `Effect.Rank()` — `prohibit` (3) > `require_approval`/`advisory_only` (1/2) > `auto_execute` (0) — independent of file order. The seeded default (`policy.DefaultEffect`) applies only when *no* rule matched at all; it is tracked as a separate fallback value, not folded into the same restrictiveness comparison the matching rules go through, specifically so a legitimately matching, correctly-authored `auto_execute` rule is never suppressed by the *unrelated* fact that `default_effect` must always be at least `require_approval` (`Policy.Validate()` forbids `default_effect: auto_execute` outright). After the tenant's own rules are folded, a fixed set of platform invariants apply last and cannot be overridden by any policy: a destructive action is never auto-executed; nothing auto-executes while tenant automation is disabled; an exhausted, freeze-configured economic error budget hard-prohibits any cost-increasing change.

## Consequences

**Positive:**
- A permissive rule added later in a policy document — or one that happens to sort first — can never quietly widen a more restrictive rule that also matched. Safety posture is a property of *which rules match*, never of *authoring order*.
- The four platform invariants (destructive-action guard, automation-disabled guard, budget-freeze guard, min-confidence-for-production floor enforced at validation time) give CloudOptix's own reviewers, not just tenant policy authors, a backstop that cannot be defeated by a policy document, however it is written.
- The decision is fully reproducible: `govern.Evaluate(policy, input)` is a pure function, so a decision made months ago can be replayed exactly during an audit.

**Negative:**
- Deny-bias means a tenant cannot express "this specific narrow rule should override that broader restrictive one, even though it's more permissive" — the model has no escape hatch for "I really do mean this exception to win"; the only way to get a narrower permissive outcome is to narrow the *restrictive* rule's own `Match` so it stops matching the case that should be exempted.
- The interaction between `default_effect`'s required floor and matched-rule tracking is genuinely subtle enough that a stale documentation comment (`policies/README.md`'s "Known limitation" section) mistakenly described the mechanism as broken — see the note below. Subtlety that can fool even an author reading their own code is a real cost of this design, mitigated only by tests and by this documentation effort's own direct verification.

## A note this ADR exists partly to correct

`policies/README.md` claims, at length, that the rule-fold as originally conceived could only ever escalate toward more restrictive effects and could never select `auto_execute`, because the seeded `default_effect` (which validation forbids from being `auto_execute`) would always out-rank a matching `auto_execute` rule if the two were compared in the same fold. **That description does not match the current code.** `govern.Evaluate` tracks `matchedEffect` as a variable entirely separate from `fallback`, and only substitutes `fallback` in when `matched == false` — so a matching `auto_execute` rule, when it is the most restrictive *among matched rules*, wins outright, regardless of what `default_effect` is seeded to. This was verified directly: constructing a `govern.Input` that satisfies every guard on `balanced.yaml`'s `balanced.waste.non-production.auto` rule and evaluating it against that policy resolves to `Effect: auto_execute`, `DecidingRule: "balanced.waste.non-production.auto"`. See [`automation-spec.md`](../automation-spec.md) and the root README's [AI safety model](../README.md#the-ai-safety-model) section for the full write-up. This ADR documents the deny-bias mechanism as it actually is, not as the stale comment describes it.

## Alternatives considered

**First-match-wins (order-dependent).** Rejected: makes a policy document's safety behavior depend on the order a human happened to type rules in, an obviously fragile property for a document a compliance reviewer is meant to be able to read and trust.

**Most-permissive-wins.** Rejected outright — the opposite of the platform's entire safety posture; would mean a single overly-broad `auto_execute` rule could silently override every more careful restriction elsewhere in the document.

**No platform-level invariants, policy is the sole source of truth.** Rejected: would mean a tenant misconfiguring their own policy (accidentally or otherwise) could authorize auto-execution of a destructive action, with nothing left to stop it. The four invariants exist because a policy authored by a human, reviewed by another human, is still just a document — the invariants are the layer that does not trust the document alone.
