# Decision docs — ccpool

The **decision docs** for pr-pool: the durable _how_ that combines with its
behavior docs (`../behavior`, authored separately) to produce the product
(`product = f(behavior docs, decision docs)` — the two-input model). These carry the realization
decisions the behavior docs deliberately exclude: architecture, tooling, testing, tuning, and
governance **authority**.

This area is **informal**. There is no ADR template and no required section list. An entry states
what was decided (or what is captured but not yet decided), and why, in as much prose as the
decision needs.

## Relocating content into this area

Content arrives here from the behavior docs by the method's relocation procedure,
[`JOURNEY-5`](../../../../behavior-docs/docs/behavior/journeys.md). Follow it there; it is **not**
restated here.

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

## Layout

Entries are grouped into **topic files** (`<topic>.md`), several entries per file. This README is
the index — it lists every entry by id with its one-line summary, and nothing else is authoritative
about what exists here. Numbered `NNNN-*.md` files are **not** used.

## Not in this area

The repository's own `docs/adr/` records remain where they are and are **not** migrated here. This
area holds (a) content relocated out of ccpool's behavior docs and (b) entries ccpool's behavior
docs cite. An entry that overlaps an existing repository ADR MUST **cite** it, never copy it.

## Entries

_None yet._
