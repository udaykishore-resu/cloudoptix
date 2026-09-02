# ADR-0003: Integer-micros money, never a float

## Status

Accepted, implemented.

## Context

Cloud economics multiplies very small unit prices (a Lambda GB-second at $0.0000166667, a per-request charge fractions of a cent) by very large usage quantities (millions of invocations, billions of requests). CloudOptix reports figures like cost-per-transaction down to the third decimal of a cent, and puts an SLO on that figure.

## Decision

`core.Money` (`internal/domain/core/money.go`) holds an exact monetary amount as integer micro-units of currency: `1 USD == 1,000,000 micros`. Every arithmetic operation stays in this integer domain (`MustAdd`, `MustSub`, `Scale`, `Div`, `Ratio`); conversion to a floating-point value happens only at a presentation boundary — rendering a UI string, serializing a chart data point — never mid-computation. Currency travels with every amount (`core.Currency`, `USD` today) because CloudOptix is multi-tenant, unlike an AWS payer account which bills in one currency.

## Consequences

**Positive:**
- No accumulation drift. Summing millions of line items in integer micros cannot drift the way repeated float addition can; a cost-per-transaction figure computed today and recomputed a year from now over the same inputs produces the identical result to the micro.
- `core.Money`'s custom JSON marshal/unmarshal means the wire format is unambiguous (an integer or a well-formed string, never a raw float that a client's own float parsing could round differently than the server did).
- Comparisons (`GreaterThan`, `LessThan`, `IsZero`, `IsNegative`) are exact integer comparisons — no epsilon-tolerance code anywhere in the money-handling surface.

**Negative:**
- `core.Money` has unexported fields (`micros`, `currency`) and no `MarshalYAML`/`UnmarshalYAML`, which is a genuine, confirmed gap: `policies/README.md` documents that `govern.Match.MaxMonthlySavingImpact` (typed `core.Money`) cannot currently be set from any policy YAML document loaded through `LoadPolicyYAML` — `gopkg.in/yaml.v3` errors outright rather than falling back to a JSON-compatible path. This is a real, load-bearing consequence of choosing a custom exact type over `float64`, not a hypothetical one — see [`automation-spec.md`](../automation-spec.md).
- Every new adapter or serialization boundary that touches money has to be deliberately taught this type; a `float64` field would have worked "for free" with more serialization libraries at the cost of the precision property that motivated this decision in the first place.

## Alternatives considered

**`float64` cents or dollars.** Rejected: the precision loss is not hypothetical at CloudOptix's actual scale (millions of GB-seconds, billions of requests) and shows up in exactly the digit the platform's headline product claim — cost per transaction — depends on.

**A third-party decimal library** (e.g. `shopspring/decimal`). Considered and rejected in favor of a small, purpose-built type: `core.Money` needs to carry a currency, support the specific operations the domain actually uses (`Scale`, `Ratio`, `Annualized`), and have full control over its JSON wire format — a general-purpose decimal library would still need a thin domain-specific wrapper around it, at which point the wrapper is most of the value and the dependency adds little. The trade-off this alternative would have most directly avoided — no YAML marshaller — remains an open gap regardless of which underlying decimal representation was chosen, since it is a missing method, not an inherent limitation of integer micros.
