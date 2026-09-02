# Copilot: a grounding failure, and what happens next

The five questions in [`questions-and-answers.md`](questions-and-answers.md)
all passed `GroundingVerifier` on the first draft. This file shows the other
path: a first-draft answer that does not ground cleanly, is regenerated
once, and — because the regeneration still can't fully ground one specific
figure — is returned with an explicit caveat rather than silently presented
as settled fact. See `docs/ai-spec.md`, SPEC-AI-003, for the mechanism this
demonstrates.

## The question

> "What will our total monthly AWS bill be next quarter if we do nothing?"

## Tool call

`get_cost_summary(window="30d")` — the only tool that actually applies here,
since no tool in the registry performs multi-month forecasting (see
`examples/copilot/README.md`'s tool table: cost, economics,
recommendations, resources, architecture graph, counterfactual, savings
funnel, and knowledge search — a forward projection three months out is not
among them).

```json
{
  "tool_call_id": "call_1",
  "name": "get_cost_summary",
  "result": {
    "window": "30d",
    "total_monthly_cost": "$185,978.41",
    "month_over_month_delta_pct": 2.1
  }
}
```

## First draft (fails grounding)

The deterministic provider's agentic-mode answer composer, working from
only this one tool result and a question asking for a number three months
out that no tool returned, produces:

> Based on current trends, your bill next quarter should be roughly
> **$569,847** if nothing changes, assuming the current 2.1% month-over-month
> growth rate continues.

`GroundingVerifier` checks every dollar figure in the draft against every
tool result returned in this exchange. `$185,978.41` and `2.1%` are both
present and ground cleanly. **`$569,847` is not present anywhere in any
tool result** — it is an extrapolation the answer-composition step
performed on its own (roughly `$185,978.41 × 1.021³`, three months of
compounding at the observed rate), and nothing in this conversation's tool
results states that figure directly. The verifier flags it as ungrounded.

## Regeneration

Per SPEC-AI-003, an ungrounded answer is regenerated once before falling
back to a caveated response. The regeneration attempt is given the same
tool results and explicitly told the prior draft's `$569,847` figure could
not be grounded. No new tool call is available to produce a genuinely
grounded three-month forecast — the tool registry has no forecasting tool,
a real structural gap, not a transient failure — so the regeneration
produces a second draft with the same fundamental problem: any specific
quarter-end dollar figure is necessarily an extrapolation no tool actually
returned.

## Final answer (caveated, not fabricated as settled fact)

Because the second attempt still can't ground a specific figure, the
platform does exactly what SPEC-AI-003 specifies for this case — return the
answer with the caveat stated plainly, rather than presenting an
unverifiable number as fact:

> I can tell you what's actually measured: your current monthly spend is
> **$185,978.41**, up 2.1% month-over-month. I don't have a tool that
> produces a genuine multi-month forecast, so I can't give you a grounded
> number for "next quarter" — any figure I gave you for three months out
> would be an extrapolation I can't verify against real data, not something
> I actually know. If a rough projection assuming the current 2.1% trend
> holds flat would still be useful with that caveat clearly attached, I can
> give you the arithmetic — but I want to be explicit that it's a
> back-of-envelope extrapolation, not a forecast grounded in anything the
> platform has actually modeled.

*Grounding check on the final answer: `$185,978.41` and `2.1%` are present in the `get_cost_summary` result and ground cleanly. No other dollar figure appears in the final text — the platform chose to name the gap rather than state an ungroundable number even with a caveat attached to it, which is a stricter behavior than SPEC-AI-003 strictly requires (a caveated ungrounded figure is permitted) but is what this specific answer-composition path produced when regeneration failed to find any tool-backed number to caveat.*

## Why this is the example worth including

Every other example in this directory shows the copilot answering something
it has real data for. This one shows what happens when a user asks a
perfectly reasonable question the platform genuinely cannot ground an
answer to — a forecasting capability that does not exist yet. The
honest response is "I don't have that, here's what I do have," delivered
mechanically by the grounding verifier's regenerate-then-caveat behavior,
not by the model electing to be humble. That distinction — a structural
guarantee versus a model's disposition — is the entire point of
`docs/ai-spec.md`'s design.
