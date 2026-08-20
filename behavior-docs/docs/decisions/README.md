# Decision docs — the behavior-docs project

The **decision docs** for the behavior-docs project: the durable _how_ that combines with the
[behavior docs](../behavior/README.md) to produce the behavior-docs product
(`product = f(behavior docs, decision docs)` — the two-input model). These carry the realization
decisions the behavior docs deliberately exclude: architecture, tooling, testing, tuning, and
governance **authority**.

This area is **informal**. There is no ADR template and no required section list. An entry states
what was decided (or what is captured but not yet decided), and why, in as much prose as the
decision needs.

## Relocating content into this area

Content arrives here from the behavior docs by the method's relocation procedure,
[`USECASE-5`](../behavior/journeys.md). Follow it there; it is **not** restated here.

## Entry ids

Every entry carries a **typed local name** and a **stable UUID** minted on its definition line —
the same carrier form the behavior docs use, so a behavior doc MAY cite an entry and the reference
resolves by UUID, never by the mutable name.

- **`DEC-<TOPIC>-<n>`** — a **settled** decision. `<TOPIC>` is a short uppercase topic tag;
  `<n>` restarts per topic.
- **`IMPL-<n>`** — captured but **not yet decided**. `<n>` is global to this area, not per topic.
- **Promotion `IMPL-<n>` → `DEC-<TOPIC>-<n>` MUST preserve the UUID.** The name records the state;
  the UUID is the identity, and it never changes.
- A definition line looks like:
  ``### `DEC-<TOPIC>-<n>` — one-line summary <!-- uuid: <a fresh lowercase RFC-4122 v4> -->``
- There is **no per-entry status header**. `IMPL-` versus `DEC-` already says it.

This area adopts the living-document discipline `INV-4` imposes on behavior docs **by choice** —
`INV-4` binds behavior docs, not decision docs, and `self-checks.sh` never looks at
`docs/decisions`, so no mechanical check covers this area.

## Layout

Entries are grouped into **topic files** (`<topic>.md`), several entries per file. This README is
the index — it lists every entry by id with its one-line summary, and nothing else is authoritative
about what exists here. Numbered `NNNN-*.md` files are **not** used.

## Not in this area

The repository's own `docs/adr/` records remain where they are and are **not** migrated here. This
area holds (a) content relocated out of the behavior-docs method's behavior docs and (b) entries
those behavior docs cite. An entry that overlaps an existing repository ADR MUST **cite** it, never
copy it.

## Entries

- **`DEC-CONFORM-1`** — in [conformance.md](conformance.md) — conformance checking splits three
  ways: **intra** (a set against the method's rules), **inter** (a set against other systems'
  contracts), **impl** (an implementation against its own behavior docs).
- **`DEC-CONFORM-2`** — in [conformance.md](conformance.md) — the realization-gap register has one
  method-fixed shape (a `## Realization gaps` section, keyed by element id, never inline, never an
  `OQ-`); **intra** checks its form and **impl** checks its truth, with presence reported as an
  advisory until every real set carries one.
- **`DEC-VOCAB-1`** — in [vocabulary.md](vocabulary.md) — three extent-defining kinds (user story,
  use case, journey), each citing its established source, with **level** as the test between them;
  and the settled re-classification of the elements that predated `USECASE-`.
- **`DEC-TRACE-1`** — in [traceability.md](traceability.md) — traceability is a **per-element
  listing**, not a coverage section, and the listings land before any section is deleted.
- **`DEC-SEAM-1`** — in [seams.md](seams.md) — an imports row says **what it is** and links the
  owner UUID; the UUID is the authority, the link may rot, and it only ever points toward the more
  publicly reachable side.
- **`DEC-SEAM-2`** — in [seams.md](seams.md) — interfaces are classified on **two** axes
  (counterparty kind and essential-vs-optional participation), and an enumerated catalog belongs to
  the interface that carries it.
- **`DEC-SEAM-3`** — in [seams.md](seams.md) — a `GOAL-5` citation of a decision-doc entry needs an
  imports-table row only when the entry belongs to **another** scope; this product's own sibling
  decision area needs no row.
- **`IMPL-1`** — in [governance.md](governance.md) — who and what may author or change behavior
  docs, and what supervision applies.
- **`IMPL-2`** — in [authoring.md](authoring.md) — author behavior docs as a cross-linked markdown
  wiki.
