# Plan: pg-claude-code-recorder phase 6, SQLite hygiene and a rolling doctor

Repo root: `/Volumes/gitrepos/phillipg/nix-agent-support` (public repo: no employer-specific detail). Package:
`packages/pg-claude-code-recorder`. Spec: `docs/superpowers/specs/2026-10-30-pg-claude-code-recorder-design.md`.
Earlier plans in this directory: phase 0 (`2026-11-03-...`), phases 1 to 5 (`2026-11-04-...`); phase 1 holds the
six-phase re-ranking, where this phase is ranked last because its costs are bounded today. Paths are relative to
the package unless they start with `docs/` or `grafana/`.

## Why (measured, post phase 0, build `0.0.0-8ab32e31`, 2026-11-04) and what is bounded

- WAL: 88,027,952 bytes and flat over 500 s; `wal_checkpointed_frames` reaches `wal_frames` only in bursts. Before
  the passive checkpoint loop the WAL had reached 13.9 GB. `index.db` is 30.6 GB. So the WAL problem is solved for
  now; the open question is whether a periodic `TRUNCATE` is still needed to give the file back (a passive
  checkpoint never shrinks it; `journal_size_limit` caps what remains after a restart).
- The doctor runs every `doctor.interval` (24 h) and, in one pass, hashes every file's copy against its original
  under `Copier.mu` (`Copier.WithLock`): for about 58.5k files that is a full read of both sides for the whole pass,
  during which every copier ingest waits. A doctor pass is a once-a-day stall plus a once-a-day I/O burst; phase 0
  did not capture one, so its size is unmeasured (phase 1 adds `loop_duration_seconds{loop="doctor"}` and
  `copier_lock_wait_seconds`). The spec mandates a full-hash comparison, so the way to remove the stall without
  weakening the check is to spread it (below).
- The query planner has no statistics: `ANALYZE` has never run on this database (no `sqlite_stat1`; the table is
  absent from the schema). The existing indexes were hand-chosen, and one planner trap is already documented in the
  schema comments (`records_source_type`, "without it the planner scans every row").
- `commits` is a per-copy-commit table (primary key `file_id, gen, seq`) that grows with every copier commit and is
  never pruned. It is read by `CopyCommits(file)` (parse-lag seconds, which phase 3 maintains in memory instead)
  and by the doctor's journal-versus-index comparison (a table dump). Its size is unmeasured: the task below
  measures it before deciding.
- The read pool has `SetMaxIdleConns(4)` and a 6 h lifetime but no maximum open connections; a burst of concurrent
  readers opens that many connections, each with a 64 MiB page cache and up to 512 MiB mmap ceiling, against a 30 GB
  file. Memory is therefore unbounded in the number of concurrent requests.

## Scope

In scope: an `ANALYZE` decision with evidence; a `records(session, source, time_ns)` index decision with evidence;
a read-pool bound; a checkpoint policy including a `TRUNCATE`; `commits` pruning; a rolling, byte-budgeted doctor
that does not hold the copier lock across files; the spec amendment those need. Out of scope: schema version bumps
that force a rebuild (a rebuild of the 30 GB index took the daemon down and needed an operator decision on disk
space; no task here MAY change the schema version or require a rebuild; an index created with `CREATE INDEX IF NOT
EXISTS` is not a schema version change), vacuuming the database file.

Hard rule for this phase: every task that touches the live index MUST be tested on a copy and MUST state its
expected size class (S, M, L, XL) and write-blocking behaviour in its bead. A new index on `records` blocks writers
while it builds, and `CREATE INDEX` is one statement that cannot be done in bounded steps, so it MUST be offered to
the operator as a stop-the-daemon `rebuild-indexes` command (the bulk rebuild already builds indexes after load:
`buildIndexesLocked`), never run implicitly at start.

## Ground rules

As in earlier plans: real SQLite and files, counting driver, injected clock, no slow test in the unit gate (a test
that needs a large database sits behind the existing `soak` build tag and its own check, never the unit gate), no
bead id in the repo, no new dependency, `nice -n 10 go test -p 4`, `-race` for shared state.

## Task list and order

- T1 (S): measure (read-only): table sizes (`dbstat`), `commits` row count, `sqlite_stat1` absence, plan of the
  five hottest queries before any change; written up as a doc, no code change.
