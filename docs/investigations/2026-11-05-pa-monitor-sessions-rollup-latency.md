# Investigation: why the pa-monitor `sessions` rollup takes 30 to 60 s for a 7 day window, 2026-11-05

Scope: the realistic query only. The operator ruled on 2026-11-05 that pa-monitor looks back at most 7
days and that a deeper search belongs to the recorder's own CLI, so open-ended pa-monitor queries are not
a requirement. This page explains why the 7 day `pa-monitor sessions` rollup is slow, what the recorder
already stores that could serve it, and what to build. The 30 s query deadline stays as it is.

All times are UTC. Evidence is local and read-only: the recorder CLI and its unix socket (`query`,
`search`, `status`), and `sqlite3 -readonly` on `~/.local/share/pg-claude-code-recorder/index.db`
(schema 4, 13.0 GB, 17.45 million records, 11 973 sessions rows). Nothing was written, restarted or
profiled in the daemon. This repo is public, so the page names no organization repo, PR or tracker id.

## Measurement caveats (read first)

- The recorder was in its first backfill the whole time (`status`: "first backfill is copying, 7712 of
  57690 files copied"; answers carry `degraded: true`). It was writing and copying while queried, so
  absolute times are inflated and noisy. The same query ranged 9 s to 16 s in `sqlite3` and 21 s to over
  30 s through the daemon (one timeout) within minutes of each other. A repeat of the default
  `pa-monitor sessions --json` took 60.2 s and ended `query_timeout` (an earlier run reported 34.4 s).
  Shares and plan shapes are reliable; single numbers are not.
- `sqlite3` is the system CLI (3.51.0), not the daemon's driver. The plans below are what that planner
  chooses on this database; the daemon does not expose an EXPLAIN. The daemon's own request behaviour
  agrees with the CLI timings (Q1 21 s vs 14 to 16 s), so the plan is very likely the same.
- No `sqlite_stat1` table exists: `ANALYZE` has never run, so the planner has no row counts.
- No CPU profile was taken. The daemon has a pprof socket, but a profile would perturb a live,
  backfilling daemon; EXPLAIN plus per-statement timing already locates the cost.

## Summary

| Question                                        | Answer                                                                                                                                                                                                                                                       | Status                       |
| ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------- |
| Why is the 7 day rollup slow?                   | It is not one query. pa-monitor sends 4 aggregate queries plus 2 queries per session, about 2 300 round trips for 1 149 sessions. The aggregates drive from the wrong index and read rows over all history, and the per-session loop alone costs about 27 s. | MEASURED                     |
| Which part is the cost?                         | About 25 to 60 % (varies with cache) is the first two aggregates (window scan and span); about 27 s is the 2 x N per-session round trips. User turns and tokens are about 3 s each.                                                                          | MEASURED                     |
| Can the existing session tables serve it?       | First time: nearly (83 % within 60 s). Last time: no (8 % within 60 s). Turn counts, tokens per model, cwd, branch, first prompt: no.                                                                                                                        | MEASURED                     |
| Recommendation                                  | Add a recorder-side sessions summary operation computed in one pass from the session index. Expected 5 to 10 s for 7 days on this data, with no schema change. Add parser-maintained columns later only if that is not enough.                               | ESTIMATE from measured parts |
| Is 12 s for 1 day of substring search expected? | Yes. It is throughput bound (about 5 MB/s of copy text read per value), not a planner bug.                                                                                                                                                                   | MEASURED                     |
| Does a 7 day substring window fit 30 s?         | No. It stops at the deadline (or the 256 MiB byte budget) with a partial answer. Default substring window should be 1 day.                                                                                                                                   | MEASURED                     |

## What pa-monitor sends

`Sessions` in `packages/pa-monitor/internal/recorderfacts/sessions.go` issues, for a bounded window:

| Step   | Request                                                                                                  | Rows read from `records`                                         |
| ------ | -------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| Q1     | group by session, count; filter source, type in (user, assistant), `is_sidechain=false`, `time >= since` | every record in the window, 3.5 million (157 k pass the filters) |
| Q2     | group by session, min(time), max(time) for the sessions of Q1                                            | all turn lines of those sessions                                 |
| Q3     | user turn count (`item=-1`) per session                                                                  | all user lines of those sessions                                 |
| Q4     | assistant turns and token sums per (session, model), distinct by `message.id`                            | all assistant lines of those sessions                            |
| Q5, Q6 | per session: first record's cwd and branch (`order by time limit 1`), first user prompt with `raw`       | 2 requests x N sessions                                          |

For the last seven days: 1 308 sessions rows started in the window, 1 149 of them have a main-thread turn,
157 k main-thread turn lines (user and assistant, not sidechain), 3.5 million records in the window, and
1.53 million transcript records belonging to the active sessions (calculation: `count(*)` per the queries
in the next section). The window therefore has about 22 times more records than turn lines (3.5 M /
0.157 M), and only the turn lines matter.

The recorder aggregates in Go, not in SQL (`aggregate` in
`packages/pg-claude-code-recorder/internal/query/query.go`): it streams every row of the filtered
statement through a Go loop and keeps the group maps and the per-message-id `seen` sets itself. So each of
Q1 to Q4 costs its full row stream, and nothing is pushed down.

## Question 1: where the 30 to 60 s goes

### EXPLAIN QUERY PLAN

`is_sidechain` is stored as a row in the entity-attribute table `fields` (name id 7), not as a column. The
recorder emits a `LEFT JOIN fields f0 ... name_id=7` and the filter `f0.n=0`. For Q1, Q2 and Q4 (same
shape) the planner chooses:

```text
|--SEARCH f0 USING COVERING INDEX fields_name_n (name_id=? AND n=?)
|--SEARCH r USING INTEGER PRIMARY KEY (rowid=?)
|--LIST SUBQUERY (session ids)  ... SEARCH sessions USING COVERING INDEX sqlite_autoindex_sessions_1
`--SEARCH fl USING INTEGER PRIMARY KEY (rowid=?)
```

It drives from `fields_name_n`: all `is_sidechain=false` rows of all history (4.17 million rows of 9.79
million `is_sidechain` rows), then one random primary-key lookup into the 13 GB `records` table per row,
and only then tests time, type and session. Neither the `time` bound (Q1) nor the session list (Q2 to Q4)
drives the plan. The index `records_session_ref_source_time(session_ref, source, time_ns)` exists and
would serve Q2 to Q4 directly, but is not chosen. The per-session queries Q5 and Q6 are planned well
(`records_session_ref_source_time (session_ref=? AND source=?)`), about 1 ms in SQL.

### Timings (`sqlite3 -readonly`, 7 day sessions, 1 149 ids as literals)

| Statement                   | As the recorder emits it | Forced onto the session index                        | Rows         |
| --------------------------- | ------------------------ | ---------------------------------------------------- | ------------ |
| Q1 window scan              | 16.0 s                   | 11.6 s (forced `records_time`)                       | 156 989      |
| Q2 span                     | 9.0 s                    | 2.1 s (`INDEXED BY records_session_ref_source_time`) | 1 193 groups |
| Q3 user turns               | 2.6 s                    | 1.2 s                                                | 62 137       |
| Q4 assistant, model, tokens | 2.6 s                    | 2.2 s                                                | 91 800       |

Q1 is slow whichever index is used because it must look at all 3.5 million window records to find the
157 k turn lines (the window is 96 % non-turn records: attachments, api-request, tool_use and so on).
Q3 and Q4 were faster than Q2 only because Q2 had just warmed the `fields` pages; in a cold run each pays
the Q2 penalty.

### The per-session loop

Through the daemon socket (`curl --unix-socket`), one request of each kind for 1 309 sessions, minus the
curl and shell overhead measured with the trivial `/v1/status` endpoint over the same number of requests:

| Loop                   | Wall   | Overhead (1 308 x `status`) | Net    |
| ---------------------- | ------ | --------------------------- | ------ |
| cwd and branch (Q5)    | 40.6 s | 19.2 s                      | 21.4 s |
| first prompt, raw (Q6) | 29.0 s | 19.2 s                      | 9.8 s  |

Net 31.2 s for 1 309 sessions, so about 27 s for the 1 149 sessions pa-monitor loops over (31.2 x 1 149 /
1 309). That is 16 ms of daemon time per session for two statements that are sub-millisecond in SQL: the
cost is the per-request transaction, the daemon's own query machinery and the HTTP hop, times 2 298.
In-process pa-monitor pays less hop overhead than curl, so treat 27 s as an upper bound; the lower bound
from the 50-request sample (13 ms plus 4 ms per session, 17 ms x 1 149 = 19.5 s) is about 20 s.

### Putting it together

Sequence for a cold-ish run: Q1 15 to 21 s, Q2 9 s, Q3 and Q4 5 s, loop 20 to 27 s: 49 to 62 s. This
matches the 60.2 s run that ended in `query_timeout`. The reported 34.4 s was a warmer run (Q1 and Q2
cheaper). Either way the two aggregate scans (Q1, Q2) and the per-session loop are each larger than the
30 s deadline's remaining margin, and Q1 alone has exceeded 30 s once in this session (a bare Q1 through
the CLI returned in 21 s once and hit the 30 s deadline the next time).

## Question 2: what the session tables can and cannot serve

Schema (from `packages/pg-claude-code-recorder/internal/parser/sessions.go` and the live database):
`sessions(session_ref, session_id, project, first_time_ns, first_version_id, entrypoint_id,
user_type_id)` and `session_stats(session_ref, last_time_ns, record_count)`. `flushSessions` upserts one
`session_stats` row per session per write transaction and takes `last_time_ns` and `first_time_ns` from
every record the session has, whatever its source, type or sidechain flag. I compared both columns
with pa-monitor's own definition (min and max time of main-thread `claude-transcript` user and assistant
lines) over the 1 193 sessions of the window:

| Fact                        | Column today                          | Agrees with pa-monitor's definition                                          | Verdict                                                                          |
| --------------------------- | ------------------------------------- | ---------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| First time                  | `sessions.first_time_ns`              | 492 within 5 s, 993 (83 %) within 60 s, 51 off by more than 1 h              | not exact; usable only to pre-select sessions with slack                         |
| Last time                   | `session_stats.last_time_ns`          | 98 (8 %) within 60 s, 747 are more than 1 h later, 551 more than 1 day later | no: it includes non-turn records (and a rebuild-time bump)                       |
| User turns                  | none                                  |                                                                              | needs a new column                                                               |
| Assistant turns             | none                                  |                                                                              | needs a new column                                                               |
| Tokens per model            | `ext_assistant_usage` per record only |                                                                              | needs a new table                                                                |
| cwd, branch of first record | per record (`cwd_id`, `branch_id`)    |                                                                              | cheap from the index (below), or store at first sight like `first_version_id`    |
| First prompt                | raw line in the copy file             |                                                                              | cannot be a column cheaply; read from the copy at the first user record's offset |

The first-time column is also fragile by construction: the insert is `ON CONFLICT DO NOTHING` with the
time of the first record the parser meets, and a backfill runs newest first, so a session may be created
from a later record and never lowered. The examples with large gaps are the lowest session refs (created
first), which supports this reading; I did not trace each one.

### Write-path cost of new columns (design, not measured)

The parser already batches per transaction (`sessionBatch`, one upsert per session at commit), so a
counter costs O(1) memory per record and no extra statement per record.

| Addition                                                                            | Write cost                                              | Catch                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ----------------------------------------------------------------------------------- | ------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `turn_first_ns`, `turn_last_ns`, `user_turns`, `assistant_turns` on `session_stats` | zero extra statements: same upsert, more columns        | the recompute-after-delete path (`recomputeSessionStats`) must recompute them too; schema bump plus rebuild or backfill of existing rows                                                                                                                                                                                                                                                                                                                                                      |
| `session_model_usage(session_ref, model, turns, in, out, cache_read, cache_write)`  | at most one upsert per (session, model) per transaction | the read side de-duplicates by `message.id` (a streamed message appears on several lines); the writer needs a "seen message" set or an agreed first-wins or last-wins rule. That rule is a design decision this investigation does not make. A `(session_ref, message_id)` table would add one indexed insert per assistant message: about 92 k rows per week next to the 17 million records and their five-plus index entries each, so under 1 % of write work, but permanent storage growth |

## Question 3: recommendation and expected time

**Recommended: a recorder-side `sessions` summary operation that computes the rollup from the session
index in one request, with no schema change first.** pa-monitor then makes one call instead of about
2 300, and the recorder owns the plan.

Design sketch (a Facade over the existing tables, so pa-monitor stops composing it from the generic query
API):

1. Candidate sessions from `sessions` by `first_time_ns >= since - slack` (milliseconds, 12 k rows); the
   exact first main-turn time decides membership afterwards.
2. One SQL statement per fact group, each forced to drive from `records_session_ref_source_time` over the
   candidate `session_ref`s (SQL `GROUP BY`, not row streaming into Go): span, user turns, assistant
   turns and tokens per model.
3. cwd and branch of the first main record as a correlated subquery over the same index (measured 19 ms
   for all 1 309 sessions in one statement).
4. First prompt: one read of the copy file at the first user record's offset per session, in-process,
   no HTTP and no transaction per session.

Expected time for 7 days, from the measured parts (warm / cold-ish in SQL): candidate selection about
0.1 s; span 2.1 s to 3.2 s; user turns 1.2 s; assistant, model and tokens 2.2 s; cwd and branch 0.02 s;
first prompts 1149 in-process reads, estimated 1 s (4 ms per request through HTTP today includes the
hop; in-process it is lower). Sum: 6.6 s warm, up to about 10 s if each statement pays a cold-page
penalty. **Estimate: 5 to 10 s, a 4x to 8x cut from the 49 to 62 s measured, and well inside 30 s.**
Assumption: the same row counts and a similar page-cache state; the recorder keeps backfilling and the
store keeps growing, so recheck after the backfill completes.

**Second step, only if 5 to 10 s is not acceptable:** the parser-maintained columns in Question 2
(turn first and last, turn counts, per-model usage). With them the rollup is a join over 1 149 small rows
plus the first-prompt reads, expected well under 2 s, at the write-path cost listed above and a schema
bump. I would not start with this: it needs the message de-duplication decision and a backfill, and the
single-pass operation already removes the dominant cost.

**Also worth doing independently:** `ANALYZE` has never run (no `sqlite_stat1`), and the planner picks
the `fields_name_n` index for a bounded query. That is the generic hazard for every recorder query that
filters on a field stored as a `fields` row. Whether an `ANALYZE` (or `PRAGMA optimize`) fixes the plan
choice is untested: it writes to the database, so I did not try it on the live one, and a copy would be
13 GB.

"No change" is not recommended: a 7 day default that takes longer than the deadline in a cold cache is
a reliability defect of the feature, not a tuning question.

## Question 4: substring search inside a window

How substring works (`substring` in `packages/pg-claude-code-recorder/internal/query/search.go`): the
candidates come from `records` and `texts` for the filter, newest first, and the recorder reads each
candidate's text from its copy file and runs `strings.Index`. It stops at a 256 MiB byte budget
(`DefaultSubstringBudget`) or the query deadline and returns a partial answer with `scanned_to`.

Measured with a fragment that matches nothing (so the scan cannot stop early), over `time >= now - N d`,
through the daemon, with `--allow-partial`:

| Window | Wall   | `scanned_bytes` | `scanned_to` (window starts 2026-11-04 ~20:40 for 1 d) | Result            |
| ------ | ------ | --------------- | ------------------------------------------------------ | ----------------- |
| 1 d    | 29.3 s | 55.8 MB         | 2026-11-04 22:08 (about 22.5 of 24 h)                  | partial, deadline |
| 2 d    | 29.3 s | 60.3 MB         | 2026-11-04 20:48                                       | partial, deadline |
| 7 d    | 29.3 s | 92.0 MB         | 2026-11-04 16:23 (about 28 h of 168 h)                 | partial, deadline |

Rates: 55.8 MB / 29.3 s = 1.9 MB/s now, while the earlier 12.2 s one-day run (42 hits) is consistent with
about 4.6 MB/s at the same volume (55.8 MB / 12.2 s). The throughput difference is the backfill and cache
load. Volume is about 60 to 80 MB of opaque text per day (1 d: 55.8 MB for 22.5 h; 7 d: 92 MB for 28 h =
79 MB/day).

- **Is 12 s for one day expected?** Yes. It is a read of about 56 MB of values one by one from copy files
  at a few MB/s; there is no index to skip it, and the day's text exceeds what the byte-per-second rate
  clears much faster. It is not a planner problem.
- **Does 7 days fit 30 s?** No. 7 days is about 420 to 560 MB (7 x 60 to 80), which exceeds the 256 MiB
  (268 MB) byte budget on its own, and at 4.6 MB/s would take 91 to 122 s, at 1.9 MB/s over 220 s. The
  deadline cuts it to the newest 1 to 2 days (28 h at the observed 92 MB/29 s).
- **What scope fits 30 s?** At 4.6 MB/s the deadline clears 138 MB, about 2 days; at 1.9 MB/s about 57 MB,
  about 1 day. Recommend a 1 day default for substring (a wider `--since` is allowed but is expected to
  return a partial answer, which pa-monitor should show with `scanned_to`). Word search is unaffected
  (about 1 s).

## Suggested follow-ups

- Build the recorder-side sessions summary operation (Question 3), keep a pa-monitor fallback only if the
  recorder lacks the operation.
- Decide the de-duplication rule for message ids before any per-model usage table.
- Retest the whole table of this page after the first backfill ends, from a warm and a cold cache.
- Look at the substring read path (per-value copy reads) if a wider default window is wanted later.
