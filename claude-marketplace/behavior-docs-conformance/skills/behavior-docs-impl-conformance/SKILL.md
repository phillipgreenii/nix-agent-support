---
name: behavior-docs-impl-conformance
description: The IMPL third of the behavior-docs conformance family — reconcile an IMPLEMENTATION against ITS OWN behavior-docs set. Use when asked to check whether the code matches the docs, whether an implementation still honours the invariants and interfaces its own `docs/behavior` set defines, whether its behavior-ID citations are live or stale, or which contract elements the code cites nowhere. Classifies every behavior-ID citation in the implementation as resolving locally, resolving through the imports table, framed as historical, or dangling; reports which contract elements are cited nowhere; reconciles the set's realization-gap register against the code in both directions (a stale row, an unrecorded divergence); and, for the behavior side, presumes the DOCS correct and the implementation at fault. The other two thirds are `behavior-docs-intra-conformance` (one set vs. the method's rules) and `behavior-docs-inter-conformance` (two sets reconciled across a seam). Do NOT use for a set in isolation, for a cross-set seam, or for general code review that is not about doc conformance.
---

# Behavior-docs impl-conformance

Verify that an **implementation** and **its own behavior-docs set** agree. The set states the
intended behavior at its floor; the implementation realizes it and, per the method's `INTF-1`, MUST
**cite the ID it implements**. This skill is the **impl**-evaluator. It is one of three parallel
evaluators, named for the concern each reconciles (never for a version number):

| Evaluator | Reconciles                          | Skill                                                                            |
| --------- | ----------------------------------- | -------------------------------------------------------------------------------- |
| **intra** | one set ↔ the method's rules        | [`behavior-docs-intra-conformance`](../behavior-docs-intra-conformance/SKILL.md) |
| **inter** | one set ↔ another system's contract | [`behavior-docs-inter-conformance`](../behavior-docs-inter-conformance/SKILL.md) |
| **impl**  | an implementation ↔ its own set     | this skill                                                                       |

## Arguments

- **set** — path to the behavior-docs set (a directory, usually `<product>/docs/behavior`).
- **impl** — path to the implementation root (usually the product directory that contains it).
- **focus** — optional: a specific `INV-*` / `INTF-*` (or family) to reconcile; default all.

## Core principle (read first)

**When the docs and the code disagree, the implementation is presumed at fault.** The set is the
source of truth for intended behavior (`INV-15`), so a divergence is a code finding by default. Two
consequences follow, and both are easy to get backwards:

- You **MUST NOT** "fix" a divergence by editing the set to describe what the code does. That
  launders a defect into intent. If the behavior the code has is the behavior that is wanted, that is
  a **human's** decision recorded as a docs change on its own terms — not a side effect of this pass.
- A **missing citation is not a divergence.** Citation retrofit is lazy, exactly as the UUID retrofit
  is, so an uncited invariant is a gap in coverage to work through, never a regression to block on.

## Step 1 — Mechanical citation reconciliation (deterministic)

Run **`scripts/impl-traces.sh <set> <impl>`**. It collects every behavior-docs ID the implementation
cites and classifies each, exiting non-zero iff any citation dangles:

| Class          | What it means                                                                                                                                                                                                                                                                                        | Rule               |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------ |
| **ok**         | resolves to a definition in this set — the citation is live                                                                                                                                                                                                                                          | `INTF-1`           |
| **external**   | resolves only through the set's `## External references` imports table: the code cites an element another set owns, and this set declares it                                                                                                                                                         | `INV-8`            |
| **decision**   | resolves to an entry in the product's own sibling decision area (`docs/decisions`, alongside `docs/behavior`) — a `DEC-`/`IMPL-` citation. That area is the **sibling input** of the two-input model, not an external set, so it needs **no** imports row; do NOT "fix" one of these by adding a row | `GOAL-5`           |
| **historical** | the citing line frames the ID as gone (former / removed / resolved / superseded). A set leaves **no tombstone** when an element is removed, so a dead ID legitimately resolves to nothing while the code still explains why it once existed                                                          | `INV-4`            |
| **FAIL**       | resolves to nothing and is not framed as historical: a stale citation, or an element of another set this set never declared                                                                                                                                                                          | `INTF-1` / `INV-8` |