- T2 (M): `ANALYZE` decision and implementation (bounded `PRAGMA optimize` at close and daily, or none).
- T3 (M): `records(session, source, time_ns)` index decision and the `rebuild-indexes` command.
- T4 (S): read-pool maximum open connections and the queueing metric.
- T5 (S): checkpoint policy including a `TRUNCATE` window and the WAL alerts.
- T6 (M): `commits` pruning and the matching doctor and replay rule.
- T7 (L): the rolling doctor.
- T8 (S): spec amendment, README, dashboard, alert text.
- T9 (S, gated follow-up): verification after apply.

Dependencies: T1 first and every other task cites its numbers. T2 and T3 after T1 (the plans in T2 and T3 compare
before and after). T4, T5 independent after T1. T6 after T1 and phase 3's T3 (parse-lag seconds no longer reads
`commits`). T7 independent but SHOULD follow phase 1's T3 (lock-wait metrics) so T9 can compare. T8 after T6 and T7.
T9 gated on the apply of T2 to T8 (`pb:pb-gate-lifecycle`).

## T1. Measurements (no code)

Output: `packages/pg-claude-code-recorder/docs/investigations/<date>-sqlite-hygiene-measurements.md`, with: per-table
bytes and row counts from `dbstat` and `COUNT(*)` run read-only on a COPY of the database (never the live file;
the copy needs about 31 GB of free disk, so the task first checks `df` and stops with a `NEEDS:` line if there is
less than 40 GB free); whether `sqlite_stat1` exists; `EXPLAIN QUERY PLAN` and wall time for the ten most frequent
read statements (derive the list from phase 1's per-op histogram once available, else from `internal/query`);
the size of a doctor pass's file set (files, total bytes of copies). No numbers are estimated in the document: each
is a command output with the command shown.

## T2. `ANALYZE` decision

The decision rule, written down in the task before any code: run `PRAGMA optimize` (which analyzes only tables whose
statistics are stale, with the default `analysis_limit`) on connection close and once a day from the doctor loop;
do NOT run a bare `ANALYZE` on the 30 GB file. Implement only if T1 shows at least one hot query whose plan changes
(or whose measured time changes by more than 2x) after `PRAGMA optimize` on the copy; otherwise record "no change"
in the investigation doc and close the task with no code. Tests (when implemented): after `optimize` on a fixture
with skewed `records` data the plan for a `session` plus `time_ns` query uses the intended index (`EXPLAIN QUERY
PLAN` text); `optimize` failure is logged at WARN and never fails a close; the daily call is on the injected clock.
Negative control: skip the call and the plan test fails.

## T3. `records(session, source, time_ns)` index

Decision first, from T1: build it only if a measured query (the `query` operation with a session filter ordered by
time, or `search`) scans `records_session` and sorts, with a wall time above 1 s on the copy, and the index's
estimated size (rows times the three-column key) is acceptable (the task records the free-space check). Then: add it
to `derivedIndexes` (so the bulk rebuild builds it after load and a new database has it), but do NOT let a normal
open of an existing database build it implicitly: split `derivedIndexes` into `required` (created on open, as
today) and `optional` (created only by the new `pg-claude-code-recorder rebuild-indexes` command, daemon stopped,
which prints progress, the free-space check and the resulting size). The planner uses the index once it exists;
queries are correct without it. Tests: an existing-database open does not create the optional index (assert
`sqlite_master`); `rebuild-indexes` creates it and is idempotent; the plan test; `rebuild-indexes` refuses to run
with the daemon's lock held and with less free space than the estimate. Negative control: put the index in
`required` and the first test fails.

## T4. Read-pool bound

`internal/store/store.go`: `r.SetMaxOpenConns(readMaxOpenConns)` with default 8 and the Nix option and config key
`query.max_connections`. Excess readers queue on the pool (they already time out at `query.timeout`; with phase 5 they
cancel when the client goes). Metric: the existing `db_wait_seconds_total{pool="read"}` and `db_wait_total` already
show queueing; add the panel `read pool wait` (phase 1's dashboard task covers panels; this task adds this one).
Tests: with `max_connections = 2` and four concurrent blocked readers (blocking SQLite function, no sleeping) the
third and fourth wait (`db_wait_total` rises) and proceed when one finishes; a reader's cancel frees its slot; the
config key is validated (at least 2). Negative control: remove the bound, the wait test fails.

## T5. Checkpoint policy

Passive every minute stays. Add a `TRUNCATE` checkpoint attempt when all of: `wal_bytes` is above
`wal.truncate_above` (default 512 MiB), the last passive checkpoint reached `wal_checkpointed_frames ==
wal_frames`, and the daemon is not mid-backfill. It runs with a short `busy_timeout`, accepts `SQLITE_BUSY` as
"try next minute", and logs `wal_truncate_done` (event already exists from phase 0) with the duration. Because a
TRUNCATE waits for readers and blocks writers while it runs, it is attempted at most once per hour. Alerts: phase 1
adds `wal-large`; this task changes its threshold to twice `wal.truncate_above` so the alert fires only when
truncation is not working. Tests (real SQLite, a WAL grown by the test): above the threshold with all frames
checkpointed the WAL file shrinks to under the journal limit and `wal_bytes` follows; with a reader holding a
snapshot the attempt returns busy, nothing is lost, the next attempt after the reader closes succeeds; below the
threshold nothing is attempted (counting hook on the checkpoint function); at most one attempt per hour on the
injected clock. Negative controls: attempt without the all-frames condition (the busy test must show writers
stalled; record that as the reason for the condition).

## T6. `commits` pruning

Decision in T1's document; implement only if the table is a measurable share of the database (the task sets the bar:
more than 5% of the file or more than 10 million rows). Rule: keep the `commits` rows of the CURRENT generation of
every file whose parse offset is below its copy offset (the parse-lag evidence) and rows newer than `commits.keep`
(default 7 d); delete the rest in chunks of 10,000 rows per transaction, one chunk per daily doctor run per minute
to avoid long write locks. Design constraint: the doctor compares the journal replay with the index tables,
including `commits` (`internal/store/query.go`, the dump list). The journal is the source of truth and is never
pruned, so a pruned index would differ from a replay. Resolution, to be confirmed against the spec before
implementation (this is a design decision the task MUST NOT make silently; it files the question and stops if the
spec is silent): exclude `commits` rows older than the keep window from the comparison, and apply the same keep
rule when a replay rebuilds the table, so both sides agree. Tests: pruning keeps the unparsed generation's rows and
recent rows and deletes the rest; chunk size is honoured (count of `DELETE` statements through the counting
driver); the doctor's journal check passes after pruning; a rebuild from the journal yields the same pruned table
(equality of the dump after applying the keep rule to both). Negative control: delete without the unparsed-keep
clause (the first test fails).

## T7. The rolling doctor

Files: `internal/settle/doctor.go`, `internal/serve/removal_ops.go` (`doctorLoop`), `internal/config/config.go`.
Today one run does everything under `Copier.WithLock`. New design:

- State: a cursor file `doctor-cursor.json` in the state directory (no schema change): `{cycle_started_ns,
next_file_id, files_done, bytes_done, mismatch_files_so_far}` written atomically after each slice.
- A slice processes files in id order from the cursor until it has hashed `doctor.bytes_per_slice` bytes (default
  256 MiB) or finished the file set; slices run every `doctor.slice_every` (default 1 minute) while a cycle is open
  and the daemon has no pending copier backlog (`copy_lag_bytes` below a bound; otherwise the slice is skipped and
  counted in `doctor_slices_total{outcome="deferred"}`).
- Locking: take `Copier.mu` only to snapshot a file's `(CopyOffset, generation)`; hash the copy prefix
  `[0, CopyOffset)` against the original's prefix with the lock released. A committed copy prefix is immutable and
  a changed original prefix is exactly the mismatch we look for. On mismatch, re-take the lock, re-check that the
  generation and offset are unchanged (else drop the finding as a race and requeue the file once), then start the
  new generation as today (`NewGenerationLocked`). Snapshot files keep the `lastSeen` and `mtimeOf` rule.
- A cycle completes when the cursor passes the last file (a file created during the cycle is covered by the next
  cycle). Completing a cycle runs the journal and archive checks (cheap, once per cycle), writes `doctor.json`
  (same format, same fields) and sets `doctor_last_run_timestamp` to the cycle END time. `doctor.interval` keeps its
  meaning as the maximum age of the last completed cycle: a new cycle starts when the last one is older than
  `doctor.interval` minus the expected cycle length (from the previous cycle's duration, with a one-hour floor), so
  every file is verified at least once per `doctor.interval` plus one cycle, preserving the spec's daily full hash.
- The `doctor` CLI command keeps its synchronous full run (an operator ask); it uses the same slice function in a
  loop and holds no lock across files either.
- Metrics (phase 1 conventions): `doctor_slice_duration_seconds` (labelled histogram on `loop="doctor"`),
  `doctor_cycle_progress_ratio` gauge, `doctor_cycle_duration_seconds` gauge of the last cycle, `doctor_slices_total
{outcome}` with `outcome` in `ok deferred error`.

Tests (real files and SQLite, injected clock, no sleeping): a fixture of 100 files, budget set to cover 10 files per
slice: ten slices complete a cycle, each file is checked exactly once (assert via a per-file counter in a test
seam on the hasher), the cursor file survives a simulated restart mid-cycle and resumes (no file checked twice, none
skipped); a file whose original prefix is corrupted is detected with the same `DoctorMismatch` and the same repair
as the old full run (the old tests run unchanged against the synchronous command as the oracle: equal results on
equal fixtures); a copier ingest running during a slice is not blocked (hold the copier lock briefly in a goroutine,
assert the slice releases it between files: test with a channel-gated hasher); a race (the generation changes
between snapshot and re-check) drops the finding and requeues once; `-race`; a deferred slice when copy lag is
above the bound. Negative controls: hold the lock across the whole slice (the non-blocking test fails); write
`doctor.json` at every slice (the cycle-end test fails).

## T8. Spec, README, dashboard, alerts

Spec amendment (the spec says `doctor` compares every copy by full hash and runs on a timer): replace with "doctor
verifies every copy against its original by full hash at least once per `doctor.interval` plus one cycle, in
byte-budgeted slices that do not hold the copier lock across files; `doctor_last_run_timestamp` is the end of the
last completed cycle", and record the pruning rule for `commits` if T6 landed. README: the doctor and WAL sections.
Dashboard: `doctor cycle progress`, `doctor slice duration`. Alerts: the existing `doctor-mismatch` staleness bound
uses `doctor.interval`; the rule text and its promtool test are updated for the cycle-end timestamp. The probe-uid
lists are unchanged (no new rule uid) unless T5 adds a rule; if so, name the downstream follow-up in the report.

## T9. Verification (gated)

After apply: one complete doctor cycle completed within `doctor.interval`, with no `copier_lock_wait_seconds` p99
above 1 s during it (phase 1 metric); `wal_bytes` stays under `wal.truncate_above` over a day with at most one
`TRUNCATE` per hour; `query.max_connections` honoured (`db_open_connections{pool="read"}` never above it); the
before and after plan and timing table for T2 and T3 filled in from the same commands as T1.

## Risks and rollback

- Index build on 30 GB blocks writers: mitigated by the stop-the-daemon `rebuild-indexes` command and the free-space
  check; no implicit build.
- `TRUNCATE` stalls writers: mitigated by the all-frames condition, hourly cap, short busy timeout.
- Rolling doctor weakens the check: it does not; every file is still fully hashed within the interval, and the
  synchronous command is kept as the oracle and an operator tool.
- `commits` pruning breaks journal equivalence: guarded by the explicit rule and the rebuild-equality test; the task
  stops and files a question if the spec does not decide it.
- Rollback: each task is config-gated (`doctor.bytes_per_slice = 0` means the old single run; `wal.truncate_above = 0`
  disables TRUNCATE; `query.max_connections = 0` means unbounded; `commits.keep = "0"` disables pruning), then revert.

## Proposed bead breakdown

Epic with nine children T1 to T9 and the edges above. Sizes: T1 S, T2 M, T3 M, T4 S, T5 S, T6 M, T7 L, T8 S, T9 S
(gated verification). T2, T3 and T6 are CONDITIONAL on T1: each bead's first acceptance criterion is the decision,
and "no change needed, evidence recorded" is a valid close. Labels `agent-support`, `pg-claude-code-recorder`.
