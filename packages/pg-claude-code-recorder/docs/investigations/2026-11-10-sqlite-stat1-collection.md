# sqlite_stat1 collection (supersedes the 2026-11-04 "no ANALYZE" decision)

Operator ruling (Phillip, 2026-11-10): "any reason we aren't tracking statistics? I think this db is big enough to
warrant I[t]." and "2) yes, enable stats". This supersedes the decision recorded in
`2026-11-04-sqlite-hygiene-measurements.md` ("ANALYZE decision: no change (PRAGMA optimize not implemented)").

## What was measured

Copy: an APFS clone (`cp -c`) of the live `index.db` (16,306,147,328 bytes, about 29.4 million `records` rows) and its
WAL, taken while the daemon ran, so the clone is a point-in-time image of the file pair, not a quiesced copy. The live
store was never opened. The host was heavily loaded; the load averages are beside every figure. `sqlite3` CLI 3.51.0
(the daemon's driver links SQLite 3.54.1; same ANALYZE algorithm family).

| Run                                                                                               | Wall time | Load average (1, 5, 15 min) at start / end |
| ------------------------------------------------------------------------------------------------- | --------- | ------------------------------------------ |
| `PRAGMA analysis_limit=1000; PRAGMA optimize;` (whole pass, first run, one transaction per table) | 267 s     | 45.5, 49.5, 57.2 / 107.8, 72.8, 64.8       |
| `PRAGMA analysis_limit=400; PRAGMA optimize;` (same)                                              | 258 s     | 115.1, 75.5, 65.8 / 88.3, 88.5, 74.7       |
| (default limit, 2026-11-04, for comparison)                                                       | 143 s     | not recorded                               |

The limit barely changes the cost (267 s against 258 s; the earlier unlimited run was 143 s on a quieter host), so
the cost on this file is dominated by reading, not by the sampling size; treat all three as "minutes".
10000 was not run (the limit does not help, and each run costs minutes of a loaded host).

Per table at `analysis_limit=1000` (`ANALYZE main.<table>`, one transaction each): `records` 152 s, `fields` 86 s,
`texts` 9 s, `ext_assistant_usage` 6 s, `unknown_keys` 3 s, `paths` 1 s, `snapshots` 1 s, all others under 1 s.

Per index at `analysis_limit=1000` (`ANALYZE main.<index>`, one transaction each, the write-lock hold of one step;
load average 54 to 62 during the run): `records_source_type` 51 s, `records_session_ref_source_time` 43 s,
`records_ident` 31 s, `records_type` 23 s, `records_time` 18 s, `texts_rec` 16 s, `fields_name_n` 10 s,
`records_cwd` 5 s, `records_branch` 3 s, `paths_path` 1 s. **The collection pause is therefore up to about 51 s
per step on the first run**, against a writer `busy_timeout` of 5 s; the steps are separate transactions, so ingest
can proceed between them.

Content (`analysis_limit=1000`): `sqlite_stat1` has 38 rows; for `records`:

```text
records|records_session_ref_source_time|29393651 1001 182 16
records|records_branch|10129731 1001
records|records_cwd|10147643 1001
records|records_source_type|29393651 1001 1001
records|records_type|29393651 1001
records|records_time|29393651 4
records|records_ident|29393651 501 501 2 1
```

At limit 400 the figures are `29393651 401 73 39` for the session index and `401 401` for `records_source_type`: the
selectivity estimates are coarser (the second number is the average rows per key prefix, here capped by the sample).

## Not measured

- `EXPLAIN QUERY PLAN` and timings of the request catalogue on the analysed clone (step 2 of the work): not done.
  The plan tests (`internal/query`, `internal/settle`, `internal/lifecycle`) were run, and a new test runs the feed
  plan on a skewed store after `CollectStats`; the real-size comparison remains to be made.
- limit 10000; a daemon-side measurement of how long ingest waits during the first collection (it needs the
  daemon running with the change applied).

## Design

`Store.CollectStats` (internal/store/stats.go) runs the statements `PRAGMA optimize(-1)` lists, each table
expanded into one `ANALYZE "main"."<index>"` per index, on the checkpoint connection with
`analysis_limit` 1000 (restored to 0 afterwards). Each index is its own write transaction, so a writer waits for one
index, not the pass (the per-table figures above, 152 s for `records`, are what a whole-table ANALYZE would hold).
Triggers:

- a daemon loop (`stats`, internal/serve/statsloop.go): first check 5 minutes after start, then hourly, collecting
  only when the stamp in `meta` (`stats_collected_at_ns`) is older than 24 hours; and
- `Store.Close`, bounded to 2 s, when the stamp is due (a cut-short pass resumes; `PRAGMA optimize(-1)` omits
  tables that already have statistics).

The doctor loop itself is not used: its tick can wait behind a multi-minute first run, and a stalled doctor trips
its loop alerts. A dedicated loop gives the same cadence.

## Risk to check after the apply

The first collection holds the write lock for up to about 51 s per index (figures above, at a load average near 55).
Writers wait with a 5 s busy timeout. Watch the recorder's slow-request rate and write errors during the first
collection (`stats_collected` / `stats_collect_failed` log lines carry duration and statement count), and confirm
`select count(*) from sqlite_stat1` is non-zero on the live store afterwards.
