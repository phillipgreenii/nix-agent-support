# ADR 0001 — Governance: who and what may author or change behavior docs

- **Status:** Stub — to be decided
- **Date:** 2026-07-16

## Context

The behavior-docs method (`../behavior`) deliberately excludes governance: who or what may
author or change intended behavior, and what supervision applies. The method routes these
to the decision docs (they are realization decisions, not intended behavior). The behavior
docs keep only the identity principle that they are the living source of truth and that
change flows docs-down (`INV-15`). This ADR is the placeholder for the governance decision.

## Decision (to be decided)

Open points to settle:

- Humans own intended behavior; the extent to which an agent may assist in authoring.
- **Supervised vs. unsupervised** agent operation — the operational definition (approval
  gate? artifact? cadence?). Tracked as an open question in the behavior docs (`OQ-1`
  family / supervision).
- Conformance direction in practice: the method presumes the implementation at fault; who
  or what may correct the implementation, and the stop conditions, live here.

## Consequences

To be documented once decided. A companion guardrail skill will enforce whatever this ADR
settles; the behavior docs will not restate it.
