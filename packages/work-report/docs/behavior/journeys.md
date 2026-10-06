# Journeys — work-report

Stories, use cases, and journeys, typed and leveled per the behavior-docs method's vocabulary
rules: **user-goal** and **subfunction** level elements are `USECASE-`; **summary**-level
multi-actor arcs stay `JOURNEY-`. Each element carries, on its own definition, what it requires and
what it includes (`INV-22`). Elements are grouped by work-report's two conceptual halves.

## Ingestion

### Stories

- **`STORY-ING-PULL`** <!-- uuid: 078cc60b-da7e-4a18-8bbb-92bc4377a66d --> — As the operator, I
  want to pull my activity from configured sources for a range, so it's captured in one place.
  _(→ `USECASE-ING-PULL`; `INV-RANGE-1`, `INV-ORDER-1`, `INV-ID-1`.)_
- **`STORY-ING-DEGRADE`** <!-- uuid: 9f4020bb-1af0-47cb-a7c8-4f8811e6cc95 --> — As the operator, I
  want a pull to keep going even when one source is temporarily unreachable, so a flaky source
  doesn't erase the rest. _(→ `USECASE-ING-PULL`; `INV-DEGRADE-1`.)_
- **`STORY-ING-EXTEND`** <!-- uuid: d7971125-8495-46c9-be99-d93a5c34b29a --> — As the operator, I
  want to add a new source without existing sources or readers changing, so the store grows with
  what I use. _(→ `USECASE-ING-ADD-SOURCE`; `INV-PLUGIN-1`.)_
- **`STORY-ING-LOOKUP`** <!-- uuid: 179e409b-3bb3-4682-8f9a-8f480bacf5a1 --> — As a reader, I want
  to query entries by range, identifier, type, or label and get exactly one authoritative entry
  per identifier, so superseding or duplicate data never shows up twice. _(→ `USECASE-ING-QUERY`;
  `INV-LATEST-1`, `INV-INDEX-1`.)_
- **`STORY-ING-VALIDATE`** <!-- uuid: 86379ee7-0bf0-41d1-b203-42c2b052a01c --> — As the operator, I
  want an entry that doesn't match its declared type's schema to be rejected and reported rather
  than silently stored, so a misbehaving source can't corrupt the store. _(→ `USECASE-ING-PULL`;
  `INV-TYPE-1`.)_

### Use cases

#### `USECASE-ING-PULL` — pull entries for a range <!-- uuid: f6b674c6-f6ee-47a5-a15e-27c4bb16917c -->

**Primary actor:** `ACTOR-OP`.
**Level:** user-goal.
**Preconditions:** at least one source is configured and enabled.
_Requires:_ `INV-RANGE-1`, `INV-DEGRADE-1`, `INV-TYPE-1`, `INV-ORDER-1`, `INV-ID-1`,
`INV-SOURCE-1`, `INV-APPEND-1`.

1. The operator triggers a pull, naming a range and, optionally, a subset of sources (default:
   all enabled).
2. work-report asks each targeted source (`INTF-INGEST`) for entries in that range.
3. Each returned entry is validated against its declared type's schema and, if valid, appended
   (`INV-APPEND-1`); work-report reports a per-source outcome.

Extensions:

- 2a. A source is unavailable or errors: reported degraded, with a reason; the other sources'
  pulls continue (`INV-DEGRADE-1`).
- 2b. A source is disabled: reported disabled; never attempted.
- 3a. An entry fails its type's schema: rejected and reported, never stored (`INV-TYPE-1`); the
  source's other entries are still stored and its outcome is succeeded, with the rejections
  counted (`INV-DEGRADE-1`).

#### `USECASE-ING-QUERY` — query entries <!-- uuid: 19a12951-53b9-4a23-83cd-e801728b2cf4 -->

**Primary actor:** `ACTOR-READER`.
**Level:** user-goal.
**Preconditions:** none — an empty store answers with no entries.
_Requires:_ `INV-LATEST-1`, `INV-INDEX-1`, `INV-LABEL-1`.

1. The reader queries a range and, optionally, any combination of unique identifier, type, label,
   or source.
2. work-report returns matching entries, each resolved to the one latest entry for its identifier
   (`INV-LATEST-1`).

Extensions:

- 2a. No entries match: an empty result, not an error.

#### `USECASE-ING-ADD-SOURCE` — add a new source <!-- uuid: 93c5e329-ed1d-48e5-8501-bcd44cb1dcca -->

**Primary actor:** `ACTOR-OP` (as integrator).
**Level:** user-goal.
**Preconditions:** the new source implements `INTF-INGEST`.
_Requires:_ `INV-PLUGIN-1`.

1. The operator configures a new source instance implementing `INTF-INGEST`.
2. work-report pulls and reports from it exactly like any existing source
   (`USECASE-ING-PULL`), with no change to any existing source's or reader's behavior.

## Reporting

### Stories

- **`STORY-REP-CHRONO`** <!-- uuid: da18a5d8-5abd-42ac-99dd-b9db06337e75 --> — As the operator, I
  want a plain chronological listing of my activity for a range, so I can scan what happened
  without prose. _(→ `USECASE-REP-REPORT`; `INV-BASELINE-1`, `INV-REPORT-RANGE-1`.)_
