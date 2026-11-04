# Design: recorder `index.db` schema v3 (proposal, not decided)

Status: PROPOSAL. Nothing in this document is implemented and `store.SchemaVersion` stays 2. It ends with
one decision for the operator (fold into one v3 rebuild, or defer). This repo is public: every figure below
comes from a synthetic fixture or from aggregate counts of a local database, with no transcript content,
session ids or real paths.

Related: `docs/investigations/2026-11-03-index-db-size.md` (where the bytes go; its "Live scan" section is the
baseline this design builds on).

## 1. Goals and non-goals

Goals, each with the cost it removes:

1. `records.session` (a 36-character TEXT on every record, plus its index) becomes a small integer reference.
2. The per-session constants that today are stored as one `fields` row per record (`cwd`, `git_branch`,
   `version`, `entrypoint`, `user_type`) become interned ids on the record.
3. Sources and record types can add or change fields without touching the core DDL: one-to-one extension
   tables keyed by `rec_id`, first user: the usage counters of an assistant line.
4. A written type-change policy and drift detection, so a field whose type changes upstream is counted, not
   silently lost.
5. One schema version bump, one rebuild (a rebuild takes hours; every schema change is batched into it).

Non-goals: changing either full-text index (the substring-search ruling is a separate product decision, see
the size investigation), adding retention, changing the capture side (copier, journal, `files`, `paths`,
`generations`).

## 2. Census

Read-only (`PRAGMA query_only=1`, `sqlite3 -readonly`, 2 s busy timeout, `nice -n 10`, hard `timeout`), on a
host whose load average was about 38, so only bounded queries ran: whole-table distinct counts, and
300,000-record windows for everything per record. `dbstat` was NOT re-run (it reads the whole file); byte
figures for live objects are the exact `dbstat` numbers recorded by the earlier live scan, and are labelled so.
The WAL did not grow during the census (173,381,992 bytes before and after).

### 2.1 Fresh counts

| Quantity                                  |                       Value | How read                                                   |
| ----------------------------------------- | --------------------------: | ---------------------------------------------------------- |
| `records` highest rowid                   |                  17,008,287 | `max(id)`                                                  |
| Distinct `records.session`                |                      11,597 | `count(distinct session)`, 31.6 s                          |
| Records per session                       | 17,008,287 / 11,597 = 1,467 | division                                                   |
| Records with an empty session             |                           1 | `session=''`                                               |
| Files holding more than one session       |         0 of 2,103 (window) | window 8,000,000 to 8,300,000                              |
| Sessions spanning more than one file      |         138 of 302 (window) | same window; the most files in one session was 109         |
| Distinct `(session, cwd)` pairs, window   |        752 for 302 sessions | 2.5 cwd values per session in the window                   |
| Distinct `(session, git_branch)` pairs    |        321 for 302 sessions | same window                                                |
| Distinct `(session, version)` pairs       |        307 for 302 sessions | same window                                                |
| Distinct cwd strings, window              |                         269 | `count(distinct s)`                                        |
| Distinct branch strings, window           |                          73 | same                                                       |
| Assistant lines, three windows of 300,000 |    34,094 / 76,107 / 59,142 | `message.usage.input_tokens` rows; 11 % to 25 % of records |
| `fields` rows per record, three windows   |          10.1 / 12.2 / 13.0 | rows in window / 300,000 (3,036,967; 3,651,036; 3,909,595) |

Reading the table. (a) A file never holds two sessions, so `session_files` is a pure fan-out from session to
files, never the reverse; a session fans out to many files (subagent transcripts), which is why a session
cannot be a column of `files`. (b) The earlier 129-file sample (11 sessions of 107 spanning files) understated
this: on the live index a session spans several files in 46 percent of the window's sessions (138 / 302).
(c) cwd varies inside a session far more than version or branch, so an interned id per record, not one
value per session, is required; a "first seen" cwd on `sessions` would be wrong for about half of them.

### 2.2 Live bytes and rows (from the earlier read-only scan, same day)

