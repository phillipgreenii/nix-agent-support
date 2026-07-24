# Journeys & open questions — the behavior-docs method

User stories, the lifecycle journeys, and open questions. Stories and journeys carry IDs so
downstream can cite them (`INV-3`); together they establish the scope's extent (`INV-11`),
so every behavior docs set includes them.

- **Story shape** — the standard role–capability–benefit form: `As <actor>, I want
<capability>, so that <outcome>`.
- **Journey shape** — an ID, a one-line intent, the actor(s), and the flow (prose, or
  Given/When/Then scenarios; a diagram when it branches).

## User stories

**Author**

- **`STORY-1`** <!-- uuid: 95f8c8e5-7cd2-4979-a7c5-bc9099178f9d --> — As an **author**, I want one place that answers "how is this supposed to
  behave?", so I don't re-derive intent from code or stale specs.
- **`STORY-2`** <!-- uuid: 7ac93c27-f9a4-459a-b1a7-108facac1585 --> — As an **author**, I want to state new intent first and derive the work
  from it, so a change anchors to a durable rule, not a throwaway spec.
- **`STORY-3`** <!-- uuid: e824087b-5ee6-4ab8-87a8-2711f35fecad --> — As an **author** adopting behavior docs on an existing product, I want to
  derive a first behavior docs set from what already exists, so I don't start from a blank
  page.
- **`STORY-8`** <!-- uuid: a725f994-235b-4940-9c8f-1c1a974062dc --> — As an **author**, I want a clear rule for what is at fault when a product
  misbehaves, so I know whether to fix the behavior docs or the decision docs.
- **`STORY-9`** <!-- uuid: b80977af-7a2f-47ae-987d-7ae344d608ac --> — As an **author** whose system interacts with another product, I want each
  interface defined and **reconciled** for agreement (inter-consistency) — cross-checked with a
  peer, or verified by a conformance suite where the other side merely implements my contract —
  so integrations don't silently drift.

**Implementer**