- **`STORY-REP-DEBUG`** <!-- uuid: 68de528e-e831-4f2a-9901-c7021929edd9 --> — As the operator, I
  want a report kind that never depends on any optional generator, so I can always sanity-check
  what was ingested even when a fancier kind is failing or misconfigured. _(→
  `USECASE-REP-REPORT`; `INV-BASELINE-1`.)_
- **`STORY-REP-RANGE`** <!-- uuid: 1b3fa4ac-3a93-4a97-a062-363cbc5715f4 --> — As the operator, I
  want a report for any range I choose — today, a week, a custom span — not only "today", so a
  retro or catch-up isn't a different capability. _(→ `USECASE-REP-REPORT`; `INV-REPORT-RANGE-1`,
  `GOAL-2`.)_
- **`STORY-REP-CUSTOMIZE`** <!-- uuid: a355e1f2-26b2-4936-b1ef-8bf6c7e27860 --> — As the operator,
  I want to narrow a report by label, source, or type, so it reflects what's actually useful to
  me rather than a fixed one-size view. _(→ `USECASE-REP-REPORT`; `INV-CUSTOM-1`.)_
- **`STORY-REP-NARRATIVE`** <!-- uuid: 9c66f09f-544c-43bf-b7fc-e368865982b1 --> — As the operator,
  I want a narrative-style report that foregrounds what mattered, so I get something more useful
  than a raw listing when I want that. _(→ `USECASE-REP-REPORT`; `INV-MULTI-1`, `GOAL-1`.)_

### Use cases

#### `USECASE-REP-REPORT` — request a report <!-- uuid: 7e769745-8545-4824-b156-7d19c116df80 -->

**Primary actor:** `ACTOR-OP`.
**Level:** user-goal.
**Preconditions:** none — work-report may hold nothing for the requested range.
_Requires:_ `INV-REPORT-RANGE-1`, `INV-MULTI-1`, `INV-BASELINE-1`, `INV-CUSTOM-1`.
_Includes:_ `USECASE-ING-QUERY`.

1. The operator requests a report: a range, an optional kind (the baseline kind when
   omitted, `INV-BASELINE-1`), and optional narrowing.
2. work-report queries the store (`USECASE-ING-QUERY`, via `INTF-READ`) for entries matching the
   range and narrowing.
3. work-report renders the report: the baseline kind directly from the queried entries, or a
   further kind by dispatching to its generator (`INTF-GENERATE`).
4. work-report returns the report, stating the range, kind, and narrowing actually applied.

Extensions:

- 2a. No entries match: the report says so plainly, rather than fabricating content
  (`INV-REPORT-RANGE-1`).
- 3a. An unrecognized kind is named: reported as an outcome, not a report.
- 3b. The generator cannot honor a given narrowing: reported distinctly (`INV-CUSTOM-1`).

#### `USECASE-REP-ADD-GENERATOR` — add a new report kind <!-- uuid: 067b3c2f-11d7-45c4-87e9-ff6aa77bb982 -->

**Primary actor:** `ACTOR-OP` (as integrator).
**Level:** user-goal.
**Preconditions:** the new generator implements `INTF-GENERATE`.
_Requires:_ `INV-GEN-PLUGIN-1`.

1. The operator configures a new generator, implementing `INTF-GENERATE`, for a new kind.
2. work-report dispatches a request naming that kind to it (`USECASE-REP-REPORT`), with no change
   to any existing kind's request or response shape.

## Journey

### `JOURNEY-DAILY-CYCLE` — the accrue, resolve, and report arc <!-- uuid: 954f631d-d849-44c3-8a86-9c820adc5fac -->

Summary level, spanning `ACTOR-OP`, `ACTOR-SOURCE`, `ACTOR-READER`, and `ACTOR-GENERATOR`.
_Includes:_ `USECASE-ING-PULL`, `USECASE-ING-QUERY`, `USECASE-REP-REPORT`.
_Requires:_ `INV-DEGRADE-1`, `INV-LATEST-1`, `INV-ENTITY-1`, `INV-REPORT-RANGE-1`,
`INV-BASELINE-1`.

On whatever cadence suits them, the operator pulls fresh activity; entries accrue, append-only.
One pull's source is temporarily degraded, and the pull still lands whatever the other sources
returned. The same real-world thing is independently observed more than once — an entity's entry
ingested from one source, then a newer entity entry for the same unique identifier ingested
later, perhaps from a different source, on the same day — and a later query resolves to exactly
one entry for that identifier, per latest-wins resolution.

Independently of any pull — sometimes right after one, sometimes days later — the operator
requests reports: most days a narrative for today, some days the baseline chronological listing
to sanity-check what is actually there, and at the end of a week a report across the whole range
narrowed to one label, for a retro. The two halves never share a request: a pull never happens
because a report was asked for, and a report never triggers a pull — it only ever reads, through
`INTF-READ`, whatever ingestion has already resolved.
