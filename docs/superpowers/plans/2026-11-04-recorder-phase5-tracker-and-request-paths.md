# Plan: pg-claude-code-recorder phase 5, tracker and request paths

Repo root: `/Volumes/gitrepos/phillipg/nix-agent-support` (public repo: no employer-specific detail). Package:
`packages/pg-claude-code-recorder`. Earlier plans in this directory: phase 0 (`2026-11-03-...`), phases 1 to 4
(`2026-11-04-...`); phase 1 holds the six-phase re-ranking. Paths are relative to the package unless they start
with `docs/` or `grafana/`.

## Why (measured, post phase 0, build `0.0.0-8ab32e31`, 2026-11-04)

- Requests over 10 s: 109 in 66 minutes (11 in a 500 s window); before phase 0 it was 520 of 591 an hour. The
  phase 0 plan asked for no growth and missed. Which operation is slow is not measurable today (the request
  histogram has no `op` label; phase 1 adds it). Two slow-request sources are known from code, independent of
  that:
  1. The default `query.timeout` is 30 s and the client's HTTP timeout (`internal/client/client.go`) is also 30 s.
     When both fire at about the same time the client has already given up, and the daemon keeps running a read
     it can no longer deliver (the log shows `query_timeout ms=30000` for two callers). The read-pool deadline
     (`internal/store/deadline.go`) is a fixed per-statement and per-transaction timer, not tied to the request
     context, so a client that disconnects does not cancel its read.
  2. Reads wait behind writes and behind the settle tick. Phases 2 and 3 remove the largest of those; this phase
     only stops the daemon from finishing work nobody is waiting for.
- The tracker (`trackLoop`, every 5 s, `liveness.interval`) does, on each pass: `Tick` (liveness checks and one
  `Lstat` per live process's inbox FIFO), then `Poll`: `sweepEnds` (a `ReadDir` of `ends/`, and for each end file
  that exists a `FilesByCurrentPath`, `LatestSnapshot` and a `COUNT(*) FROM records WHERE file_id=?`),
  `readRequests` (reads the whole of `requests.jsonl`, splits and JSON-decodes every line, builds a new set of up to
  1,000 keys) and `sweepFifos` (a `ReadDir`, then `Procs()`, a table read of every Process, then a liveness probe
  per FIFO). That is O(window) and O(table) work per 5 s, 17,280 times a day, to learn mostly nothing.
- `feed_marks` has only its `rec_id` primary key; `feed.floor` runs `SELECT MAX(rec_id) FROM feed_marks WHERE
at_ns < ?` per feed request, which scans the table (it is pruned by `snapshot.go`, so it is bounded but not
  small: one row per parse batch inside the feed retention).

## Scope

In scope: tail `requests.jsonl` from an offset; handle `ends/` and the inbox FIFOs by event with a slow fallback
scan; an index for `feed_marks.at_ns`; cancel a read when its client goes away or its declared deadline passes;
make the daemon deadline shorter than the client's; phase 1 metrics for each. Out of scope: the control mod's
file formats (read-only to the recorder), the sweep (phase 4).

Behavioural contract: `control_requests_total` counts each request line exactly once (the existing keyed-dedup
semantics: key is `ts`, `id`, `verb`); `end_reason_source_files_total` counts each end file once; FIFO removal
rules unchanged; wire operations unchanged except the additive deadline header.

## Ground rules

As in earlier plans: real SQLite and files, counting driver and `fsx` recorder for call counts, injected clock, no
slow test in the unit gate, no bead id in the repo, no new dependency, `nice -n 10 go test -p 4`, `-race` for
shared state.

## Task list and order

- T1 (M): tail `requests.jsonl` from an offset.
- T2 (M): `ends/` by event with a slow fallback; remove the per-poll per-file SQL.
- T3 (S): FIFO sweep keyed on process exit events plus a slow fallback; `Procs()` out of the 5 s path.
- T4 (S): `feed_marks(at_ns)` index.
- T5 (L, split T5a and T5b): request-scoped cancellation (context through the read pool) and the client deadline header.
- T6 (S): defaults (`query.timeout` below the client timeout) and README.
- T7 (S, gated follow-up): verification after apply.