| Object                                                         |                                             Bytes or rows | Source                      |
| -------------------------------------------------------------- | --------------------------------------------------------: | --------------------------- |
| `index.db` total                                               |                                      30,650,707,968 bytes | dbstat                      |
| `records` table                                                |                                       1,680,785,408 bytes | dbstat                      |
| `records_session`                                              |                                         870,182,912 bytes | dbstat                      |
| `records_source_type`                                          |                                         623,489,024 bytes | dbstat                      |
| `records_type`                                                 |                                         339,263,488 bytes | dbstat                      |
| `fields` table plus `fields_name_n`                            |                                       7,668,715,520 bytes | 7,022,608,384 + 646,107,136 |
| `fields` rows                                                  |                                               196,306,009 | `count(*)`                  |
| `cwd`, `git_branch`, `version`, `entrypoint`, `user_type` rows | 9,574,399 / 9,573,572 / 9,918,292 / 9,574,399 / 9,573,572 | per-name census             |
| `message.usage.*` numeric rows, each of six                    |                                                 2,755,447 | per-name census             |

Within-session variation (earlier scan, 7,898 sessions): `cwd` changes in 1,637 sessions (most values in one
session 31), `git_branch` in 597 (9), `version` in 34 (2), `entrypoint` in 12 (2), `user_type` in 0.

## 3. Savings estimate

### 3.1 Method

`scripts/sizing-schema-v3.sql` builds, from the same synthetic rows in one SQLite file, the current layout and
each proposed layout, then reads `dbstat` per object. It uses 300,000 records at 1,460 per session (206
sessions); 56 percent of records carry the five constants (live: 9,574,399 / 17,008,287 = 56.3 percent);
20 percent are assistant lines with nine usage rows (live 16.2 percent: 2,755,447 / 17,008,287, so per-line
savings are scaled by the live count, not the fixture's). Strings are synthetic but length-matched (cwd 44
bytes, branch 7). It models only the rows that change; uuid, parent uuid and the rest of the line fields are
identical in both layouts and omitted. Run: `sqlite3 "$TMPDIR/sizing-v3.db" < scripts/sizing-schema-v3.sql`
(the script is deterministic; the bytes below are its output).

The fixture's accuracy was checked against live where both exist: `records_session` costs 13,639,680 / 300,000 =
45.5 B per record in the fixture and 870,182,912 / 17,008,287 = 51.2 B live (live 12 percent heavier, so the
estimates below are slightly low for that item).

### 3.2 Results (fixture bytes, then scaled to live)

| Proposal                                       |                                            Current bytes (fixture) |                                                         Proposed bytes (fixture) | Saved per unit                                     | Live scale                   |                                                         Saved (live) |
| ---------------------------------------------- | -----------------------------------------------------------------: | -------------------------------------------------------------------------------: | -------------------------------------------------- | ---------------------------- | -------------------------------------------------------------------: |
| P1 `session_ref` replaces `session` TEXT       |                               29,368,320 + 13,639,680 = 43,008,000 |                                              18,644,992 + 3,108,864 = 21,753,856 | 21,254,144 / 300,000 = 70.8 B per record           | x 17,008,287 records         |                                                             1.205 GB |
| P2 interned constants as columns on `records`  |                            `fields` rows 25,939,968 (840,000 rows) | +2,535,424 columns, +3,670,016 two partial indexes, +188,416 lookups = 6,393,856 | 19,546,112 / 168,000 = 116.3 B per carrying record | x 9,574,399 carrying records |                 1.114 GB (1.323 GB without the two optional indexes) |
| P3 `ext_assistant_usage` replaces 9 usage rows |           10,661,888 + 4,546,560 index = 15,208,448 (540,000 rows) |                                                          1,806,336 (60,000 rows) | 13,402,112 / 60,000 = 223.4 B per assistant line   | x 2,755,447 assistant lines  |                                                             0.616 GB |
| Total P1 to P3                                 |                                                                    |                                                                                  |                                                    |                              | 1.205 + 1.114 + 0.616 = 2.935 GB, 9.6 % of 30.65 GB (2.935 / 30.651) |
| P4 (optional) `source` and `type` as small ids | 29,368,320 table less session, plus 10,788,864 + 5,345,280 indexes |                                               10,977,280 + 3,207,168 + 2,904,064 | 17,690,624 / 300,000 = 59.0 B per record           | x 17,008,287 records         |                                                             1.003 GB |

