# ADR 0001 — Governance: who and what may author or change behavior docs

- **Status:** Stub — to be decided
- **Date:** 2026-07-16

## Context

The behavior-docs method (`../behavior`) **splits** governance. The **authority** slice — who or
what may author, approve, or change intended behavior, and what supervision applies — is a
realization decision, routed here to the decision docs. A described system's **behavioral**
governance (an identity, permission posture, or non-defeatable guardrail it treats as intended
behavior) instead stays in that system's behavior docs (see `../behavior/actors.md`). The method's
own product has no behavioral governance posture, so all of _its_ governance is authority and
lives here. The behavior docs keep the identity principle that they are the living source of truth
and that change flows docs-down (`INV-15`). This ADR is the placeholder for that authority
decision.

## Decision (to be decided)

Open points to settle:

- Humans own intended behavior; the extent to which an agent may assist in authoring.
- **Supervised vs. unsupervised** agent operation — the operational definition (approval
  gate? artifact? cadence?). This is an **authority** gap, out of the behavior set's extent,
  so it is tracked here by this ADR — not by an in-set open question.
- Conformance direction in practice: the method presumes the implementation at fault; who
  or what may correct the implementation, and the stop conditions, live here.

## Consequences

To be documented once decided. A companion guardrail skill will enforce whatever this ADR
settles; the behavior docs will not restate it.
