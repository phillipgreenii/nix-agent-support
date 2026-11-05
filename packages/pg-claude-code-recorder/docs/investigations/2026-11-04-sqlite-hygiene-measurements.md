# Measurements: SQLite hygiene and a rolling doctor (2026-11-04)

Read-only measurements that the phase 6 tasks (ANALYZE, the session index, the commits pruning rule, the
checkpoint policy, the doctor slicing) cite. Every figure below sits next to the command that produced it;
nothing is estimated. Where a number is derived from other numbers, the arithmetic is shown.

## Method and safety

- Host: macOS, local disk; `sqlite3` CLI 3.51.0 (`sqlite3 --version`), and the daemon's own driver
  `modernc.org/sqlite` v1.61.0 (`rg -n modernc go.mod`), which reports `sqlite_version()` = 3.54.1 in the
  optimize run below.
- The live `index.db` was never opened for writing and never opened by a measurement query. A copy was made
  with a read-only source handle:

  ```text
  sqlite3 -readonly $STATE/index.db ".backup $COPY/index.db"     # real 50m36.317s, exit 0
  cp -f $STATE/observations.journal $COPY/observations.journal   # taken AFTER the backup, so the journal is
                                                                 # ahead of the copy, never behind it
  ```

  `$STATE` is `$HOME/.local/share/pg-claude-code-recorder` (the configured `stateDir`); `$COPY` is a scratch
  directory under `$HOME/.cache`, on the volume that had room. The copy was deleted when the measurements
  ended (see the last section).

- Free disk checked first with `df -k /Volumes/gitrepos $HOME`: `/Volumes/gitrepos` 39,827,704 KiB free
  (about 38 GiB, under the 40 GB floor, so not used); the volume holding `$HOME` 57,050,484 KiB free (about
  54 GiB), which is above the 40 GB floor and was used. After the copy that volume showed 41,075,556 KiB
  free (`df -k $COPY`), so the copy plus its scratch use stayed within the margin.
- The copy is the live file at the moment of the backup: `ls -l` of the live state directory just before
  showed `index.db` 13,394,198,528 bytes, `index.db-wal` 29,622,832 bytes, `observations.journal`
  129,672,306 bytes. The copy's `index.db` is 13,394,198,528 bytes (`ls -l $COPY`), with an empty WAL. The
  earlier investigation note in this directory quotes "index.db about 30 GB"; the index has since been
  rebuilt (its `meta.last_rebuild_profile` says `records` 17,028,419), so this document supersedes that
  size.
- Statements on the copy that were not plain reads (the freeze harness opens the copy as a store, and the
  `PRAGMA optimize` run writes `sqlite_stat1` into it) ran last, after all plans and counts below were
  recorded; they changed only the copy.

Database shape (`sqlite3 $COPY/index.db "pragma page_size; pragma page_count; pragma freelist_count; pragma journal_mode;"`):

| Figure         | Value                                                              |
| -------------- | ------------------------------------------------------------------ |
| page_size      | 4096                                                               |
| page_count     | 3,270,068 (x 4096 = 13,394,198,528 bytes, equals `ls -l`)          |
| freelist_count | 31,460 (x 4096 = 128,860,160 bytes, about 1.0 percent)             |
| journal_mode   | wal                                                                |
| schema_version | 3 (`select * from meta`; also `epoch` 3, `session_ref_complete` 1) |

## sqlite_stat1

Before any measurement writes, `sqlite3 $COPY/index.db "select name,type from sqlite_master where name like 'sqlite_stat%'"`
printed no row: **`sqlite_stat1` does not exist** in the database, so the planner has no statistics today.
(The `PRAGMA optimize` run below created it in the copy only.)

## Per-table bytes (dbstat) and row counts (COUNT(\*))