P1 table delta: 29,368,320 - 18,644,992 = 10,723,328; index delta: 13,639,680 - 3,108,864 = 10,530,816.
P4 is measured against the table with `session_ref` already applied (18,644,992), so it adds to P1.
P3 drops the partial numeric index entries of the six counters; if a counter must stay filterable fast, an
index on that ext column gives the 4,546,560-byte share back (4,546,560 / 60,000 = 75.8 B per line, so the
saving falls from 223.4 to 147.6 B per line, 0.41 GB).

These savings are against the layout AFTER the redundant rows are gone. The five facts a row already carries
elsewhere (`type`, `session_id`, `timestamp`, `file_session`, `project`: 57,838,016 live rows) are no longer
written by the current parser (`derivable` in `internal/parser/parser.go`), so any rebuild drops them whether
or not v3 happens; the earlier investigation priced that at 57,838,016 x 35.78 B = 2.07 GB at least. The
fixture models none of those rows, so nothing is counted twice.

### 3.3 Statement savings

Each field is one `INSERT INTO fields` statement today (plus one for the record). In v3 the five constants
ride in the `records` insert and the nine usage values are one `ext_assistant_usage` insert:

| Item                            | Calculation              | Statements |
| ------------------------------- | ------------------------ | ---------: |
| Constants no longer separate    | 5 x 9,574,399            | 47,871,995 |
| Usage rows folded into one      | (9 - 1) x 2,755,447      | 22,043,576 |
| Total avoided over a full parse |                          | 69,915,571 |
| Share of live `fields` inserts  | 69,915,571 / 196,306,009 |     35.6 % |

Per record after a rebuild without v3: (196,306,009 - 57,838,016) / 17,008,287 = 8.14 field statements plus 1
record statement = 9.14. With v3: 9.14 - 69,915,571 / 17,008,287 = 9.14 - 4.11 = 5.03, 45 percent fewer. Write
time was not measured, so no time saving is claimed.

### 3.4 What the end state looks like

Starting from the live 30,650,707,968 bytes (30.65 GB): minus the redundant rows 2.07 GB (lower bound),
minus P1 to P3 2.94 GB = 25.64 GB; a dense rebuild also reclaims some of the 1.32 GB of in-page slack
(upper bound 24.3 GB). With P4 about 23.3 GB. The full-text indexes (18.26 GB) are untouched here; the size
investigation shows the larger lever (13.4 GB) is the substring-search ruling. v3 on its own is a 9.6
percent change, so it is worth a rebuild only if a rebuild is happening anyway (section 8).

## 4. Naming convention

| Prefix or form  | Meaning                                            | Examples                                 |
| --------------- | -------------------------------------------------- | ---------------------------------------- |
| plain noun      | core row table, one row per real thing             | `records`, `sessions`, `files`, `fields` |
| `ext_`          | one-to-one extension of a core row table, same key | `ext_assistant_usage`                    |
| `lk_`           | lookup, interned strings, `(id, v)`                | `lk_cwd`, `lk_branch`, `lk_version`      |
| `fts_`          | full-text index                                    | `fts_word`, `fts_tri`                    |
| `<table>_stats` | mutable sibling of an otherwise append-only table  | `session_stats`                          |
| join tables     | `<left>_<right>s`, composite primary key           | `session_files`                          |

