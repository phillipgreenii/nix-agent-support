# Behavior docs — the method

This is the behavior docs set **for the behavior-docs method itself** — self-describing, in
its own vocabulary. New here? Read this on-ramp, then the [glossary](glossary.md); the rules
are in [invariants](invariants.md).

A **behavior doc** describes how a system _should_ behave, from the user's perspective: user
stories, use cases, journeys, actors, interfaces, goals, and invariants. A **behavior docs set**
is all of that for one **scope**. The behavior docs set is the **living source of truth** for the
system's intended behavior, and it outlives any one implementation.

## Two inputs, one product — the two-input model

A product comes from **two** durable, human-owned inputs — the **two-input model**:

- **behavior docs** — the _what_: intended behavior; this method's scope (conventionally
  under `docs/behavior`).
- **decision docs** — the _how_: rationale-backed realization decisions (architecture,
  language, framework, tooling, testing, tuning, and **governance authority** — who or what may
  approve or change the docs), conventionally under `docs/decisions`. An **ADR** is one
  established form of decision doc; which form a project uses is its own realization decision.
  (A system's own **behavioral** governance — an identity, permission posture, or
  non-defeatable guardrail it treats as intended behavior — stays in its behavior docs; see
  [actors](actors.md).) Named here; the **hand-off** to them is in this method's extent
  (`USECASE-5`), their internal form is out of this method's scope.

`product = f(behavior docs, decision docs)`. When a product is wrong, the split says which
input to fix: wrong _behavior_ → the behavior docs; behavior right but _realization_ wrong →
the decision docs.

## The north-star

A behavior docs set exists so downstream work — increasingly agent-driven — runs more
autonomously: a source of truth to resolve uncertainty against, and from which (with the
decision docs) a _behavior-conformant_ implementation can be regenerated.

## Scope of this behavior docs set (extent + floor)

- **Scope** is established by the [user stories, use cases and journeys](journeys.md): the
  **extent** is exactly what they require; the **floor** is how deep the docs go before deferring
  to downstream.
- **Extent (in)** — what a behavior docs set is and contains; the rules it follows; how it
  establishes scope; how it defines its interfaces to other products; the lifecycle use cases
  and journeys.
- **Extent (out)** — the companion tooling (authoring, bootstrap) is a downstream product;
  decision docs (tech, tuning, governance authority, human/agent roles) are a sibling input —
  the **hand-off** across that seam is in extent (`USECASE-5`), their internal form, layout and
  local ID vocabulary are not; downstream spec/design/plan and any concrete file or output layout
  are downstream.
- **Floor** — this behavior docs set speaks at "how the behavior-docs method should behave,"
  never how to build the tooling, nor a fixed file layout (**layout is illustrative** — the
  invariants govern content, not files).

## IDs

Each citable element carries a **typed name** (`INV-3`): invariants `INV-`, goals `GOAL-`, user
stories `STORY-`, use cases `USECASE-`, journeys `JOURNEY-`, interfaces `INTF-`, actors `ACTOR-`,
open questions `OQ-`; concepts are cited by name. **Identity is a stable UUID** minted at the element's definition and
never changed, carried in an HTML comment on the definition line; the **name is a mutable,
human-readable label**, consistent only within its own set. Namespacing (`INV-DISP-1`,
`INTF-SOURCE`) is for readability, not identity — matching is by UUID — so a name MAY be renamed
without breaking identity, and gaps and out-of-sequence numbers are legal. Across behavior docs
sets an element is cited by name as `<repo-name> · <set-path> · <name>`, and a set declares the
external elements it references — each with **what it is** and the **owner's UUID linked to the
owner's remote-served definition** — in [External references](#external-references) below. A
decision-doc entry is cited the same way, by typed name resolving to the entry's UUID (`GOAL-5`).
A set that cites another set SHOULD namespace
its own names by topic so a cited name never collides with one of its own; this **root** method,
citing no other set, keeps bare numeric names.

## External references

A set declares every external element it cites here, so a cross-set reference resolves by the
owner's UUID (`INV-3`) — the same imports mechanism used for external contracts. Removed elements
leave no tombstone; their history lives in git (`INV-4`).

Each row is `` `| <name> | <what it is> | <repo> · <set-path> | [<uuid>](<remote-url>) |` ``. The
**what it is** column is one line, so a reader learns why the row is there without following the
reference. The owner UUID is rendered as a link to the owner's **remote-served** definition — the
published URL, never a local path — because the local path is meaningless to anyone reading from
another repository.

**The UUID is the authority; the URL may rot.** Matching is by UUID (`INV-3`), so a dead or missing
link is an inconvenience, never a broken identity, and MUST NOT be reported as divergence. Because
the link is only useful if the reader can follow it, it MUST point from a **less** publicly reachable
scope toward a **more** publicly reachable one and MUST NOT point the other way: a private set MAY
link into a public one, while a public set citing a private one declares the bare UUID, since a link
its readers cannot open is worse than none.

| Name | What it is | Owner set-path | Owner UUID |
| ---- | ---------- | -------------- | ---------- |

_This **root** method references no other behavior docs set, so its imports table is empty; the
header above is the normative shape every set's table takes. The decision-doc entries this set's
rules cite live in this same product's own decision area (`behavior-docs/docs/decisions`) — the
sibling input of the two-input model, not an external set — so they are cited by typed name and need
no row here (`GOAL-5`)._

## What belongs — with examples

Say what a user should be able to do and what must always hold; leave _how_ downstream.
Illustrative (`GOAL-7`):

> - **Story** (`STORY-1`) — "As an author, I want one place that answers 'how should this
>   behave?'"
> - **Use case** (`USECASE-2`) — user-goal level, primary actor the Author: "To change
>   behavior: edit the docs first; downstream is re-derived, then discarded."
> - **Journey** (`JOURNEY-4`) — summary level, spanning the owning and the consuming Authors:
>   "an owner edits its contract and each consumer re-converges by pull."
> - **Invariant** (`INV-15`) — the behavior docs set is the source of truth; change flows
>   docs-down.
> - **Interface** (`INTF-1`) — "the docs provide intended behavior at the floor; downstream
>   must cite the ID it implements."
> - **Open question** (`OQ-1`) — "What counts as a named concept for `INV-14`? Undecided."

Those are **illustrative examples**, not **golden examples** — a golden example is one
asserted by a real test. What does _not_ belong, and where it goes instead:

| ❌ In a behavior doc                           | ✅ Where it belongs                   |
| ---------------------------------------------- | ------------------------------------- |
| "`parseConfig()` validates the schema"         | downstream design/code                |
| "retry 3× with a 30s backoff"                  | decision docs (the two-input model)   |
| "who or what may approve a change" (authority) | decision docs                         |
| a tool named below the scope's floor           | generalize it (the substitution test) |
| "we used to shell out to X; next quarter Y"    | nowhere — intended behavior only      |

## Documents

- **[README](README.md)** · **[glossary](glossary.md)** · **[actors](actors.md)** ·
  **[interfaces](interfaces.md)** · **[invariants](invariants.md)** ·
  **[use cases, journeys & open questions](journeys.md)**