Bytes: `sqlite3 -header -column $COPY/index.db "SELECT name, SUM(pgsize) AS bytes, COUNT(*) AS pages FROM dbstat GROUP BY name ORDER BY bytes DESC;"`
(real 4m17.063s; the full output is 77 rows, the 30 largest objects are listed; every object not listed is
under 1.3 MB).

| Object                                         |         Bytes |     Pages |
| ---------------------------------------------- | ------------: | --------: |
| fields (table)                                 | 4,345,643,008 | 1,060,948 |
| fts_word_data                                  | 3,791,081,472 |   925,557 |
| records (table)                                | 1,235,607,552 |   301,662 |
| records_session_ref_source_time (index)        |   750,747,648 |   183,288 |
| records_source_type (index)                    |   703,643,648 |   171,788 |
| fields_name_n (index)                          |   409,518,080 |    99,980 |
| records_ident (unique index)                   |   372,977,664 |    91,059 |
| records_type (index)                           |   355,655,680 |    86,830 |
| records_time (index)                           |   345,411,584 |    84,329 |
| texts                                          |   174,067,712 |    42,497 |
| ext_assistant_usage                            |   133,582,848 |    32,613 |
| records_cwd (index)                            |   118,198,272 |    28,857 |
| records_branch (index)                         |   108,376,064 |    26,459 |
| sqlite_autoindex_unknown_keys_1                |    80,351,232 |    19,617 |
| unknown_keys                                   |    75,739,136 |    18,491 |
| fts_word_docsize                               |    75,522,048 |    18,438 |
| texts_rec (index)                              |    73,842,688 |    18,028 |
| settled                                        |    34,832,384 |     8,504 |
| paths                                          |    16,125,952 |     3,937 |
| sqlite_autoindex_paths_1                       |    15,990,784 |     3,904 |
| paths_path (index)                             |    15,781,888 |     3,853 |
| files                                          |     7,008,256 |     1,711 |
| generations                                    |     3,149,824 |       769 |
| files_ident (index)                            |     3,043,328 |       743 |
| **commits (table)**                            | **2,805,760** |   **685** |
| sqlite_autoindex_commits_1 (the commits index) |     1,740,800 |       425 |
| feed_marks                                     |     1,728,512 |       422 |
| fts_word_idx                                   |     1,490,944 |       364 |
| session_files                                  |     1,286,144 |       314 |
| snapshots                                      |     1,228,800 |       300 |

Row counts: for each table, `sqlite3 $COPY/index.db 'SELECT COUNT(*) FROM "<table>"'` (script output, seconds
per count in the last column; tables named `fts_word_*` other than `fts_word` are the FTS shadow tables and
were not counted).

| Table                         |         Rows | Count time |
| ----------------------------- | -----------: | ---------: |
| fields                        |  126,137,192 |       57 s |
| records                       |   17,229,841 |       11 s |
| texts                         |    5,161,085 |        2 s |
| fts_word                      |    5,161,085 |        3 s |
| ext_assistant_usage           |    2,779,987 |        7 s |
| unknown_keys                  |    1,252,381 |        3 s |
| session_files                 |       98,149 |        0 s |
| feed_marks                    |       95,727 |        0 s |
| **commits**                   |   **90,343** |        0 s |
| paths                         |       88,476 |        1 s |
| files                         |       88,391 |        0 s |
| parse_state                   |       86,365 |        0 s |
| generations                   |       54,334 |        0 s |
| snapshots                     |       37,584 |        0 s |
| sessions                      |       11,841 |        0 s |
| session_stats                 |       11,841 |        0 s |
| conv_stage                    |        8,174 |        0 s |
| settled                       |        8,172 |        0 s |
| tombstones                    |          794 |        0 s |
| procs                         |          400 |        0 s |
| parse_resets, resets          |     378 each |        0 s |
| lk_cwd, lk_branch             | 3,926; 1,425 |        0 s |
| ranges, torn_tails, bad_lines |       0 each |        0 s |

Findings the later tasks use:

- `commits` is 90,343 rows and 2,805,760 + 1,740,800 = 4,546,560 bytes (4.3 MiB, 0.034 percent of the
  13,394,198,528-byte file: 4,546,560 / 13,394,198,528). Pruning it cannot reclaim a meaningful share of the
  database or change a statement's plan; the commits pruning decision should weigh that.
- The three largest objects, `fields`, `fts_word_data` and `records` with its indexes, hold the file:
  `fields` 4,345,643,008 + `fts_word_data` 3,791,081,472 = 8,136,724,480 bytes, 60.7 percent
  (8,136,724,480 / 13,394,198,528).
- `records` carries seven indexes (from `sqlite_master`): `records_ident`, `records_time`, `records_type`,
  `records_source_type`, `records_cwd`, `records_branch`, `records_session_ref_source_time`. The current
  session index is `records_session_ref_source_time(session_ref, source, time_ns)`, 750,747,648 bytes.
  There is no `records_session` index in a schema_version 3 database (`sqlite3 $COPY/index.db "select name, sql from sqlite_master where type='index' and tbl_name in ('records','fields','commits') and sql is not null"`);
  `records` has no `session` text column (`pragma table_info(records)`), it has `session_ref`.

## The ten most frequent read statements

The list comes from the per-operation histogram that the phase 1 work added. Source:
`curl -s --max-time 5 127.0.0.1:49320/metrics` (the daemon's loopback metrics address from its config), the
series `pg_claude_code_recorder_query_duration_seconds_count{op=...}` and `requests_total`, read at the end of
the session in which the measurements were taken:

| op                | `query_duration_seconds_count` | sum (s) |
| ----------------- | -----------------------------: | ------: |
| conversation.show |                            658 |  343.11 |
| query             |                            327 | 2469.58 |
| process.list      |                            194 |  136.59 |
| snapshot          |                              6 |  179.65 |
| conversation.list |                              2 |    0.96 |
| status            |                              2 |    0.00 |

(`requests_total` also shows `hello` 1,264 and `feed` 32, neither of which is a SQL read worth timing.)

`conversation.show`, `conversation.list` and `process.list` all run the lifecycle load (`loadFrom` in
`internal/lifecycle/engine.go`: the first eight statements below, one read transaction), and
`conversation.show` adds the two per-session statements (S5, S6). `query`, `snapshot` and `feed` share the
head and the generated record select (S9, S10, S11). S1 to S9 and S11 are the statement text of the code
verbatim (with the `session_ref` variant that the store selects when `session_ref_complete` is 1, which it is
here); **S10 is hand-built from `base()` in `internal/query/query.go`** for a one-session filter, because the
`query` op's SQL is generated per request: it is representative, not a captured statement. S5, S6 and S10
use the session with the most records
(`SELECT s.session_id FROM sessions s JOIN session_stats t USING(session_ref) ORDER BY t.record_count DESC LIMIT 1`
gave `f94c53d4-fedc-472e-8baf-2feca7f508b5`, 147,068 records; S5 returned 1 row, S6 664 rows, S10 101 rows).

Plan command: `sqlite3 $COPY/index.db` with `EXPLAIN QUERY PLAN <statement>;` (`scripts/explain.sql` kept
outside the repository). Wall time command: `time sqlite3 $COPY/index.db < Sxx.sql > /dev/null`, run three
times per statement in one script (bash `time`, TIMEFORMAT `%R`, so each figure includes about 5 ms of
process start). The first run is the coldest (the page cache already held pages from the dbstat scans, so
"cold" here is only relative); runs 2 and 3 are warm. The statement files and the full logs are in the
scratch directory, not in the repository.

| #   | Statement (code name)                                                                   | Plan (EXPLAIN QUERY PLAN)                                                                                                                                                                                   | Wall s: run 1 / 2 / 3 |
| --- | --------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------- |
| S1  | `registryQuery` (lifecycle load)                                                        | SEARCH r USING INDEX records_source_type (source=? AND type=?); SEARCH f USING PRIMARY KEY (rec_id=?); SEARCH fn USING INTEGER PRIMARY KEY; CORRELATED SCALAR SUBQUERY 1 SEARCH s USING INTEGER PRIMARY KEY | 0.603 / 0.050 / 0.043 |
| S2  | `registryDeletedQuery`                                                                  | SEARCH r USING INDEX records_source_type (source=? AND type=?); SEARCH f USING PRIMARY KEY (rec_id=?); SEARCH fn USING INTEGER PRIMARY KEY                                                                  | 0.020 / 0.011 / 0.011 |
| S3  | `slashCommandQuery`                                                                     | SEARCH r USING INDEX records_source_type (source=?); SEARCH f USING PRIMARY KEY (rec_id=? AND name_id=?); SCALAR SUBQUERY 2 SEARCH field_names USING COVERING INDEX; CORRELATED SCALAR SUBQUERY 1 SEARCH s  | 0.129 / 0.047 / 0.053 |
| S4  | `endReasonQuery`                                                                        | SEARCH r USING INDEX records_source_type (source=?); SEARCH f USING PRIMARY KEY (rec_id=? AND name_id=?); two scalar subqueries; USE TEMP B-TREE FOR ORDER BY                                               | 0.046 / 0.013 / 0.013 |
| S5  | `cwdQueryRef` (biggest session)                                                         | SEARCH r USING COVERING INDEX records_session_ref_source_time (session_ref=? AND source=?); SCALAR SUBQUERY 2 SEARCH sessions USING COVERING INDEX; SEARCH f USING PRIMARY KEY (rec_id=? AND name_id=?)     | 0.272 / 0.045 / 0.042 |
| S6  | `fileFactsQueryRef` (biggest session)                                                   | SEARCH records USING INDEX records_session_ref_source_time (session_ref=? AND source=?); SCALAR SUBQUERY 1 SEARCH sessions USING COVERING INDEX; USE TEMP B-TREE FOR GROUP BY                               | 0.210 / 0.083 / 0.080 |
| S7  | `Procs` (`FROM procs ORDER BY first_seen_ns, pid`)                                      | SCAN procs; USE TEMP B-TREE FOR ORDER BY (400 rows)                                                                                                                                                         | 0.015 / 0.009 / 0.010 |
| S8  | `Stages` (`FROM conv_stage ORDER BY session`)                                           | SCAN conv_stage USING INDEX sqlite_autoindex_conv_stage_1                                                                                                                                                   | 0.049 / 0.015 / 0.012 |
| S9  | feed head (`sqlite_sequence` for `records`)                                             | SCAN CONSTANT ROW; SCALAR SUBQUERY 1 SCAN sqlite_sequence                                                                                                                                                   | 0.009 / 0.008 / 0.011 |
| S10 | query-op shape (one session, `type='assistant'`, `ORDER BY r.id LIMIT 101`; hand-built) | SEARCH r USING INDEX records_type (type=?); SCALAR SUBQUERY 1 SEARCH sessions USING COVERING INDEX; SEARCH fl USING INTEGER PRIMARY KEY (rowid=?)                                                           | 2.354 / 0.117 / 0.101 |
| S11 | `feed_marks` lookup (`MAX(rec_id) ... WHERE at_ns < ?`)                                 | SEARCH feed_marks                                                                                                                                                                                           | 0.053 / 0.009 / 0.009 |

Reading the table:

- Eight of the eleven statements finish in under 0.1 s warm. The lifecycle load statements S1 to S4 and S7, S8
  total (warm, run 3) 0.043 + 0.011 + 0.053 + 0.013 + 0.010 + 0.012 = 0.142 s of SQL for one load.
- S10 is the outlier: its first run took 2.354 s because the planner chose `records_type (type=?)` (a scan
  of every `assistant` record, ordered by `r.id`) instead of `records_session_ref_source_time`, since the
  session is only known through a scalar subquery. The plan has no statistics to correct it; it was
  **unchanged after `sqlite_stat1` existed** (plans and timings re-run on the analysed copy:
  `S10 0.089 s` warm, plan still `records_type`), so ANALYZE alone does not change this statement's plan.
  A caveat: S10 is hand-built, so the real generated `query` statement may differ.
- With `sqlite_stat1` present in the copy (after the optimize run below), the plans of S1 to S11 were
  identical to the table above (`EXPLAIN QUERY PLAN` re-run, `explain-after-optimize.log`), and warm times
  were within noise or a little lower (S1 0.033, S3 0.036, S4 0.008, S5 0.027, S6 0.073, S10 0.089 s).
  The ANALYZE decision therefore rests on cost (below), not on a plan change in these statements.

## The size of a doctor pass's file set

A pass is `Settler.Doctor` in `internal/settle/doctor.go`: for every append file with `cur_gen > 0` it hashes
the copy's committed bytes and the same-length prefix of the original; for every snapshot file it hashes the
original; then `doctorArchives` hashes every manifest file that has a hash through the archive.
Command: `sqlite3 -header -column $COPY/index.db < fileset.sql` (the SQL is quoted in each row).

| Part                             |                    Files |          Bytes | Query                                                                                                                                                                 |
| -------------------------------- | -----------------------: | -------------: | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Append files (copy, current gen) |                   53,956 | 44,180,594,578 | `SELECT COUNT(*), SUM(g.copy_offset) FROM files f JOIN generations g ON g.file_id=f.id AND g.gen=f.cur_gen WHERE f.mode='append' AND f.cur_gen>0 AND g.copy_offset>0` |
| Snapshot files (latest version)  |                   34,435 |  3,199,530,371 | `SELECT COUNT(*), SUM(s.size) FROM snapshots s WHERE s.version=(SELECT MAX(version) FROM snapshots s2 WHERE s2.file_id=s.file_id)`                                    |
| Settled archives: manifest files | 82,662 in 8,172 archives | 46,993,867,477 | `SELECT COUNT(*), SUM(json_extract(j.value,'$.size')) FROM settled st, json_each(st.manifest,'$.files') j WHERE COALESCE(json_extract(j.value,'$.hash'),'')<>''`      |

- `files` by mode: `select mode,count(*),sum(cur_gen>0) from files group by mode` gave append 53,956 (all with
  `cur_gen>0`) and snapshot 34,435; 53,956 + 34,435 = 88,391, equal to `COUNT(*) FROM files`.
- Total bytes of copies hashed by the live-file pass: the append copies, 44,180,594,578 bytes (41.1 GiB).
  The archive stage hashes 46,993,867,477 bytes more through the archive pointers (the archives share the
  copies' bytes, so the two stages overlap on disk but each hashes its bytes).
- Physical size of the data directories (read-only, `du -sk copies archives` in `$STATE`): `copies`
  46,772,652 KiB (x 1024 = 47,895,195,648 bytes) in 145,944 entries (`ls copies | wc -l`); `archives`
  50,180 KiB in 8,172 directories (`ls archives | wc -l`), i.e. manifests only.
- Whole state directory: `du -sk .` gave 60,029,988 KiB (about 57 GiB).

For the T7 sizing rule: the byte budget counts bytes of ONE side (the copy). One pass therefore hashes
44,180,594,578 (append copies) + 46,993,867,477 (archives) = 91,174,462,055 bytes of copy-side data, or 85 GiB
(91,174,462,055 / 1,073,741,824 = 84.9), in 53,956 + 82,662 = 136,618 hash operations on the copy side, plus
the originals' prefixes (another 44,180,594,578 bytes) and the 34,435 snapshot originals (3,199,530,371
bytes).

## Journal freeze duration (T7 stop condition: 5 s)

`doctorJournal` freezes under the store mutex by `Store.FreezeJournal` (reads the journal file into a private
copy, then `s.Dump()` of the index, both while holding `s.mu`), and the replay then runs outside the lock.
Measured with a throwaway Go test kept outside the repository's committed files (a `_test.go` file in
`internal/store` that opened the copy as a store with `Options{ReplayOnly: true}` and called the same
functions, then was deleted before the commit):

