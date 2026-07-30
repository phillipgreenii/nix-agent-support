# Governance — decision docs for the behavior-docs method

Realization decisions about the **authority** slice of governance: who or what may author,
approve, or change intended behavior, and what supervision applies.

### `IMPL-1` — Governance: who and what may author or change behavior docs <!-- uuid: 373ec369-1310-4f4e-be63-14d7b3bef642 -->

_Captured 2026-07-16. Not yet decided — the `IMPL-` prefix carries that; promotion to `DEC-GOV-1`
will preserve the UUID._

## Context

The behavior-docs method (`../behavior`) **splits** governance. The **authority** slice — who or
what may author, approve, or change intended behavior, and what supervision applies — is a
realization decision, routed here to the decision docs. A described system's **behavioral**
governance (an identity, permission posture, or non-defeatable guardrail it treats as intended
behavior) instead stays in that system's behavior docs (see `../behavior/actors.md`). The method's
own product has no behavioral governance posture, so all of _its_ governance is authority and
lives here. The behavior docs keep the identity principle that they are the living source of truth
and that change flows docs-down (`INV-15`). This entry is the placeholder for that authority
decision.

**Open points to settle**

- Humans own intended behavior; the extent to which an agent may assist in authoring.
- **Supervised vs. unsupervised** agent operation — the operational definition (approval
  gate? artifact? cadence?). This is an **authority** gap, out of the behavior set's extent,
  so it is tracked here by this entry — not by an in-set open question.
- Conformance direction in practice: the method presumes the implementation at fault; who
  or what may correct the implementation, and the stop conditions, live here.

## Consequences

To be documented once decided. A companion guardrail skill will enforce whatever this entry
settles; the behavior docs will not restate it.
