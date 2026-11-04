# Investigation: what still costs the settle tick and the over-10 s requests (2026-11-04)

After the phase 0 CPU work and the read-path batching, the live daemon (about 8,000 conversations, about
60,000 files, index.db about 30 GB) still showed a settle tick of 9 to 60 s (27 ticks: 14 under 15 s, 19 under
30 s, 25 under 60 s, 27 under 120 s) and `query_duration_seconds` observations over 10 s (49 of 99). This note
names each remaining cause from the code, a 30 s CPU profile and the loopback metrics (read-only), and the fix
for each. No live data was written.

## What changed in this commit

`Settler.settledPrefilter` ran one read transaction but issued one `settled` read per conversation plus one
`generations` point read per file (about 8,000 + 60,000 statements). Inside a transaction each statement still
takes the shared-memory lock (`unixShmLock` showed 13.1 percent of the Tick samples), and the whole
transaction must finish inside the 30 s read deadline. When it does not, the prefilter returns an empty
"unchanged" set and the tick falls back to evaluating every conversation with per-statement reads outside a
transaction, which is the `evaluate` to `changedSinceSettle` path (18.5 percent of samples in the profile).
The prefilter now reads `settled` (`Reader.AllSettled`) and the copy offsets (`Reader.CopyOffsets`) with one
statement each, so its cost no longer grows with files; the answer is unchanged (the existing equivalence test
passes, a new test pins the statement count at a constant). Expected effect: the prefilter transaction drops
from seconds to well under one second and the fallback is no longer reachable by tick length.

## Remaining causes

| #   | Cause                                                                                                                                                                                                                                                                                                                                                                                     | Evidence                                                                                                                                                                                    | Proposed fix                                                                                                                                                                                                                                                                                                                                              | Size |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---- |
| 1   | `Engine.transcriptFacts` (under `conversation.list`, `conversation.show`) runs `MAX(time_ns)`, a newest-cwd lookup and a `DISTINCT file_id` over `records WHERE session=? AND source='claude-transcript'`. The only index that helps is `records_session(session)`, so each call walks every record of the session and sorts; List does this for every conversation (13 million records). | 13.59 s of 24.60 s of samples under `Engine.List` in the read-path profile; `query_timeouts_total` grows on `conversation.show` (14) and `op="query"` (21) and never on a settle operation. | Add a covering index `records(session, source, time_ns)` (built by the derived-index list, off the request path, in one pass during a maintenance window because it writes on a 30 GB file) and make List read the per-session facts in one grouped statement instead of three statements per conversation. Needs an operator ruling on the build window. | M    |
| 2   | `Settler.refresh` and `Parser.UpdateGauges` each read all files, all generations, and (refresh) all ranges every tick or gauge refresh. `UpdateGauges` also reads copy commits per lagging generation and finds each file by binary search.                                                                                                                                               | `AllGenerations` is 87.5 percent (UpdateGauges) and 11.7 percent (refresh) of the 9.3 percent of allocation it still costs; 4.8 percent of CPU samples.                                     | Replace both with the bulk `CopyOffsets` read plus one aggregate statement for parse lag, and drop `AllFiles` from `UpdateGauges` by joining files in SQL.                                                                                                                                                                                                | S    |
| 3   | `refreshRemovalGauges` walks the whole `projects/` tree (`filepath.WalkDir` in `quarantineDirs`) on every tick.                                                                                                                                                                                                                                                                           | 1.44 s of 4.97 s of Tick samples (11.3 percent), all in `os.ReadDir`.                                                                                                                       | Refresh the quarantine gauges every N ticks, or from the removal pass itself, which already knows what it moved.                                                                                                                                                                                                                                          | S    |
| 4   | `Settler.groups` rebuilds the file-to-conversation map from `AllFiles` and `AllPaths` each tick (and again after the removal pass when removal is enabled).                                                                                                                                                                                                                               | 0.55 s of 4.97 s (11.1 percent).                                                                                                                                                            | Keep the map and update it from the files and paths that changed since the last tick (a generation counter on the store).                                                                                                                                                                                                                                 | M    |
| 5   | Quiet, held and changed conversations are evaluated one by one: `evaluate` reads the stage, then `newest` calls `lastSeen`, which runs three statements per file (copy commits, snapshots, tombstones).                                                                                                                                                                                   | Not in the 30 s profile (3 quiet, 0 held at the time); it matters after a burst of activity.                                                                                                | Batch the three reads into one read transaction per tick, as the prefilter does.                                                                                                                                                                                                                                                                          | S    |