- **`STORY-4`** <!-- uuid: 5ae35f53-5599-419c-982e-d893c89fe677 --> — As an **implementer**, I want a source of truth to resolve uncertainty
  against, and to locate and classify a gap rather than guess. _(the method's north-star)_
- **`STORY-5`** <!-- uuid: c35a453b-f6b8-4311-8a0a-64e5049bf03d --> — As an **implementer**, I want a stable ID to cite from a test or decision
  doc, so the `intent → check` link outlives the spec that introduced it.
- **`STORY-6`** <!-- uuid: 89aeb9aa-2003-43cb-98bf-7788af4fe956 --> — As an **implementer** re-platforming, I want to regenerate a
  behavior-conformant implementation from the behavior docs + decision docs.
- **`STORY-7`** <!-- uuid: 05c396f9-0466-4937-8032-f6b0611da321 --> — As an **implementer**, I want the behavior docs to be self-consistent and
  cross-checkable — and, where two rules genuinely tension, resolved by a declared **precedence**
  — so I can trust which rule wins when I read them.

## Journeys

### `JOURNEY-1` — Starting a behavior docs set <!-- uuid: 8fc13b60-a1c6-4869-bc0a-2b52b12b6e52 -->

Establish the scope by writing what you already know. Draft the user stories and journeys
first — their union _is_ the extent (`INV-11`). Capture the rules they imply as invariants
and goals, name the actors and interfaces they touch, and set the floor with the
substitution test. Record every unknown as an open question.

```mermaid
flowchart TD
    sj["draft stories + journeys (these define the extent)"] --> rules["capture invariants/goals; name actors + interfaces"]
    rules --> floor["set the floor (substitution test)"]
    floor --> gaps["record unknowns as open questions"]
```

### `JOURNEY-2` — Changing intended behavior <!-- uuid: 5497015a-4537-4d18-a7c7-dface48365ea -->

_Actor:_ `ACTOR-1` (Author) — a change of intent originates here.

Edit the behavior docs first to state the new intended state. Downstream (spec → design →
plan) is re-derived from the change and thrown away on re-convergence. Record a decision doc
(ADR) if the change is consequential. Until the implementation catches up, the difference is a
normal **realization gap** (`INV-15`) tracked against the changed IDs — not a status header on
the doc (`INV-4`).

### `JOURNEY-3` — Resolving an open question <!-- uuid: 20083081-2735-4ab4-8a65-d56d59a8a1e7 -->

_Actor:_ `ACTOR-1` (Author) — owns the question and its resolution.

Decide → state the decision in the docs → record a decision doc if consequential → delete
the question. A question is a placeholder for a gap, not a home for debate.

### `JOURNEY-4` — Propagating a contract change across a reference seam <!-- uuid: b7351f46-b8bc-4fff-bc52-54ec6aa578bf -->

<!-- uuid: 0d9f88cb-d020-43ac-a87e-c415db77073e -->

_Actor:_ `ACTOR-1` (Author) of the owning set.

An owner edits its contract (`JOURNEY-2`). It does **not** notify consumers — an owner does not
know its implementers (`INV-3`). Each consumer instead **re-converges by pull**: it re-runs its
conformance suite (`INV-18`) against the owner's _current_ contract, matching cited elements by
UUID (`INV-3`). A reconciliation failure is a **realization gap** (`INV-15`) on the consumer's
side, tracked against the cited elements — not a status header (`INV-4`). The conformance suite
versions with the contract, so the pull always checks against the latest owner state. This is the
same **level-triggered** re-convergence the docs use everywhere: no push, no notification — each
side pulls the current truth from the reference seam.

```mermaid
flowchart TD
    edit["owner Author edits its contract (JOURNEY-2)"] --> nonotify["owner does NOT notify consumers (INV-3)"]
    nonotify --> pull["each consumer re-runs its conformance suite vs the owner's current contract (INV-18), matching by UUID (INV-3)"]
    pull --> ok{"reconciles?"}
    ok -->|yes| done["converged"]
    ok -->|no| gap["realization gap (INV-15) on the consumer, tracked against the cited elements"]
    gap --> fix["consumer re-converges docs-down"]
    fix --> pull
```

## Open questions

Each open question states the gap, its owner, a resolution path, and where it blocks.

- **`OQ-1` — What counts as a "named concept" for `INV-14`?** <!-- uuid: 6b9cb74d-04ea-43f5-b8e7-6522a0c5173c --> Glossary terms alone are too
  narrow; "every noun" is too broad. _Owner_: author. _Path_: iterate on a real set.
  _Blocks_: a mechanized redundancy check.
- **`OQ-2` — A category name for stories + journeys?** <!-- uuid: 7716c97d-9e14-4e8a-b941-8912e871a7f4 --> They are required and define the
  extent, so they may deserve a collective term. _Owner_: author. _Path_: decide while
  applying the method. _Blocks_: nothing yet.
- **`OQ-3` — The bootstrap flow.** <!-- uuid: af7f087e-0cdb-4cd4-b4ec-324c6990b6f4 --> `STORY-3`'s want (existing product → first behavior docs
  set) has no defined journey yet. _Owner_: author. _Path_: co-develop with the companion
  authoring tool. _Blocks_: the bootstrap tool.
- **`OQ-4` — The regeneration flow.** <!-- uuid: 731fe9c5-b051-4010-b601-c70b873f16b6 --> `STORY-6` / `GOAL-17`'s want is forward-looking and
  has no defined journey. _Owner_: author. _Path_: revisit once a first full set exists.
  _Blocks_: re-platform automation.
- **`OQ-5` — Behavior/decision seam.** <!-- uuid: ff2fc74c-a5ef-4460-9682-b724202b5338 --> How to classify observable-behavior-essential "how"
  (durability, backoff-growth). _Owner_: author. _Path_: refine the substitution test with
  an observability framing on real cases. _Blocks_: clean placement at the seam.
- **`OQ-6` — Enforcing docs-first, and onboarding.** <!-- uuid: bb2430eb-e523-4f70-9442-14ef7529b843 --> Companion tooling that directs
  contributors to start from the behavior docs, keeps their downstream work tied back to the
  cited IDs (`INV-3`, `INV-21`), and helps maintain the docs. The **conformance/drift pass**
  partially realizes this by checking a set against the method, but the direct-to-source
  onboarding and the tie-back enforcement are undefined. _Owner_: author. _Path_: co-develop
  with the companion tooling. _Blocks_: automated docs-first enforcement.
