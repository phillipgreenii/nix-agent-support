# Op `query`: 4461 requests over 10 s, all cancelled by the client (2026-11-09)

Evidence is the launchd log (2026-11-06 19:08 to 2026-11-09 01:17, local) and read-only probes of the live daemon.

## What the log says

- `slow_request`: 2715 lines, 2407 of them `op=query client=pa-monitor-daemon`, steady at 38 to 49 per hour (the log is
  capped at one per op per minute, so the true count is higher: 4461 in 35 h per the counter). The other callers are
  `process.list` (176, pa-monitor-daemon), `feed` (81), `conversation.show` (36, ccpool), `snapshot` (9) and `doctor` (6).
- Every `op=query` request over 10 s that was logged as `request` has outcome `cancelled` (6757 of 6757 for
  pa-monitor-daemon). Their durations: 3386 between 19 and 21 s, 2625 between 21 and 30 s, 641 between 30 s and 173 s,
  105 under 19 s. The spike at 19 to 21 s is `readTimeout = 20s` in pa-monitor's corpus reader (`sync` uses one
  20 s context for the whole bootstrap, drain and fold); the 21 to 30 s tail is the time SQLite takes to stop after
  the cancel. The recorder's own `query.timeout` (25 s) accounts for the rest.
- So the requests are not slow because of lock waits: `lock waits` and `copier_lock_wait_seconds` show 33 s in total
  over 20336 waits, `db_wait_seconds_total{pool=read}` is 0, and the read pool has 1 to 2 connections in use.

## Which queries

The recorder logged no query shape for a cancelled or slow request (only `query_timeout` did), so the shape was found
by reading the caller (`packages/pa-monitor/internal/core/corpus/reader.go`) and timing the shapes against the live
daemon (`pg-claude-code-recorder query --client ...`, read only):

| shape (pa-monitor caller)                                                                                                      | result                      |
| ------------------------------------------------------------------------------------------------------------------------------ | --------------------------- |
| `select type limit 1` (bootstrap cursor)                                                                                       | 0 s                         |
| inventory: group by file, source, project, file_session, agent_id, `max(time)`, status OR transcript with `time >= week start` | `query_timeout` at 30 s     |
| same with `time >= 12 h ago`                                                                                                   | `query_timeout` at 31 s     |
| group by the same 5 columns, `source = claude-status` only                                                                     | 23 s, 930 KB answer         |
| `count` where `time >= 1 h ago` (about 9.4 k rows)                                                                             | 13 s, later `query_timeout` |
| group by file, `time >= 10 min ago`                                                                                            | 21 s                        |

## Cause

1. `Engine.aggregate` (`internal/query/query.go`) issues `SELECT <columns> FROM records r JOIN files fl ... WHERE ...`
   with no `GROUP BY`/`COUNT` in SQL and aggregates in Go, so every matching row is read. The plan is
   `SEARCH r USING INDEX records_time` plus a primary-key lookup of `files` per row (`EXPLAIN QUERY PLAN`), a
   non-covering index, so each matching row costs a random read of the 13.8 GB `index.db`.
2. The same 9.4 k-row range took 12 s in `sqlite3` with 0.6 s of user CPU: the time is I/O, not CPU. At the time of the
   probes the host had a load average of about 40 and about 95 MB of free memory, so the page cache cannot hold the
   index and table pages and each row is a cold read.
3. pa-monitor's `bootstrap` takes its feed cursor only after the inventory succeeds (`r.cursor = head.AsOf` after
   `inventory`), and `sync` runs `bootstrap` whenever the cursor is empty. When the inventory exceeds the 20 s
   `readTimeout`, the cursor stays empty and the next Scan runs the same inventory again, so it never converges: that is
   the steady 45 per hour. The `process.list` slow requests are the same host pressure on a cheaper query.

## What changed here

`slow_request` and `request_cancelled` now carry `request_id`, `query_kind`, `args`, `wait_ms` and `exec_ms` (the fields
`query_timeout` already had), so the next occurrence names the query in the log.

## Not fixed here (separate seams)

- pa-monitor (`corpus/reader.go`): make the inventory cheap or incremental (narrow window or a per-file op instead of
  a grouped scan over all status and transcript rows), and keep a partial bootstrap's progress so a timeout does not
  restart it.
- Recorder: push `GROUP BY` and aggregates into SQL, or serve per-file facts (file, project, session, last time) from the
  `files` and `generations` tables, which need no scan of `records`.
- Host: memory pressure (about 95 MB free, load about 40) makes any scan of the 13.8 GB database slow; a live-host fact
  not verifiable from the repo.
