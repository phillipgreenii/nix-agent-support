# Invariants — work-report

The rules work-report's behavior follows, grouped by its two conceptual halves. See the
[glossary](glossary.md), [actors](actors.md), [interfaces](interfaces.md), and
[journeys](journeys.md). Each definition's identity is a **stable UUID** minted at its definition
(`INV-3`); the typed name is a mutable, intra-consistent label, so gaps and out-of-sequence
numbers are legal.

## Ingestion

### Entry shape

- **`INV-ORDER-1`** <!-- uuid: 683f30cd-326f-4bf6-9afb-3796e48e903d --> — Every entry MUST carry
  an `occurred_at` sufficient to place it in chronological order among all entries. A bare date
  with no time-of-day is acceptable.
- **`INV-ID-1`** <!-- uuid: 8d196e36-07d9-4597-98ab-56111c60f983 --> — Every entry MUST carry a
  unique identifier that is the store's own identity for it, conceptually distinct from any
  external identifier the same real-world thing may carry elsewhere. An external identifier MAY
  be used as, or as part of, the unique identifier, but the store's uniqueness and resolution
  (`INV-LATEST-1`) are always keyed on the unique identifier.
- **`INV-SOURCE-1`** <!-- uuid: d10d7247-fd6d-4d1a-b21f-3dd51530d717 --> — Every entry MUST carry
  the identifier of the source that produced it.
- **`INV-TYPE-1`** <!-- uuid: 28292828-8cd4-4505-ab00-de39da263b22 --> — Every entry MUST carry a
  type, and the type determines the schema its payload MUST satisfy. An entry MAY also carry a
  one-line `summary`, independent of its type, and a `url`; they are what lets the baseline
  rendering (`INV-BASELINE-1`) show an entry of a type it has never seen. An entry that does not
  satisfy its declared type's schema MUST be rejected and reported, never silently stored. The
  type catalog is open: a source MAY introduce a new type without changing any existing type's
  schema.
- **`INV-LABEL-1`** <!-- uuid: 5af9c759-24f3-4737-baaa-938e8495ab1a --> — An entry MAY carry zero
  or more labels. A label is opaque to the store beyond being available for lookup (`INV-INDEX-1`)
  — the store MUST NOT interpret a label's meaning.
- **`INV-ENTITY-1`** <!-- uuid: ceb304cc-0afd-476e-a756-093d7517cd62 --> — An entity-typed entry
  (a standing fact about something external) follows exactly the same resolution as any other
  entry (`INV-LATEST-1`), keyed on its unique identifier: a newer entity entry for the same
  identifier supersedes an older one at read time.

### Storage and resolution

- **`INV-APPEND-1`** <!-- uuid: b857c687-e865-4a5b-a204-efcb58cf6a43 --> — The store MUST NOT
  mutate or delete a written entry. Every write is an addition; resolution among entries sharing
  an identifier happens only at read time (`INV-LATEST-1`).
- **`INV-LATEST-1`** <!-- uuid: 9ad15cbb-4e00-45a6-87a5-526506df03cd --> — This is the store's
  **latest-wins resolution**: when more than one entry shares a unique identifier, a read MUST
  resolve to exactly one — the entry with the latest `occurred_at` wins; when `occurred_at` ties,
  the entry with the latest ingestion time wins. A reader MUST NOT be handed more than one entry
  for the same identifier from a single query (`INTF-READ`).
- **`INV-INDEX-1`** <!-- uuid: 1346cb1b-7d25-4509-88e0-aa376c49eea8 --> — The store MUST index
  entries for efficient cross-reference and lookup by, at minimum, unique identifier, type,
  source, and label.

### Sources

- **`INV-RANGE-1`** <!-- uuid: 87376caf-8afb-4968-8471-d0e7d6d3ecff --> — A pull request to a
  source names a range — a single date, a bounded span, or open-ended. How a source satisfies
  repeated or overlapping pulls efficiently is entirely the source implementation's own decision;
  this set imposes no watermark, cursor, or other bookkeeping mechanism.
- **`INV-DEGRADE-1`** <!-- uuid: 136b2ebc-b9da-41cf-8c0b-e7ce6a9ec845 --> — A source that is
  unavailable, errors, or is disabled MUST NOT prevent ingestion from any other configured
  source, and a pull request MUST report each source's outcome individually — succeeded (with a
  count), degraded (with a reason, and a count when entries were stored despite it), or disabled
  — never merged into one pass/fail result. A source that stored some entries and rejected others
  for schema reasons (`INV-TYPE-1`) is succeeded, with the rejections reported, not degraded.
- **`INV-PLUGIN-1`** <!-- uuid: e4b92fcc-177a-45a6-b270-5d1260d08e43 --> — `INTF-INGEST` is a
  uniform contract: adding a new source instance MUST NOT require a caller-visible change to any
  existing source's behavior. The named source catalog (`INTF-INGEST`) is illustrative of what
  exists today, not a closed set.

## Reporting

### Requests and range

- **`INV-REPORT-RANGE-1`** <!-- uuid: 98e08691-48c6-4a30-bac3-36aa9273a048 --> — Every report
  request MUST be scoped to an explicit or defaulted range, and every produced report MUST state
  the range it covers — including a range for which no entries were found, which MUST be reported
  as such rather than omitted or fabricated.
- **`INV-CUSTOM-1`** <!-- uuid: 1fd9c46b-28da-4982-a799-00c57a57923b --> — A report request MAY
  narrow scope by any combination of label, source, and type, giving one or more values within
  each dimension. Narrowings compose as an intersection (AND) across dimensions and a union (OR)
  within a dimension. A generator MUST honor a narrowing
  it is asked to apply, and MUST report distinctly when it cannot, rather than silently ignoring
  it.

### Kinds

- **`INV-MULTI-1`** <!-- uuid: c09f47e0-d60d-4a1d-879c-0245f187ab8a --> — work-report MUST offer
  more than one report kind. Requesting one kind MUST NOT require requesting, or depend on the
  availability of, another.
- **`INV-BASELINE-1`** <!-- uuid: 5326f98d-50ff-4859-a9c9-07ab5138e1aa --> — Exactly one kind is
  the **baseline**: a plain chronological rendering of the queried entries that MUST always be
  available and MUST NOT depend on any optional or pluggable generator (`INTF-GENERATE`). A
  report request naming no kind gets the baseline kind. Its
  purpose is that the operator can always get a plain view of what was ingested, even when every
  other kind's generator is unavailable or misconfigured.
- **`INV-GEN-PLUGIN-1`** <!-- uuid: 4987462b-30f4-4f7f-834a-f0e3aa45f8d1 --> — Adding a new report
  kind (a new `INTF-GENERATE` implementer) MUST NOT require a caller-visible change to any
  existing kind's request or response shape.

### Goals

- **`GOAL-1`** <!-- uuid: 544ceaa0-e7a9-4580-ab7b-cd9e78e078e9 --> — A narrative-style kind
  SHOULD foreground what happened and what stood out, so it reads as something worth having
  written, rather than enumerating every entry in order. This is a quality expectation of that
  kind, not a requirement on any particular rendering mechanism (`INV-2`).
- **`GOAL-2`** <!-- uuid: fc6d527a-3bf9-476b-8f77-03a41a2c9d76 --> — A report SHOULD be
  requestable for any already-ingested range, not only "today", so a week-in-review or retro use
  is not a different capability from a daily one.
