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
  never the reverse.
- **`INV-3`** — Every invariant, goal, user story, journey, interface, and actor MUST carry
  a **typed, stable ID** (`INV-`, `GOAL-`, `STORY-`, `JOURNEY-`, `IF-`, `ACTOR-`; open
  questions use `OQ-`). Downstream artifacts MUST cite the ID they implement or verify;
  across behavior docs sets an ID is cited as `<repo-name> · <set-path> · <ID>`.
- **`INV-4`** — Every behavior doc is **living**: no per-doc status header, and unresolved
  debate is not baked in — what is written is the current intended behavior.

## Interfaces & consistency

- **`INV-8`** — A cross-product interaction MUST be defined as an **explicit interface** —
  its structure and shape stated — with no implicit interpretation. Each interacting product
  defines the interface on its own side (what it sends **out** and takes **in**); this
  duplication is deliberate.
- **`INV-18`** — A behavior docs set MUST be **inter-consistent**: its outgoing interface to
  another product MUST match that product's incoming interface, and vice versa. The
  deliberate duplication in `INV-8` exists so this agreement can be cross-checked.
- **`INV-12`** — A behavior docs set MUST be **self-consistent**: its parts MUST NOT
  contradict. Behavior docs are intentionally not DRY, so restatements exist to be compared;
  a surfaced contradiction is a defect a human resolves.
- **`INV-14`** — A **named concept** MUST be _used_ in at least two places beyond its
  glossary definition, so its uses can be compared to surface inconsistency; a definition
  alone does not satisfy it. _(Exactly what counts as a named concept is an open question,
  `OQ-1`.)_

## Goals

- **`GOAL-16`** — **Adherence defines validity**: a product adhering to the behavior docs is
  behaviorally valid. The two-input model routes the fix — behavioral defects ⇒ fix the
  behavior docs; non-behavioral defects ⇒ fix the decision docs.
- **`GOAL-5`** — Adding or changing an invariant SHOULD reference a **decision doc** (ADR).
- **`GOAL-17`** — The behavior docs set, with the decision docs, SHOULD suffice to
  **regenerate a behavior-conformant implementation** (not byte-identical); it outlives any
  implementation.