New tables are `STRICT`. `ext_` tables are keyed by `rec_id INTEGER PRIMARY KEY` (the same integer as
`records.id`), which makes the join one b-tree seek and costs no second index (Class Table Inheritance with
the subtype keyed by the supertype's key). Existing tables are not renamed.

## 5. DDL

Unchanged: every capture-side table in `internal/store/schema.go` (`meta`, `files`, `paths`, `generations`,
`commits`, `snapshots`, `tombstones`, `procs`, `resets`, `conv_stage`, `settled`, `ranges`, `torn_tails`), and
`field_names`, `fields`, `texts`, `fts_word`, `parse_state`, `parse_resets`, `feed_marks`,
`bad_lines`, `unknown_keys`, `unknown_keys_total`. `fts_tri` is not in the new file: opaque fields (tool bodies)
are indexed in `fts_word` (the store builds that layout with `Options.NoTrigramIndex`; the running v2 file keeps
`fts_tri`). `conv_stage`, `settled` and `ranges` keep the natural
session id as TEXT: they are small (one row per session), belong to the lifecycle side, and keeping the text
there means the capture side carries no link to the derived `session_ref`.

Added (derived side):

```sql
CREATE TABLE sessions(
  session_ref      INTEGER PRIMARY KEY,
  session_id       TEXT NOT NULL UNIQUE,
  project          TEXT,
  first_time_ns    INTEGER NOT NULL,
  first_version_id INTEGER REFERENCES lk_version(id),
  entrypoint_id    INTEGER REFERENCES lk_entrypoint(id),
  user_type_id     INTEGER REFERENCES lk_user_type(id)
) STRICT;

-- Mutable sibling: written once per parse batch (an upsert), never per record.
CREATE TABLE session_stats(
  session_ref  INTEGER PRIMARY KEY REFERENCES sessions(session_ref),
  last_time_ns INTEGER NOT NULL,
  record_count INTEGER NOT NULL
) STRICT;

-- A session fans out to many files (subagent transcripts); a file holds one session.
CREATE TABLE session_files(
  session_ref INTEGER NOT NULL REFERENCES sessions(session_ref),
  file_id     INTEGER NOT NULL,
  PRIMARY KEY(session_ref, file_id)
) WITHOUT ROWID;
CREATE INDEX session_files_file ON session_files(file_id);

CREATE TABLE lk_cwd(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE lk_branch(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE lk_version(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE lk_entrypoint(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE lk_user_type(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
```

`records` (the new columns are NULL when the line does not carry the value; `session_ref` 0 never occurs: the
empty session gets a sentinel `sessions` row with `session_id = ''`):

```sql
CREATE TABLE records(
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  file_id        INTEGER NOT NULL,
  gen            INTEGER NOT NULL,
  off            INTEGER NOT NULL,
  item           INTEGER NOT NULL,
  len            INTEGER NOT NULL,
  source         TEXT NOT NULL,          -- P4 turns source and type into ids (optional)
  type           TEXT NOT NULL,
  parser_version INTEGER NOT NULL,
  session_ref    INTEGER NOT NULL REFERENCES sessions(session_ref),
  time_ns        INTEGER NOT NULL DEFAULT 0,
  cwd_id         INTEGER REFERENCES lk_cwd(id),
  branch_id      INTEGER REFERENCES lk_branch(id),
  version_id     INTEGER REFERENCES lk_version(id),
  entrypoint_id  INTEGER REFERENCES lk_entrypoint(id),
  user_type_id   INTEGER REFERENCES lk_user_type(id)
) STRICT;
CREATE INDEX records_session ON records(session_ref);
CREATE INDEX records_cwd     ON records(cwd_id)    WHERE cwd_id IS NOT NULL;     -- optional, 1.8 MB per 300,000
CREATE INDEX records_branch  ON records(branch_id) WHERE branch_id IS NOT NULL;  -- optional
-- records_ident, records_time, records_type, records_source_type unchanged.
```

The fixture measured `records` without the `REFERENCES` clauses; foreign keys are declared for documentation
and are NOT enforced (`PRAGMA foreign_keys` stays off, as it is today), so there is no per-insert check cost.

First extension table:

```sql
CREATE TABLE ext_assistant_usage(
  rec_id                       INTEGER PRIMARY KEY,   -- = records.id
  input_tokens                 INTEGER,
  output_tokens                INTEGER,
  cache_creation_input_tokens  INTEGER,
  cache_read_input_tokens      INTEGER,
  cache_1h_input_tokens        INTEGER,
  cache_5m_input_tokens        INTEGER,
  service_tier                 TEXT,
  inference_geo                TEXT,
  speed                        TEXT
) STRICT;
```

The fixture interned the three small strings as ids to keep the row narrow; the DDL keeps them TEXT because
they have a handful of values and a TEXT of 8 to 13 bytes costs about 0.3 GB at live scale; the slice that
builds it SHOULD re-measure and MAY intern them. Every column is nullable, because a source can omit any
counter and `NULL` must mean "absent", never zero.

## 6. Type-change policy and drift detection

The catalogue (`Catalog()` in `internal/source/source.go`, today a name to `Kind` map from the `ingest` struct
tag) gains a storage declaration per field: `fields` (default, the generic key-value table), `record:<column>`,
or `ext:<table>.<column>`. A field's declared kind decides the column type.

When a value does not fit its declared type the parser MUST follow this policy:

1. Convert only if lossless: a JSON number that is integral and within int64 into an `INTEGER` column; a decimal
   string that parses and re-formats to the identical string; a float into a `REAL` column. Never truncate,
   round or clamp.
2. Otherwise store the raw value as text in the generic `fields` row for that name (`s` set, `n` and `f`
   NULL), leave the typed column NULL, and count `field_type_mismatch_total{source,type,field}`.
3. A reader of a typed field MUST treat "typed column NULL and a `fields` row present" as a present, raw
   value, so a mismatch is queryable and never looks like a missing value.

Drift detection builds on the existing `unknown_keys` (per file) and `unknown_keys_total` (aggregate): a key
the catalogue does not declare is counted there already. The new metric covers the other half (declared key,
unexpected type). Neither blocks ingest. A dashboard panel and an alert on a non-zero mismatch rate are the
operator-facing surface (Grafana files live in `grafana/`).

## 7. Migration path

There is no in-place migration. `store.Open` already refuses an older database with `SchemaOlderError`; the
derived part is rebuilt from the journal and the copies (`serve --rebuild` or the rebuild command), which sets
the old file aside as `index.db.pre-rebuild` and deletes nothing. v3 uses exactly that path:

1. All slices land with `SchemaVersion` still 2 in their DDL behind the new code paths, except the last slice,
   which flips the constant to 3. Until then the daemon runs unchanged.
2. The version-bump slice adds a free-space preflight: the new file plus its WAL must fit beside the old file
   (the v3 file is estimated at 23 to 26 GB, section 3.4, so the operator needs at least that much free; 31 GB
   was free at the last scan).
3. The rebuild writes `records` with `session_ref` assigned in first-seen order, fills `sessions`,
   `session_files` and `session_stats` as it goes, and builds the secondary indexes after the load (the bulk
   path already does that, `buildIndexesLocked` in `internal/store/bulk.go`).
4. Parity gate (a unit test on a fixture, not a live run): rebuild the same fixture corpus into v2 and v3 and
   assert that every request in the query test corpus returns the same rows; the existing path-fact request
   list in `internal/query/pathfacts_test.go` is the model.
5. Rollback is the retained `index.db.pre-rebuild`; a v2 binary cannot open a v3 file (`SchemaNewerError`), so
   a downgrade is a restore plus the old binary.

One full rebuild takes hours (the earlier fixture measured 2 to 3 MB/s parse; the live copy size was not
read, so no total is stated). That is the reason all of section 5 is one version bump.

### 7.1 Two-file split (capture side versus derived side)

Evaluated, NOT recommended for v3. A split would let a rebuild write a new derived file and swap it, leaving
the capture tables alone. Costs: `records.file_id` and the `paths` joins used by path facts become
cross-file (`ATTACH`), with no atomic commit across the two files (the copier's commits and the parser's
`parse_state` would no longer share a transaction, and the crash-recovery invariants rely on that), two WALs
to bound, and the lifecycle engine's queries join capture and derived tables. The benefit (a rebuild does not
touch capture state) is already delivered by "rebuild into a new file, then swap", which exists. Revisit only
if a rebuild ever has to preserve capture rows the journal cannot regenerate.