Dependencies: T1, T2, T3 touch `internal/lifecycle/tracker.go`: land in numeric order. T4 independent (only
`internal/store/derived.go` and `internal/feed`). T5 after phase 1's T4 (the `op` label) so the verification can see
which operations remain slow; technically independent. T6 after T5. T7 gated on the apply of T1 to T6
(`pb:pb-gate-lifecycle`).

## T1. Tail `requests.jsonl`

File: `internal/lifecycle/tracker.go` (`readRequests`, `reqSeen`). The mod keeps the last 1,000 lines, which means
the file is rewritten or truncated and appended to; the recorder cannot assume append-only. Algorithm: remember
`(inode, size, offset, tailHash)` from the last read, where `tailHash` is the hash of the last 64 bytes before
`offset`. On a poll: `Stat`; if inode and size unchanged, return (no read, no decode). If the inode is the same,
size is greater than `offset` and the 64 bytes before `offset` still hash to `tailHash`, read only from `offset` to
the end, decode only the new complete lines, and advance `offset` to the last newline. Otherwise (shrink, new
inode, hash mismatch: a rotation or rewrite) fall back to a full read exactly as today, with the existing keyed
dedup (`reqSeen`) so no line is double counted, then reset the cursor. A trailing partial line is left for the next
poll.