```text
nice -n 10 go test -p 4 -count=1 -timeout 3000s -run TestZZMeasureFreeze -v ./internal/store/    # with CCREC_MEAS_DIR=$COPY
MEAS open_seconds=1.482
MEAS dump_alone_seconds=0.911 bytes=35850636 err=<nil>
MEAS freeze_seconds=0.609 (lock held for this whole call)
MEAS journal_delta_seconds=1.207 lastGood=449930 lastSeq=449930 err=<nil>
MEAS replay_dump_seconds=9.384 equal=true err=<nil>
MEAS total_doctorJournal_equivalent_seconds=11.203
```

- **Freeze duration (the time `s.mu` is held): 0.609 s**, below the 5 s limit, so the T7 packet's stop
  condition ("the freeze exceeds 5 s on the copy") is not met. The journal copied was 129,980,035 bytes
  (`ls -l`, the copy's `observations.journal`), and the dump of the index 35,850,636 characters.
- The unlocked part of the journal check is the long one: `JournalDelta` 1.207 s plus `ReplayDump` 9.384 s;
  the whole `doctorJournal` equivalent is 11.203 s (0.609 + 1.207 + 9.384 = 11.200, plus rounding).
  The comparison came out equal (`equal=true`, `lastGood == lastSeq == 449930`).
- Caveat: the copy was taken without the daemon's writers, so the lock was uncontended; on the live daemon
  the freeze also waits for a commit in flight.

## PRAGMA optimize time (T2 skip condition: 5 s)

Measured with the daemon's own driver (`modernc.org/sqlite` v1.61.0, `sqlite_version()` 3.54.1) against the
copy, with a second throwaway test kept outside the committed files (one connection,
`SetMaxOpenConns(1)`, default `analysis_limit`):

```text
nice -n 10 go test -p 4 -count=1 -timeout 3000s -run TestZZMeasureOptimize -v ./internal/store/    # with CCREC_MEAS_DIR=$COPY
MEAS analysis_limit=0
MEAS optimize_call1_seconds=143.107 err=<nil>
MEAS optimize_call2_seconds=0.001 err=<nil>
MEAS sqlite_stat1_exists_after=1
```

- **The first `PRAGMA optimize` on a database with no statistics took 143.107 s**, far above the 5 s skip
  threshold, and `analysis_limit` is 0 (no limit) by default on this connection. The T2 packet's rule
  ("skipped when it exceeds 5 s") therefore applies to the first call as the daemon would run it today.
- `PRAGMA optimize(-1)` (a dry run listing what it would analyse) before the call listed 30 `ANALYZE`
  statements, including `"main"."records"`, `"main"."fields"`, `"main"."texts"`, `"main"."commits"` and
  the small tables.
- The second call, with statistics in place, took 0.001 s, so a recurring call is cheap once the first has
  run; the cost is the first run (or a deliberate one-off at a maintenance moment, or a lowered
  `analysis_limit`, which this document did not measure).
- After the first call `sqlite_stat1` held rows for the tables, for example
  `records|records_session_ref_source_time|17229841 2001 364 3` and
  `records|records_source_type|17229841 1001 1001`.

## What this document does not cover

- A lowered `analysis_limit` timing, the ANALYZE decision, the index decision and the commits decision are
  the later tasks'; this document only supplies their inputs.
- Query plan changes from a new index were not measured (no schema change was made, even in the copy).
- The WAL check at the live size is not measured here (the live WAL was 29,622,832 bytes at copy time,
  `ls -l`, which is why a soak-tagged test is the planned check).

## Cleanup

The copy (about 13.4 GB) and the scratch scripts and logs under `$HOME/.cache` were deleted at the end of the
run (`rm -rf` of the scratch directory). No file under `$STATE` was written; no code, config or schema in
the repository changed, and the two throwaway test files were removed before the commit.

## ANALYZE decision: no change (PRAGMA optimize not implemented)

Decision rule, fixed before any code: run `PRAGMA optimize` (default `analysis_limit`) on connection close and
once a day from the doctor loop only if, after `PRAGMA optimize` on the database copy, at least one of the
hot read statements S1 to S11 changes plan or changes measured time by more than 2x. A bare `ANALYZE` is never
run on the 13.4 GB file.

Evidence (all from the sections above, on the copy):

- Plans: with `sqlite_stat1` present after the optimize run, the `EXPLAIN QUERY PLAN` of S1 to S11 was
  identical to the plans recorded before. No plan changed, including S10, which keeps choosing `records_type`.
- Times: the largest warm change was S10 from 0.101 s to 0.089 s and S1 from 0.043 s to 0.033 s (S1 ratio
  0.043 / 0.033 = 1.3x, S10 ratio 0.101 / 0.089 = 1.1x), all under the 2x threshold and within noise.
- Cost: the first `PRAGMA optimize` took 143.107 s (limit 5 s), so even if it had helped, the 5 s skip rule
  would skip the call as the daemon would run it today.

Outcome: **no change**. No code, schema or config is changed for this decision. If a later hot statement
shows a plan that statistics would fix, revisit with a lowered `analysis_limit` (not measured here).

## Session index decision: no change (no optional index, no rebuild-indexes command)

Decision rule, fixed before any code: build a `records(session, source, time_ns)` index only if a measured
query (the query operation with a session filter ordered by time, or search) scans a session index and sorts
with a wall time above 1 s on the copy, and the index's estimated size is acceptable.

Which layout the database uses: the copy is schema_version 3. In that layout the legacy `records.session`
column and its two indexes (`records_session`, `records_session_source_time`, listed in `legacySessionIndexes`
in `internal/store/derived.go`) do not exist; the equivalent composite already exists as the required index
`records_session_ref_source_time(session_ref, source, time_ns)` (750,747,648 bytes, `dbstat` table above),
created on every open through `layoutIndexes` / `sessionRefIndexes`. In the legacy layout the same composite
is `records_session_source_time(session, source, time_ns)`, also required. So the index under decision
already exists in both layouts; there is nothing to add, and `records_session` is not a redundant prefix in
a v3 database (it is not there).

Evidence on the copy (S5, S6, S10 above):

- The statements that filter on a session (S5 `cwdQueryRef`, S6 `fileFactsQueryRef`) use
  `records_session_ref_source_time` and run in 0.042 s and 0.080 s warm (0.272 s and 0.210 s cold), no sort on
  a session scan.
- The only statement over 1 s is S10 (2.354 s cold, 0.101 s warm). It does not scan a session index and sort:
  the planner chose `records_type (type=?)` ordered by `r.id` because the session is known only through a
  scalar subquery. A second index with the same leading columns cannot fix that choice (the existing one is
  already available and not picked), and the plan did not change after statistics existed (ANALYZE section).
  S10 is hand-built, so a real generated `query` statement may differ; that is a query-shape question, not an
  index gap.

Write amplification and size: a second copy of the same key would add about 0.75 GB (the existing index's
size, 750,747,648 bytes) and one more index update per ingested record, for no plan benefit.

Outcome: **no change**. No code, schema or config is changed; there is no optional-index split and no
`rebuild-indexes` command. Revisit only if a generated `query` statement is measured scanning a session index
and sorting above 1 s.
