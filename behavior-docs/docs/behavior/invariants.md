# Invariants — the behavior-docs method

The rules every behavior-doc set follows. See the [glossary](glossary.md) for terms
and the [README](README.md) for journeys and examples.

## Scope & placement

- **`INV-METHOD-1`** — A behavior-doc set **MUST** describe exactly **one** scope and
  live at that scope's `docs/behavior` (a repo → `<repo-root>/docs/behavior`; a
  tool → `<tool-root>/docs/behavior`).

## What a behavior doc contains

- **`INV-METHOD-2`** — A behavior doc **MUST** describe intended behavior only. It
  **MUST NOT** carry downstream detail (`file:line`, function/schema names, test
  names, tuning constants) or current-vs-past-vs-future code framing. The test: if it
  would change when the _implementation_ changes while the _intended behavior_ held
  constant, it does not belong.
- **`GOAL-METHOD-7`** — A set **SHOULD** show intent through examples (illustrative
  user-visible output, journeys) and **MAY** use counter-examples to sharpen the
  boundary. Examples are **illustrative** unless labeled **golden** (asserted by a
  real test).

## Invariant IDs

- **`INV-METHOD-3`** — Every invariant **MUST** have a stable ID; downstream artifacts
  (specs, designs, ADRs, tests) **MUST** cite the ID they implement or verify, so the
  `invariant → check` link survives after the disposable spec is discarded. A set
  tags each rule as an invariant (`INV-*`, absolute), a goal (`GOAL-*`,
  desired-not-absolute), or a concept.

## Living-by-default

- **`INV-METHOD-4`** — Every behavior doc is living; there is **no** per-doc status
  header. Debate is not merged — it stays in the proposing PR — and a change lands
  **only when there is agreement**, so what is merged is always the agreed expected
  behavior.

## Decisions & layering

- **`GOAL-METHOD-5`** — Adding or changing an invariant **SHOULD** reference an ADR
  recording the decision.
- **`GOAL-METHOD-6`** — Org/repo-specific behavior **SHOULD** live in a per-project
  overlay in that org's own repo; a **generic** set **MUST NOT** reference anything
  specific to a downstream deployment. The overlay imports the generic set by
  reference (cites its IDs), never by copying.

## Cross-set references

- **`INV-METHOD-8`** — A reference from one set to another **MUST** be a textual
  citation `<repo-name> · <set-path> · <ID-or-section>` (using the repository's
  directory/workspace name), never a relative-path markdown link. Relative links are
  used only **within** a single set.