Tests (temp directory, real files, no sleeping): append two lines, poll, assert both counted once and the bytes read
equal the appended bytes (use the `fsx` recorder); poll again with no change, assert zero reads; append a partial
line, assert not counted until its newline arrives; rewrite the file keeping the last 1,000 lines with the oldest
dropped (the mod's behaviour): assert nothing is double counted and new lines are counted; replace the file with a
new inode; a file that shrinks. Negative controls: drop the `tailHash` check (the rewrite test must fail by
double counting or missing lines); advance `offset` past a partial line.

## T2. `ends/` by event

Files: `internal/lifecycle/tracker.go` (`sweepEnds`), `internal/serve/daemon.go` (`afterIngest`).
`afterIngest` already calls `tr.Observe(p)` for registry paths; add `tr.ObserveEnd(p)` for paths under
`ends/`. The first validation (the `ingested|invalid` counter) happens there, from the copier's result, not by
re-reading the file on a poll. The removal part (an end file is removed a day after its snapshot was seen and its
records exist) is time-driven: keep a `map[path]removeAfter` filled by `ObserveEnd` and a slow
`ends.sweep_every` fallback scan (default 10 minutes, also at start) that rebuilds it with one `ReadDir`; the 5 s
poll only checks the map head. The per-file SQL (`FilesByCurrentPath`, `LatestSnapshot`, the `COUNT(*)`) runs when
an entry comes due, not every poll.

Tests: ingest 3 end files (valid, invalid JSON, valid): counters 2 ingested, 1 invalid after one event each and
no change after further polls; with the counting driver a poll with 1,000 end files none due issues zero store
queries (today: more than 1,000); advance the injected clock past `EndFileMaxAge` and the due file is removed and
`removed` counts once; the fallback scan recovers an end file the event path missed (simulate by writing the map
empty). Negative control: validate on both event and fallback (must fail by double count).

## T3. FIFO sweep

`sweepFifos` reads `Procs()` (every Process row ever) and probes each FIFO each poll. New: remove a FIFO on the
tracker's process-exit callback (it already knows the pid; `removeFifo` exists) and run the existing full sweep
only at start and every `fifo.sweep_every` (default 10 minutes). Tests: a process exit removes its FIFO within
the same wake (fake `proc` system); a FIFO whose process died while the daemon was down is removed by the start
sweep; with the counting driver, a poll between sweeps issues zero queries; `stale_fifos_removed_total` counts each
removal once.

## T4. `feed_marks(at_ns)` index

File: `internal/store/derived.go` (`derivedIndexes`). Add
`{"feed_marks_at", "CREATE INDEX IF NOT EXISTS feed_marks_at ON feed_marks(at_ns)"}`. Normal open runs
`derivedSQL`, which includes the index list, so an existing database gains it at the next start with no schema
version change; building it on a small, pruned table is cheap, unlike an index on `records` (phase 6). The task MUST
first measure the table's row count on a copy of the live index (read-only, `sqlite3` against a copy or the daemon's
own `coverage` path; record the figure in the task's commit message body as a number, not an id) and stop to
report if it exceeds 5 million rows. Tests: the query plan (`EXPLAIN QUERY PLAN` on the `floor` query) uses
`feed_marks_at` (assert the plan text contains the index name); a store opened on a database created without the
index gains it; the bulk rebuild path (`buildIndexesLocked`) also creates it (the schema is the same either way).
Negative control: remove the index and the plan test fails.

## T5. Request-scoped cancellation

Files: `internal/server/server.go`, `internal/store/deadline.go`, `internal/query`, `internal/feed`,
`internal/serve/ops.go`, `internal/client/client.go`, `internal/wire/wire.go`.

1. Audit: every operation handler receives `ctx` (the HTTP request context, cancelled when the client disconnects)
   but only some pass it to the store. List the operations (16) and, for each, whether its reads use `ctx`. The
   audit table goes in the commit message body; operations that ignore `ctx` get it threaded through
   (`BeginTx(ctx, ...)`, `QueryContext`). The store's read pool exposes `ReadTxCtx(ctx, fn)`; the existing
   `ReadTx(fn)` becomes `ReadTxCtx(context.Background(), fn)`.
2. The deadline wrapper (`dconn`) applies `min(its fixed timeout, request deadline)`: a connection's statement ends
   when the request context ends, then is returned to the pool clean (SQLite `interrupt` through the driver's
   context support; test with the real driver, the interrupt semantics matter, a mock does not count).
3. Additive wire header `X-Recorder-Deadline-Ms` set by the client from its own timeout minus 1 s; the server
   applies it as a request deadline when present and shorter than `query.timeout`. Old clients send no header and
   behave as before.
4. Metrics: `request_cancelled_total{op,reason}` with `reason` in `client_gone deadline` (phase 1's `op` label
   enumeration), so cancellation is visible.

Tests (real SQLite, a slow read provided by a large generated table and a CPU-bound query so it runs long enough
without sleeping; or a registered SQLite function that blocks on a channel, added by the test through the driver, so
no timing is needed): cancel the context mid-read and assert the call returns the context error promptly, the
connection returns to the pool and a following read on the same pool succeeds; a request with a deadline header of
50 ms against the blocking function returns `query_timeout`; a request without a header uses `query.timeout`; the
cancelled counter increments with the right reason; `-race`. Negative controls: ignore the context in one operation
(its cancel test must fail, so the audit table cannot silently regress: add a table-driven test that runs every
registered operation against a blocking store and asserts each returns on cancel).

## T6. Defaults and docs

`query.timeout` default 25 s (below the client's 30 s) in `internal/config` and the module option; the
`DefaultReadTimeout` constant in `store` follows. README: a runbook entry "requests time out" (read `request
duration p99 by op`, `request_cancelled_total`, then `status` loops), the new header, the new keys
(`ends.sweep_every`, `fifo.sweep_every`), and the tracker description. Spec: the tracker polling text and the
request deadline text. Check the module tests render the new keys.

## T7. Verification (gated)

After apply and one hour: requests over 10 s by op (phase 1 panel) versus the 109 per 66 minutes figure;
`request_cancelled_total`; the tracker loops' p99 and a 30 s profile showing `Tracker.Poll` below 1% of samples. If
slow requests remain, the per-op histogram names the operation and a bead is filed against it (do not guess here).

## Review findings folded in (corrections to the sections above; where they disagree, this section wins)

1. T5 is larger than drafted (size L, not M). `Store.ReadTx` begins its transaction with a background context and the
   `Reader` methods take no context, so `ReadTxCtx(ctx, fn)` alone cancels only the `BeginTx`; statements inside `fn`
   keep running, and the transaction's cancel-rollback waits for an in-flight statement. Real cancellation needs
   context variants of the `Reader` methods used by each operation and of `computeCoverageFrom`. T5 is split: T5a
   (M) adds the ctx-taking `Reader` methods and `ReadTxCtx`, converting operations one at a time starting from the
   ones that already take `ctx` (`feed.once`, `Snapshot`, `Raw`); T5b (M) the server-level cancellation mapping,
   the header and the metrics. The deadline wrapper (`dconn.bound` already takes the earlier of the two deadlines;
   inside a transaction the end is fixed at `BeginTx`, so a per-request deadline works only through the `BeginTx`
   context, which T5a guarantees) needs no change beyond that statement and a test.
2. Error classes. `wire.FromErr` maps only `DeadlineExceeded` to `query_timeout`; `context.Canceled` would become
   `internal`. T5b adds a `cancelled` mapping that is kept out of `query_timeouts_total` and the `internal`
   metrics, and tells `client_gone` from `deadline` by checking the request context's own error before the
   store's (the store collapses both to the context error). A cancelled request logs a `request_cancelled` event next
   to the existing `query_timeout` event.
3. The deadline header is clamped: at least 1 s, never above `query.timeout`; the client sets it per request from
   its own HTTP timeout minus 1 s (`client.go`), and the server reads it in `route`. Both files are in T5b's list. A
   25 s default turns some 25 to 30 s answers into `query_timeout`; the README and runbook say so.
4. T3 is reworded: `onExit` already removes a FIFO (`removeFifo`); the work is only to drop `sweepFifos` and the
   `Procs()` table read from the 5 s path and keep the full sweep at start and every `fifo.sweep_every`, including
   the branch for a FIFO whose process was never tracked (the `StartTime`/`ErrGone` case).
5. T1 specifics: the cursor is `(inode, size, offset, hash of the 64 bytes before offset, hash of the first 64
bytes)`; the tail path is taken only when all match; `reqSeen` is updated incrementally on a tail read (new keys
   added, none dropped) and rebuilt on a fallback full read, so a later fallback cannot re-count. `readRequests`
   reads through a small `fs` seam (default `os`) so tests count bytes read; today it calls `os.ReadFile`.
6. T2 specifics: `ObserveEnd` needs a matching `scope` class for `ends/` paths (check `scope.Classify`); the copier's
   result does not say whether the JSON is valid, so the file is read once at event time (one read per file ever,
   not per poll), and the plan no longer claims validation comes from the copier's result.
7. Config and wiring: `ends.sweep_every`, `fifo.sweep_every`, `query.timeout` default and the header need keys in
   `internal/config` (pattern at `config.go`), the Nix module option and `tests/pg-claude-code-recorder-hm-launchd.nix`;
   the new timers use the tracker's injected `Clock`; start-up runs each fallback once, `Close` stops them.
8. Dependencies: T5b and T7 hard-depend on phase 1's T4 (`op` label); the "technically independent" wording is
   dropped. The query counting driver is the phase 0 task's `internal/store/countdriver` package. The operation count
   is whatever `rg -n 'srv.Register' internal/serve` lists (the audit table is generated from it, not from the
   number 16).
9. Observability added: `requests_json_reads_total{path}` with `path` in `tail full` (tail fast-path hits versus
   full fallbacks), `tracker_fallback_found_total{kind}` with `kind` in `ends fifos` (a find by the fallback scan
   means the event path missed one), `tracker_poll_duration_seconds` (through phase 1's loop wrapper, loop
   `tracker_poll`), and the `request_cancelled` event. T5b's test covers the server-level `cancelled` mapping for
   every registered operation.

## Risks and rollback

- Tail cursor mistakes lose or duplicate request counts: guarded by the keyed dedup retained as the fallback and by
  the rewrite tests.
- An interrupt leaves a connection in a bad state: tested with the real driver; rollback is to ignore the request
  context in the wrapper (one function) and revert the default timeout.
- The `feed_marks` index changes plans: one table, one query; measured first.

## Proposed bead breakdown

Epic with seven children T1 to T7 and the edges above. Sizes: T1 M, T2 M, T3 S, T4 S, T5 M, T6 S, T7 S (gated
verification). Labels `agent-support`, `pg-claude-code-recorder`.
