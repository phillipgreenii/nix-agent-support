# Invariants — the behavior-docs method

The rules every behavior docs set follows. See the [glossary](glossary.md),
[actors](actors.md), [interfaces](interfaces.md), and [journeys](journeys.md). Numbers are
stable and grouped topically; gaps and out-of-sequence numbers are legal.

## Scope

- **`INV-1`** — A behavior docs set describes exactly **one scope**. By convention it lives
  at that scope's `docs/behavior`.
- **`INV-11`** — A behavior docs set's **extent** is exactly what its user stories and
  journeys require, so it MUST include user stories and journeys; establish scope by writing
  them. Nothing is in scope that no story or journey needs.
- **`INV-10`** — A behavior docs set speaks at its scope's **floor** and MUST NOT descend
  below it (below is relative to scope), located by the **substitution test**: generalize a
  term unless doing so loses essential meaning within the extent.
- **`INV-13`** — A behavior docs set MUST make its **scope explicit** (extent + floor),
  include its **user stories and journeys**, and define **all its actors** and **all its
  interfaces**; known gaps MUST be recorded as open questions. Completeness is judged
  against an implementer being able to _locate_ what it needs; unknown gaps surface as holes
  in this structure.

## What a behavior doc contains

- **`INV-2`** — A behavior doc describes **intended behavior only** — no _how_, and no
  past/present/future-code framing. The test: if it would change when the implementation
  changed while intended behavior held, it does not belong.
- **`GOAL-7`** — A behavior docs set SHOULD show intent through **examples**; an example is
  an **illustrative example** unless it is a **golden example** (asserted by a real test).

## Identity, IDs, and change

- **`INV-15`** — The behavior docs set is the **source of truth** for intended behavior.
  Change flows **docs-down**: edit the docs first; the implementation re-converges to them,
  never the reverse. A **realization gap** — intended behavior the implementation has not yet
  built — is therefore normal, not a defect in the docs; it is tracked against the cited IDs,
  never annotated inline (the docs stay living, `INV-4`).
- **`INV-3`** — Every invariant, goal, user story, journey, interface, and actor MUST carry
  a **typed, stable ID** (`INV-`, `GOAL-`, `STORY-`, `JOURNEY-`, `INTF-`, `ACTOR-`; open
  questions use `OQ-`). Downstream artifacts MUST cite the ID they implement or verify;
  across behavior docs sets an ID is cited as `<repo-name> · <set-path> · <ID>`. A set that
  **cites another set** SHOULD namespace its own IDs by topic (`INV-DISP-1`, `INTF-SOURCE`)
  so a bare ID it cites never collides with one of its own; the **root** set — this one,
  which cites no other — keeps bare numeric IDs.
- **`INV-4`** — Every behavior doc is **living**: no per-doc status header, and unresolved
  debate is not baked in — what is written is the current intended behavior.

## Interfaces & consistency

- **`INV-8`** — A cross-product interaction MUST be defined as an **explicit interface** — its
  field-shape and what-must-hold stated — with no implicit interpretation. Between two **peers**
  (each keeping its own set) each product defines the interface on its own side (what it sends
  **out** and takes **in**), and this duplication is deliberate. Where one side is an
  **implementer** of the other's contract, that side **cites** the owner's interface and states
  only its own obligations, rather than restating the contract.
- **`INV-18`** — A behavior docs set MUST be **inter-consistent** at every interface; how
  agreement is reconciled follows the **counterparty**'s kind. With a **peer** (both sides keep a
  set), the outgoing interface MUST match the peer's incoming interface and vice versa,
  cross-checked against the deliberate duplication of `INV-8`. With an **implementer** — a set or a
  pluggable implementation that realizes the contract, and which often keeps **no** set of its own
  — agreement is reconciled by a **conformance suite** the implementer runs, not by a verbatim peer
  cross-check.
- **`INV-12`** — A behavior docs set MUST be **self-consistent**: its parts MUST NOT
  contradict. Behavior docs are intentionally not DRY, so restatements exist to be compared;
  a surfaced contradiction is a defect a human resolves.
- **`INV-14`** — A **named concept** MUST be _used_ in at least two places beyond its
  glossary definition, so its uses can be compared to surface inconsistency; a definition
  alone does not satisfy it. _(Exactly what counts as a named concept is an open question,
  `OQ-1`.)_
- **`INV-19`** — A set **MAY** declare a **precedence** ordering over its own invariants so a
  genuine conflict between two of them resolves the same way every time, and a reader can trust
  which rule wins (`STORY-7`). A newly-surfaced precedence conflict MUST be recorded as an open
  question and settled by a decision doc (`JOURNEY-3`) — never chosen ad hoc by an agent — and a
  downstream set **MAY** cite an owning set's ordering rather than restate it.

## Goals

- **`GOAL-16`** — **Adherence defines validity**: a product adhering to the behavior docs is
  behaviorally valid. The two-input model routes the fix — behavioral defects ⇒ fix the
  behavior docs; non-behavioral defects ⇒ fix the decision docs.
- **`GOAL-5`** — Adding or changing an invariant SHOULD reference a **decision doc** (ADR).
- **`GOAL-17`** — The behavior docs set, with the decision docs, SHOULD suffice to
  **regenerate a behavior-conformant implementation** (not byte-identical); it outlives any
  implementation.