An ID is excused as historical only when **every** occurrence is framed that way: one live citation
of a dead ID is a stale citation however many comments explain its history.

The script also reports **coverage** — invariants, interfaces and goals the implementation cites
nowhere — as a NOTICE. Stories and journeys are excluded: a user-facing arc is not something code
cites, so demanding a citation for one is noise.

## Step 2 — Obligation reconciliation via the conformance suite (executable)

A resolving citation proves the code names the right rule, not that it obeys it. The executable half
is the product's own **conformance suite** (for pr-pool, `packages/pr-pool/conformance`): it runs the
set's message schemas and golden examples against the implementation, so a schema or round-trip
failure is a genuine behavioral divergence. Report suite failures alongside the dangling citations —
they are the same finding class seen at two depths, and Step 1 alone cannot see this one.

## Step 3 — Behavior-level checks (read and judge)

These need reading, not grep. For each, cite the set element.

- **Each cited invariant is actually upheld** at the citing site — the citation names the rule the
  code is accountable to, so read whether the code does what the rule says.
- **Each interface's what-crosses / what-must-hold matches the real boundary** — argument and result
  shapes, error surface, and ordering guarantees.
- **Nothing below the floor has leaked upward into the set** — if the code needed a doc change to
  describe a realization detail, the change belongs downstream (`INV-2`/`INV-10`), and the set is
  wrong to have absorbed it.
- **A behavior the code has and the set does not describe** is either undocumented intent (a docs gap
  for a human) or unintended behavior (a code finding). Say which you think it is, and why.

## Step 3b — Reconcile the realization-gap register (read and judge)

The set's **realization-gap register** — its `## Realization gaps` section (`INV-23`) — is the
record of intended behavior the implementation has not yet built. Intra checks its **form**; **this
evaluator owns its truth**, because only a pass with running code in front of it can say whether the
record is accurate (`behavior-docs/docs/decisions · DEC-CONFORM-2`). Reconcile it both ways:

- **Each row against the code** — the divergence the row claims either still holds, or the code has
  caught up and the row is stale. A stale row is the one register finding that indicts the **docs**
  rather than the code, and it is a mechanical fix: delete the row.
- **Each divergence against the rows** — a divergence you found in Step 3 that no row records is an
  unrecorded gap. Report it as a row to add, naming the element id, what the docs require, and where
  the implementation stands. Do **not** instead soften the element to match the code: that launders a
  defect into intent, and the register exists precisely so the element never has to move.

A register that is **absent** is an `INV-23` finding intra already reports; note it and move on
rather than duplicating it.

## Step 4 — Report

Lead with a one-line verdict (conformant / N divergences / M dangling citations). Then list,
most-severe first: behavioral divergences, dangling citations, undeclared external citations,
realization-gap register rows to add or delete, then the coverage NOTICE. Cite the set element ID and the `file:line` for each. Separate **mechanical**
findings (fixable — a stale citation, a missing imports row) from **behavioral** ones (a human
decides).

## Corpus

[`corpus/impl/`](corpus/impl/) carries one fixture per classification against a shared `set/`:
`live/` (ok), `external/` (resolves through the imports table), `historical/` (framed as removed),
and `dangling/` (FAIL). The `test-behavior-docs-impl-conformance` bats check drives
`impl-traces.sh` over them under `nix flake check`, and `test-behavior-docs-real-corpus` drives it
over the real in-repo implementation so a stale citation in shipped code is visible.

## Red flags

- Editing the set to match the code → forbidden; the docs are the source of truth and a human owns
  intended behavior.
- Reading a coverage NOTICE as a failure → retrofit is lazy; an uncited invariant blocks nothing.
- Treating a `former X, resolved …` comment as a stale citation → that is the historical class; the
  set leaves no tombstone, so the code is the only place the history can live.
- Claiming conformance from Step 1 alone → a resolving citation proves the code names the rule, not
  that it obeys it. Run the conformance suite and read the citing sites.
- Recording a divergence by annotating the element, or by opening an `OQ-` for it → both are `INV-23`
  violations. A gap is settled intent the build has not reached, so it goes in the register as a row
  and nowhere else.