## How to re-measure

After the next apply, take `scripts/profile-cpu.sh --seconds 30`, the tick histogram and
`query_duration_seconds` from the loopback metrics, once in a quiet window (no `conversation.list` client
running), and compare `Settler.Tick`'s share against the 39 percent recorded here. Cause 1 will dominate the
over-10 s requests until the index exists, so judge the tick and the requests separately.

## Cause 1 fix: index and grouped read (deploy note)

`conversation.list` and `conversation.show` now read a session's newest write and its files in one grouped
statement (`SELECT file_id, MAX(time_ns) ... GROUP BY file_id`) instead of a `MAX` and a `DISTINCT` statement,
and the newest-cwd lookup runs on the same new index. The index is `records_session_source_time` on
`records(session, source, time_ns)`.

- Migration: no `SchemaVersion` bump and no migration step. The index is one more entry in `derivedIndexes`,
  and a normal open runs `CREATE INDEX IF NOT EXISTS` for every entry, so the first start of the new binary
  builds it. A bulk rebuild builds it with the others after the load. An older binary ignores the extra index,
  so a rollback is safe. If the schema v3 rebuild lands, carry this index into v3 (or replace it with the
  equivalent on the interned session reference).
- Operator step at deploy: the first start on the live file (about 30 GB) builds this index in one write
  transaction before the daemon serves requests. The file and the WAL grow by the size of the index (not yet
  measured on the live file) until the next checkpoint. Do it in a maintenance window: check free space first
  (`pg-claude-code-recorder status` shows it), stop the old daemon, start the new one, wait for it to report
  `caught_up`, then confirm with
  `sqlite3 index.db "SELECT name FROM sqlite_master WHERE name='records_session_source_time'"`.
- Verify afterwards: `query_duration_seconds` observations over 10 s for `conversation.list` and
  `conversation.show` should stop, and `query_timeouts_total` should stop growing on them.

## A slow `remove` no longer starves the settle tick

Observed live: after a 9.4 s tick no tick finished for about 512 s, and a 30 s CPU profile put 72 percent of
the samples in the `remove` request path (`Settler.Remove`, `removeLocked`, `blockers`, `formatHeld`, one SQLite
`QueryRow`). `Remove` held the settler's run lock (the lock `Tick` also takes) for the whole pass, and the pass ran
the format-hold scan once per Conversation (about 8,000) even though the answer depends on the source and the
last day only. The settle tick timestamp that stayed frozen for 15 minutes is explained by the same holder: the
tick was waiting for the run lock, so `settle_tick_last_timestamp_seconds` could not advance.

Changes (no schema change, no new index):

- The scan is computed once per source per pass (a verdict cache), not once per Conversation.
- `Remove` computes the per-source verdicts before it asks for the run lock, so a slow scan no longer blocks a
  tick. Safety is kept by repeating the scan, once per source, under the lock and right before the first removal
  of the pass: a rate that rose after the early scan still keeps the files. The early verdicts only decide what a
  dry run reports and which Conversations are worth considering. (The tick's own removal pass already holds the
  lock and scans once per source, as before the removal.)
- The three counts no longer scan `records`: they start from the source's files, take their commits through the
  primary key and reach `records` through `records_ident` by file, generation and an offset range. The commits of
  the source's files are still read, because `commits.seen_ns` has no index; that is the remaining cost, bounded
  by the commit count rather than the record count. An index on `commits(seen_ns)` would remove it and belongs
  with the schema v3 rebuild.
- New counters `settle_run_lock_wait_total{op}` and `settle_run_lock_wait_seconds_total{op}` count the time callers
  queue for the run lock, and a wait of 5 s or more logs `settle_run_lock_wait_slow` with the operation, so a
  starved tick shows up as a lock wait instead of only as a missing tick.
