# Glossary — work-report

Vocabulary for work-report, grouped by its two conceptual halves; each term here is used in at
least two other places in this set (`INV-14`). See [actors](actors.md) for who uses these terms and
[interfaces](interfaces.md) for where they cross a boundary.

## Ingestion

- **Entry** — one record in the store: something the operator did (an **activity**), or a
  standing fact about something external (an **entity**), depending on its **type**. Every entry
  carries a **unique identifier**, a **source**, a **type**, an **occurred-at**, and optionally
  **labels**, a **summary**, and a **URL** (`INV-ID-1`, `INV-SOURCE-1`, `INV-TYPE-1`,
  `INV-ORDER-1`, `INV-LABEL-1`).
- **Summary** — an entry's optional one human-readable line naming it, independent of its type, so
  a report can render an entry of a type it has never seen (`INV-TYPE-1`, `INV-BASELINE-1`).
- **URL** — an entry's optional link to the thing it describes.
- **Unique identifier** — the identity the store keys an entry on. Distinct in concept from an
  **external identifier** (an identifier some external system already uses for the same
  real-world thing, e.g. a ticket key); an external identifier MAY be used as, or as part of, the
  unique identifier, but the store's own identity and any external system's identity are never
  conflated (`INV-ID-1`).
- **Type** — an entry's classification, which determines the schema (expected fields) it must
  satisfy. The type catalog is open: a **source** MAY introduce new types (`INV-TYPE-1`).
  **Activity** types describe a one-off happening at a point in time; **entity** types describe a
  standing fact about something external, superseded by a newer entry for the same unique
  identifier exactly as any other entry is (`INV-ENTITY-1`).
- **Occurred-at** — the date or date-time an entry's own content places it at. A bare date with no
  time-of-day is acceptable; it is still enough to place the entry in chronological order
  (`INV-ORDER-1`).
- **Ingestion time** — the instant the store itself accepted an entry, distinct from
  **occurred-at**. Used only to break a tie when two entries for the same unique identifier share
  the same occurred-at (`INV-LATEST-1`).
- **Source** — the system an entry came from, and the counterparty on `INTF-INGEST`. Every entry
  names its source (`source_id`, `INV-SOURCE-1`). git, GitHub, Claude Code sessions, beads, and
  Jira are today's named sources (`interfaces.md`); the set is open to more (`INV-PLUGIN-1`).
- **Label** — an optional tag on an entry, opaque to the store beyond being available for lookup
  (`INV-LABEL-1`, `INV-INDEX-1`).
- **Range** — the date span a pull or a query covers: a single date, a bounded span, or
  open-ended. Every `INTF-INGEST` and `INTF-READ` request names one (`INV-RANGE-1`).
- **Pull outcome** — what a single source's pull attempt reports: **succeeded** (with a count),
  **degraded** (unavailable or errored, with a reason, and a count when entries were stored
  despite the degradation), or **disabled** (not configured to run). It also reports how many
  entries were **rejected** for schema reasons, how many were **unchanged** (re-observed
  identically and not appended), and whether the pull was **truncated** (the source could not
  cover the whole range). A source that stored entries and rejected others is **succeeded**, not
  degraded. Never merged into one pass/fail signal for the whole pull request (`INV-DEGRADE-1`). A source that ran out of time is retried once before it is reported **degraded**.
- **Latest-wins resolution** — the rule a read applies when more than one entry shares a unique
  identifier: the entry with the latest occurred-at wins; a tie is broken by the latest ingestion
  time (`INV-LATEST-1`).
- **Append-only** — the store never mutates or deletes a written entry; latest-wins resolution
  happens only at read time, over an ever-growing set of writes (`INV-APPEND-1`).

## Reporting

- **Report** — the artifact work-report produces for a range: a set of files written by one
  **generator** into one **output directory**, together with a **run record**. Requesting one
  generator or preset never requires requesting another (`INV-MULTI-1`), and every report states
  the range it covers (`INV-REPORT-RANGE-1`).
- **Generator** — a named producer of a report's files: given the entries a request resolved to
  and the range, it yields files and a description of them, and it is the implementer of
  `INTF-GENERATE`. The **baseline generator** is built in: a plain chronological listing that
  always exists and depends on no externally registered generator (`INV-BASELINE-1`). Any further
  generator (for example a narrative-style rendering) is added by the operator
  (`INV-GEN-PLUGIN-1`). A generator's own mechanism (a template, generated prose, anything else)
  is unspecified; work-report states only the contract it must honor.
- **Preset** — a named, repeatable report request: it binds a generator, that generator's
  settings, a range, and a narrowing, so a repeatable report is one word. A request naming no
  preset gets the default preset, which is the baseline generator (`INV-BASELINE-1`). A preset replaces
  the retired "report kind", which named only the rendering and bound nothing else.
- **Run record** — work-report's own description of one report run: the range covered, the preset
  or generator used, the narrowing applied, how many entries the generator was given, and the
  files the run produced (`INV-REPORT-RANGE-1`, `INV-GEN-CONTAIN-1`). It is the durable record of
  a report; the store holds no copy of it.
- **Output directory** — the one directory a single report run writes into. Every file a report
  reports lies inside it (`INV-GEN-CONTAIN-1`), and the run record is kept with those files.
- **Narrowing** — restricting a report request to entries matching a label, a source, and/or a
  type — the same dimensions the store indexes (`INV-INDEX-1`). work-report applies it before
  generation, so a generator only ever receives matching entries. A request MAY combine
  narrowings and give several values within one dimension; narrowings compose as an intersection
  across dimensions and a union within a dimension (`INV-CUSTOM-1`).