## 8. Query-layer changes

| Where                                                                                                                              | Change                                                                                                                                                                                                                                                                                 |
| ---------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/query/filter.go` `expr` and `resolve` (`session` to `r.session`)                                                         | A session filter resolves the natural id to `session_ref` (`r.session_ref = (SELECT session_ref FROM sessions WHERE session_id=?)`); a select, group or order by session joins `sessions`. `contains` and `in` resolve through the join.                                               |
| `internal/query/filter.go` for fields with `record:` or `ext:` storage                                                             | `eq` on a string resolves the id first (`r.cwd_id = (SELECT id FROM lk_cwd WHERE v=?)`); `contains`, group and select join the `lk_` table; order by joins and sorts by `v`, never by id. An `ext:` field joins `ext_*` once per alias on `rec_id`.                                    |
| `internal/query/search.go` (hit rows select `r.session`)                                                                           | Join `sessions` to return `session_id` on a hit.                                                                                                                                                                                                                                       |
| `internal/query/query.go` target lookup (`WHERE r.session=?`)                                                                      | Same resolution through `sessions`.                                                                                                                                                                                                                                                    |
| `internal/lifecycle/engine.go` `cwdQuery`, `registryQuery`, `endReasonQuery`, `slashCommandQuery`, last-write and file-set queries | `cwdQuery` becomes `records r JOIN lk_cwd` (the plan guard `TestCwdQueryPlanStartsFromTheSessionIndex` MUST be kept and updated); the file set comes from `session_files` instead of `SELECT DISTINCT file_id FROM records WHERE session=?`; last write comes from `session_stats`.    |
| `internal/parser/parser.go` `insert`                                                                                               | Resolve `session_ref` and the five lookup ids through caches keyed to `Store.RollbackEpoch` (the same validity rule as the field-name cache); route `record:` and `ext:` fields into the records insert and one ext insert; upsert `session_stats` and `session_files` once per batch. |
| Reparse and delete paths (`Reparse`, store delete by file and generation)                                                          | Delete `ext_*` rows by `rec_id` together with `fields` and `texts`; `sessions`, `session_files` and `lk_` rows are NOT deleted (they are shared), `session_stats` is recomputed.                                                                                                       |
| Catalogue and `ingest` tags (`internal/source`)                                                                                    | Add the storage declaration (section 6) and a test that every catalogued field has exactly one storage.                                                                                                                                                                                |
| `unknown_keys` consumers (`internal/settle/remove.go`, `internal/obs`)                                                             | Unchanged; add the mismatch metric next to the existing unknown-key metric.                                                                                                                                                                                                            |

The wire protocol (`internal/wire`) does not change: answers still say `session_id`, and the field names
clients filter on are the same. The planner has no `sqlite_stat1` in this store (documented at `cwdQuery`),
so every new join MUST be written so the intended outer table is forced (as `CROSS JOIN` does there) and
covered by a query-plan test.

## 9. Alternatives considered

| Alternative                                                | Why not (or not now)                                                                                                                                            |
| ---------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| First-seen `cwd` and `branch` on `sessions` only           | Wrong for about half the sessions in the window (2.5 cwd values per session); the copy would be the only truth.                                                 |
| History table of cwd changes                               | A per-record id is 2 bytes and needs no change-detection logic in the parser; no history table needed.                                                          |
| Keep the constants in `fields`, add only `session_ref`     | Gives P1 only (1.2 GB) and keeps 47.9 million statements.                                                                                                       |
| Widen `fields` with typed columns instead of `ext_` tables | `fields` is sparse key-value; adding columns taxes every row with NULLs and changes the core DDL per source.                                                    |
| Intern `source` and `type` (P4)                            | Measured 1.0 GB, but every `r.type` and `r.source` in the query layer and the lifecycle engine becomes a lookup; optional, last, only if the operator wants it. |
| Two SQLite files                                           | Section 7.1.                                                                                                                                                    |
| Per-record `lk_` ids for `entrypoint` and `user_type`      | Included anyway: 17,008,287 x about 1 byte is small, and 12 sessions change entrypoint, so dropping the per-record value would lose data.                       |

## 10. Risks

- Planner regressions on the new joins (no statistics): mitigated by forced join order and plan tests.
- `session_stats` and `session_files` are derived from `records` and MUST be rebuildable from it; a test
  asserts that recomputing them from `records` equals what the parser wrote.
- The ext table and the generic `fields` row for a mismatched value can disagree; the reader rule in section 6
  point 3 is the guard.
- The estimate is from a fixture with a 12 percent lighter session index than live (section 3.1); the live
  saving is probably slightly larger, not smaller.
- A rebuild needs free space of about the new file size, section 7 step 2.

## 11. Proposed implementation slices

Sizes are t-shirt sizes. "Blocked by" names other slices of this list.

| #   | Title                                                                                                            | Size | Blocked by               |
| --- | ---------------------------------------------------------------------------------------------------------------- | ---- | ------------------------ |
| S1  | Catalogue storage declaration, lossless-conversion rule and `field_type_mismatch_total`                          | M    | none                     |
| S2  | `sessions`, `session_stats`, `session_files`; `records.session_ref`; parser caches and per-batch upserts         | L    | none                     |
| S3  | Query layer and lifecycle engine read `session_ref` (filter, search, query targets, `cwdQuery` plan guard)       | M    | S2                       |
| S4  | `lk_` tables and per-record `cwd_id`, `branch_id`, `version_id`, `entrypoint_id`, `user_type_id`; parser routing | M    | S1, S2                   |
| S5  | Extension-table convention and `ext_assistant_usage`; reparse delete path                                        | M    | S1, S2                   |
| S6  | Query layer for `record:` and `ext:` storage (filter, group, order, select, plan tests)                          | L    | S4, S5                   |
| S7  | (Optional) intern `source` and `type`                                                                            | M    | S2, S3                   |
| S8  | Version bump to 3, free-space preflight, v2 versus v3 parity test, README and ADR update                         | L    | S3, S6 (and S7 if taken) |
| S9  | Grafana panel and alert for the mismatch metric and unknown-key growth                                           | S    | S1                       |

Slice 2 as built: a fresh or existing store gets `sessions`, `session_stats`, `session_files` and a
`records.session_ref` column (added with `ALTER TABLE` at open when missing, a constant default so no row is
rewritten) while `SchemaVersion` stays 2 and `records.session` stays the column every query reads. The parser
writes both. A store created before the slice keeps `session_ref` 0 on its older rows and has no session rows
for them until it is rebuilt. Left to later slices: the `records_session` index on `session_ref` (an index
build over the live table is the version-bump slice's job), the swap of `session` for `session_ref`, and the
lookup ids on `sessions`, which stay NULL until the lookup tables exist.

## 12. Decision for the operator

Fold S1 to S6, S8 and S9 (and S7 if wanted) into one schema v3 rebuild, or defer v3?

Facts for the ruling: v3 saves 2.94 GB of 30.65 GB (9.6 percent) and 35.6 percent of the field inserts; it
costs six implementation slices plus the bump, and one hours-long rebuild. The bigger levers (the
substring-search ruling worth about 13.4 GB, and the 2.07 GB of redundant rows that any rebuild drops) need a
rebuild too, so the cheapest plan is one rebuild that carries all of them. Recommendation: if the
substring-search ruling leads to a rebuild, fold v3 into it; if not, defer v3 and take S1 (a policy slice
with no rebuild) and S9 alone.
