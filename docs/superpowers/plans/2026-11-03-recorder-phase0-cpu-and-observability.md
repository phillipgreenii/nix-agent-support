# Plan: pg-claude-code-recorder phase 0, stop the CPU burn and make the burn visible

Repo root: `/Volumes/gitrepos/phillipg/nix-agent-support` (public repo: no employer-specific detail in code, tests,
comments or docs). Package: `packages/pg-claude-code-recorder`. Design of record:
`docs/superpowers/specs/2026-10-30-pg-claude-code-recorder-design.md` (the spec) and
`docs/adr/0115-pg-claude-code-recorder-domain-model-and-access-rule.md`. This plan covers phase 0 only: two query
fixes, a WAL checkpoint and its metrics, and the metrics, dashboard panel and alert that let us measure them.
Phases 1 to 6 are listed at the end and get their own plans.

All task paths are relative to `packages/pg-claude-code-recorder` unless they start with `docs/` or `grafana/`.

## Why now: measured evidence (2026-11-02 to 2026-11-03, one machine, daemon up about 10 h)

The operator rule this plan serves: every file the recorder handles MUST be handled as efficiently as possible, and
a periodic sweep MUST NOT re-read all files; it identifies files that changed since the last read and reads only
the bytes since the last read. Phase 0 does not finish that rule (the sweep is phase 4); it removes the largest
measured cost and makes the rest measurable.

Three 30 s CPU profiles from the daemon's pprof socket, one allocation profile, and read-only SQLite probes:

- `Settler.Tick` is the largest consumer: 51.7%, 54.3% and 61.3% of CPU (cumulative) in the three profiles.
  `changedSinceSettle` is 44.5% to 60.1% of it.
- The cost is `Store.AllGenerations`, a `SELECT` of the whole `generations` table, called once per settled
  conversation per tick. It is 85.9% of the 17.31 TB allocated since the daemon started, and the GC share (7.3% to
  9.4% of CPU) follows from it. The store has 26,412 generation rows, 58,533 files and 7,777 settled conversations,
  so one tick reads about 2.05e8 rows (7,777 x 26,412 = 205,406,124).
- `conversation.show` is slow: `transcriptFacts` is 20.1% of CPU in one profile, and its `cwd` query plans as a
  scan of every `cwd` field (`SEARCH f USING INDEX fields_name_n (name=?)`, a rowid lookup, then a sort) because
  the store has no `sqlite_stat1`. `query_duration_seconds` sums 88,837 s over 591 requests, and 520 of them took
  over 10 s, so the slow ones average at most 171 s each (88,837 / 520 = 170.84). The histogram has no `op` label, so
  that the slow ones are `conversation.show` is inferred.
- The WAL cannot reset: `index.db` is 23,486,660,608 bytes and `index.db-wal` is 7,391,300,632 bytes. Total query
  time over uptime means about 2.4 reads are in flight on average (88,837 s / 37,062 s). That readers pin the WAL is
  inferred, not measured.
- Nothing shows any of this: no per-loop duration, run counter, tick log line, Go runtime or process metric, or WAL
  metric exists, and no dashboard panel or alert covers CPU or loop health.

Unknowns phase 0 resolves by measuring: the real settle tick duration, whether any checkpoint ever completes, and
the CPU after the fixes.

## What T1 does and does not remove

T1 removes the per-conversation `AllGenerations` read. These unbounded or per-conversation reads remain in the tick
path after T1 (read from source; phases 2 and 3 remove them) and the tick-duration metric measures their sum:

- once per tick: `groups()` (`AllFiles` and `AllPaths`, `settle.go:266-270`, twice when removal is enabled);
  `refresh()` (`Stages`, `AllGenerations` at `settle.go:1318`, `AllRanges` at `1319`, `TornTails`, `AllFiles`);
  `refreshRemovalGauges` (`remove.go:915-927`: a `newest()` stat per file of every settled conversation, even with
  removal disabled).
- per conversation: `Stage` and `SettledOf` (the manifest blob plus a JSON unmarshal).
- outside the tick: `Parser.UpdateGauges` (`parser.go:604`) and `serve/ops.go:34` read `AllGenerations` once per call
  (every 5 s and on every scrape).

So the 44% to 60% is the ceiling of T1's saving, not a promise, and the 0.5-core figure in the verification section
is a hypothesis to be set from the T6 baseline.

## Scope

In scope: T0 to T7 below. Out of scope, deliberately (each has a later phase):

- Event-driven settle (a dirty set plus due times) and incremental stage counts: phase 2.
- `refreshGauges` and `refresh()` bulk reads, scrape-triggered refresh: phase 3.
- Sweep re-ingest of unchanged files, `SweepMissing`: phase 4.
- Tracker 5 s poll, `requests.jsonl` re-read, cancelling a query whose client timed out: phase 5.
- `ANALYZE`, a composite index on `records`, `TRUNCATE` checkpoints, read-pool bounds, commits pruning, a rolling
  doctor: phase 6. A new index on a 23 GB database blocks writes while it builds, and `ANALYZE` changes every query
  plan, so neither is a quick fix.
- Labelled histogram support in the registry, the `op` label on the query histogram, HELP lines, the unbounded
  `clone_private_size_bytes{file}` label (known violation of the catalogue rule), `status` showing live and quiet
  counts, CPU and WAL alerts (they need the T6 baseline): phase 1.

## Ground rules

- Each task lands on its own and is tested with real SQLite and real files (global rules V-3, V-4). Counting tests
  assert READ COUNTS, never wall-clock time. Anything slow or large sits behind an existing non-unit tag (`soak`);
  the unit gate (`nix build .#checks.<system>.pg-claude-code-recorder-go-tests`, 120 s per package, unit tier only)
  never gets a slow test and its timeout is never raised.
- Run Go tests directly as `nice -n 10 go test -p 4 ./internal/<pkg>/...` from the package directory (add `-race`
  for T3). Nix checks run through `pg-nix-log-wrapped` with an explicit timeout or in the background.
- A new metric is a `const M...` in `internal/obs/obs.go` AND one line in `grafana/pg_claude_code_recorder_metrics.txt`,
  or `grafana/check-signals-registration.sh` fails (nix check `test-pg-claude-code-recorder-signals-registration`;
  confirm the attribute with `nix flake show`). Labels are bounded and enumerated; the catalogue header gets an
  enumerated-values line for every new label.
- Every exported name carries the prefix `pg_claude_code_recorder_` (the exporter prepends it). Dashboard and alert
  expressions MUST use the prefixed names. Names in this plan are given without the prefix.
- Do not add an index, run `ANALYZE`, or change a PRAGMA other than the one named in T3. No schema change, no data
  migration.
- No wire change: no operation, field, exit code or output format of the daemon or CLI changes in phase 0 (the
  unchanged wire golden tests under `internal/wire` are the evidence).
- No bead id appears in code, comments, docs or commit messages. No new Go dependency: `syscall.Getrusage`,
  `runtime/metrics` and `runtime/debug` are standard library, and `gomod2nix.toml` does not change (do not run
  `update-deps.sh`).

## Task list and order

Sizes are S, M, L, XL (no time estimates).

- T0 (S): a store test seam that counts reads.
- T4a (M): loop metrics, tick stats, the `settle_tick` log line.
- T4b (M): runtime, process and WAL-size collectors.
- T4c (M): dashboard panels, README, spec text, runbook.
- T5 (M): one alert, `settle-tick-slow`.
- T6 (S): the baseline capture (a gated follow-up).
- T1 (S to M): `changedSinceSettle` per-file reads.
- T2 (M): the `cwd` query.
- T3 (M): WAL checkpoint loop and metrics.
- T7 (S): the post-deploy verification (a gated follow-up).

Dependencies: T0 first. T4a depends on T0 (its tests use the seam) and edits `internal/settle/settle.go` (`Tick`
returns stats), `internal/obs/obs.go`, `internal/serve/daemon.go`. T4b depends on T4a (same `obs.go`, same
catalogue) and T0. T4c and T5 depend on T4a and T4b (metric names). T6 is blocked by the apply of T4a, T4b, T4c
(gate keyed on the landed commits' patch-ids) and by at least one hour of the new metrics running. T1, T2 and T3 are
blocked by T6 so the baseline exists before they change the numbers; their landing order among themselves is free
except that T3 edits `obs.go` and the catalogue and therefore lands after T4b. T7 is blocked by the apply of T1,
T2 and T3.

File overlap table (the cause of the ordering above): `settle.go` is edited by T1 (`changedSinceSettle`) and T4a
(`Tick`); `obs.go`, the catalogue and `daemon.go` by T4a, T4b and T3; `grafana/*.json` by T4c; `alerts.yaml` by T5;
`lifecycle/engine.go` by T2 only; `store.go` by T0 and T3.

## T0. A store seam that counts reads

Files: `internal/store/store.go` (`Options`, `openDB`, currently `store.go:198-226`, with `sql.Open("sqlite", dsn)`
called for the read and the write handle), a new non-test package `internal/store/countdriver`.

Change: add `Options.DriverName string` (default `"sqlite"`) and pass it to both `sql.Open` calls. Add
`countdriver.Register(name)` which registers (once per name, safe under `-count=N`) a `database/sql/driver` wrapper
around the SQLite driver and returns a counter with `Queries()`, `RowsRead()` and `Reset()`. It counts statements
passed to `QueryContext`, `ExecContext` and `Prepare`, and rows returned through `Rows.Next`. The package lives
outside `_test.go` files so tests in other packages (`settle`, `lifecycle`, `serve`) can import it; it is
documented as test support and nothing in the daemon imports it.

Tests (package `countdriver` and `store`): a store opened with the wrapper reports one query and N rows for a
`SELECT` returning N rows; `Reset` zeroes the counters; the default driver name still works; two opens with the same
registered name do not panic.

Done when: the tests pass, `go vet` is clean, and `nice -n 10 go test -p 4 ./internal/store/...` passes.

## T4a. Loop metrics, tick stats, the `settle_tick` log line

Files: `internal/obs/obs.go`, `internal/settle/settle.go` (`Tick`), `internal/serve/daemon.go` (`settleLoop`,
`refreshGauges`), `grafana/pg_claude_code_recorder_metrics.txt`.

Change:

1. `Tick` returns `(TickStats, error)`. `TickStats` carries `Examined` (conversations evaluated or skipped, that is
   `len(groups)`), `Unchanged` (already settled and unchanged), `SettledNow` (settled by this tick) and
   `Transitions` (stage changes by this tick). `Tick` already holds each `evaluate` `Outcome`; aggregate them there.
   Update every caller (`daemon.go:401`, `internal/settle/env_test.go`, `settle_test.go`; find them with
   `rg -n 'Tick\(\)'`).
2. Timing and outcome are recorded in one function the loop calls (for example `runTick(step)` in `daemon.go`), so a
   test can drive it without starting a daemon.
3. Metrics (unlabeled histograms, because the registry's histograms take no labels; labelled histograms are a phase 1
   decision once there are 8 or more loops):
   - histogram `settle_tick_duration_seconds`, buckets `LoopBuckets` = 0.01, 0.05, 0.25, 1, 5, 15, 30, 60, 120, 300,
     600, 1800 (60 s is the tick interval, so it gets its own edge; a tick today probably takes minutes). Register it
     in `RegisterStandard` (`obs.go:226`).
   - histogram `refresh_gauges_duration_seconds`, same buckets, observed around the BODY of `refreshGauges` (not the
     5 s loop), so scrape-triggered calls are timed too.
   - counter `loop_runs_total{loop,outcome}` with `loop` in `settle_tick refresh_gauges wal_checkpoint` and `outcome`
     in `ok error` (six series).
   - counters `settle_tick_examined_total` and `settle_tick_transitions_total` (rate-able work per tick).
   - gauges `settle_tick_last_duration_seconds` and `settle_tick_last_timestamp_seconds` (a stalled loop adds no
     histogram samples, so the timestamp is what shows it).
4. One structured event per tick through `obs.Event` (a new `EvSettleTick` constant), fields `outcome`
   (`ok|error`), `duration_ms`, `examined`, `unchanged`, `settled_now`, `transitions`. Field names avoid the bare word
   `settled` because it is also a stage name and a counter. The duration is in milliseconds in the log and in seconds
   in metrics; say so in the catalogue comment. `obs.Event` also appends to an in-memory events slice: check that it is
   capped (about 1,440 events a day); if it is not, construct the daemon's events with the existing disable-capture
   option. A tick that takes longer than 60 s also logs a WARN `settle_tick_slow` through `obs.Log.Warn` (as
   `settle_pass_failed` does), so a slow tick is visible in logs when nothing scrapes. An error tick keeps the
   existing `settle_pass_failed` WARN and records `outcome=error`.
5. `Store.Close` logs a `wal_truncate_done` event with `duration_ms` after its `wal_checkpoint(TRUNCATE)` (and a
   `wal_truncate_start` event before it), so an operator watching an apply can tell the shutdown checkpoint
   finished (see "Apply and restart"). Test: closing a store with a WAL emits both events in order.
6. The catalogue (`grafana/pg_claude_code_recorder_metrics.txt`) gets one line per new name and an enumerated-values
   line for `loop` and `outcome (loop_runs_total)`. Stage gauge help in the catalogue comment states that
   `conversations_by_stage` is as of the last settle tick (up to 60 s old).

Tests:

- Unit (`internal/serve`): `runTick` with a stub step that succeeds records one `loop_runs_total{settle_tick,ok}`,
  one histogram observation, sets both gauges, and emits one `settle_tick` event whose fields equal the stub's
  `TickStats`; a stub that errors records `outcome=error` and still logs `settle_pass_failed`; a stub that takes
  over 60 s on an injected clock logs `settle_tick_slow` (inject the clock, do not sleep).
- Unit (`internal/settle`): `Tick` on a real store fixture with 3 settled-unchanged, 1 live, 1 quiet-then-settled
  conversation returns exactly those counts.
- Unit (`internal/obs`): `LoopBuckets` is strictly increasing with first bound at most 0.01 and last at least 600
  (fails on the old `LagBuckets`, which stop at 300 s).
- `check-signals-registration.sh` passes with the new names (nix check).
- Event capture uses `slog.NewJSONHandler` into a buffer; assert keys and exact values, never durations.

Done when: the tests pass and the registration check passes. Hand-applied negative controls the implementer MUST try
(each makes one named test fail): count `ok` on the error path; rename the `settled_now` key; swap `LoopBuckets` for
`LagBuckets`.

## T4b. Runtime, process and WAL-size collectors

Files: `internal/obs/obs.go` (add `Registry.SetCounter(name, labels, v)`: absolute, ignores a value below the current
one so a counter never moves backwards), a new `internal/serve/runtimemetrics.go`, `internal/serve/daemon.go`
(`refreshGauges` calls the collector, so it runs on every scrape and on the 5 s loop), the catalogue.

Change: exported names (prefix added by the exporter):

- counter `process_cpu_seconds_total` from `syscall.Getrusage(RUSAGE_SELF)` using `Timeval.Nano()` (user plus system;
  no cgo, builds on darwin and linux).
- gauges `go_goroutines`, `go_memstats_heap_alloc_bytes` and counter `go_memstats_alloc_bytes_total` from
  `runtime/metrics` (`/sched/goroutines:goroutines`, `/memory/classes/heap/objects:bytes`, `/gc/heap/allocs:bytes`),
  read with a pre-built sample slice. NOT `runtime.ReadMemStats` (it stops the world on every scrape).
- counter `go_gc_pause_seconds_total` from `debug.ReadGCStats` `PauseTotal` (no stop-the-world).
- gauge `wal_bytes`: `os.Stat` of `index.db-wal` (derive the path from the store path; absent file is 0). It ships here,
  not in T3, so the WAL-size baseline exists before any checkpoint changes it.

Each collector reads into locals and then does one registry `Set` or `SetCounter`. None of them reads the database.

Tests: (`internal/obs`) `SetCounter` never decreases a counter and survives concurrent calls under `-race`;
(`internal/serve`) the collector alone, run with the T0 counting driver attached, issues zero store queries; after
two collector runs the five names are present with the right `# TYPE`, `go_goroutines >= 1`,
`heap_alloc > 0`, the counters are monotone after allocating some memory between runs; `wal_bytes` equals the size of
a WAL file the test creates and is 0 when absent; (`internal/exporter`) the rendered text passes a minimal exposition
grammar check added to `exporter_test.go` (the module has no Prometheus text parser). The assertion "a scrape issues no
store query" is NOT made for the whole scrape: the scrape path still calls `UpdateGauges`, whose whole-table reads
stay until phase 3.

Done when: tests pass (with `-race` for `SetCounter`) and the registration check passes.

## T4c. Dashboard panels, README, spec, runbook

Files: `grafana/pg-claude-code-recorder.json`, `README.md`, the spec, `grafana/pg_claude_code_recorder_metrics.txt`
comments. `grafana/check-signals-registration.sh` (R4) must still pass, and `check-alert-structure.sh` too.

Dashboard: keep the eight existing titles unchanged and unique (the check compares titles case-insensitively), keep
the existing layout (12 x 8 timeseries tiles in two columns, panel ids 9 and up, refresh 1m), use `$__rate_interval`
and `max(...)` for gauges as the existing panels do, write every expression with the prefixed name, and end each new
panel description with "no alert covers this panel yet" unless T5 covers it. Add, appended below the existing
panels, titles unique and distinct from the reserved eight:

1. `settle tick duration`: unit `s`; p50 and p99 of `settle_tick_duration_seconds_bucket` over `$__rate_interval` of
   at least 5m (a tick runs once a minute, so a 1m window holds about one sample), plus
   `max(settle_tick_last_duration_seconds)`; a visual threshold line at 60.
2. `settle tick work`: unit `short`; `rate(settle_tick_examined_total)` and `rate(settle_tick_transitions_total)`.
3. `loop runs by outcome`: unit `ops`; `sum by (loop, outcome) (rate(loop_runs_total[$__rate_interval]))`.
4. `recorder cpu cores`: unit `short`; `rate(process_cpu_seconds_total[$__rate_interval])` and, as a second series,
   `rate(go_gc_pause_seconds_total[$__rate_interval])`.
5. `go heap and goroutines`: `go_memstats_heap_alloc_bytes` (bytes) and `go_goroutines`; split into two panels if
   one unit per panel is required.
6. `refresh gauges duration`: unit `s`, p99 of `refresh_gauges_duration_seconds_bucket`.
7. `wal size`: unit `bytes`, `max(wal_bytes)`.

The WAL frame and checkpoint panels are added by T3 (it owns those metrics). The existing `conversations by stage`
panel keeps its title; its description gains "as of the last settle tick, up to a minute old".

README: update the Signals bullets (the series list, and the stale `session_id` mention to `session` where it
refers to log fields), the "Signals, alerts and the dashboard" files table row for the dashboard (the panel list),
and add the "recorder CPU is high: what to look at" runbook entry: open `recorder cpu cores`; open
`settle tick duration` and `loop runs by outcome`; `rg settle_tick` in the recorder log; take a 30 s profile with
`scripts/profile-cpu.sh` (pprof is on in the deployed configuration); compare with `wal size`. Do not rewrite the
README section whose needles `check-signals-registration.sh` R5 requires (`remove.enable`, `daemon-down`,
`free-space`, `no undo other than`, `remove --dry-run`, `doctor`, `backed up`). Add a known-limitation sentence:
`status` `stage_counts` omits live and quiet while the dashboard and metrics include them (phase 1), and
`status --watch` runs an O(files) loop on the daemon every 2 s, so do not leave it open while profiling.

Spec: in `### Signals` (spec line 1381) add the new metric names, the `settle_tick` log event and the new alert row,
and add the requirement "every periodic loop MUST report duration and run outcome" (phase 1 completes the list of
loops). State that settle cadence (a hard-coded one minute) is not specified in the spec and is decided in phase 2.

Done when: the registration and alert-structure checks pass and the dashboard JSON validates (`jq` parses it;
`promtool` is run by the check).

## T5. One alert: `settle-tick-slow`

Why in phase 0: the 1.2 to 1.3 core burn was found only by manual profiling, so the next regression would be silent.
A structural threshold needs no baseline: the tick interval is 60 s, so a tick longer than 60 s means ticks overrun.
CPU and WAL alerts wait for the T6 baseline (phase 1).

Rule: uid `pg-ccrec-settle-tick-slow`, `for: 15m`, severity `warning`, `noDataState: OK`, query A
`max(pg_claude_code_recorder_settle_tick_last_duration_seconds)`, reduce last, threshold greater than 60. Summary
"pg-claude-code-recorder settle tick has taken over 60 seconds for 15 minutes"; description
"settle_tick_last_duration_seconds is above 60 s, the tick interval, for 15 minutes: the settle pass is overrunning
and burning CPU. Run pg-claude-code-recorder status, then read the recorder log for the settle_tick line, and take a
30 s CPU profile from the pprof socket." (The existing rules require the literal `pg-claude-code-recorder ` in the
description and a first command; copy the shape of the `copy-lag` rule.)

Registration points: `grafana/alerting/alerts.yaml`, `grafana/alerting/alerts-delete.yaml`, `EXPECTED_UIDS` and the
rule-count text in `grafana/check-alert-structure.sh`, a promtool unit test in `grafana/alerting/rule-tests/` (fires at
130 s sustained, does not fire at 30 s, does not fire on a short breach), the spec alert table, and the list of rule
uids the probe package keeps (`rg -n 'pg-ccrec-copy-lag' /Volumes/gitrepos/phillipg/nix-agent-support` finds every list
in this repo, for example `packages/pg-router-probe/cmd/pg-router-probe/run.go`). A downstream deployment repo may keep
its own list of alert uids and fail an evaluation check on an unclassified new uid; this task cannot edit other
repos, so its report names that follow-up for the operator.

Done when: the alert-structure check and the rule test pass.

## T6. Baseline capture (gated follow-up, human-visible)

Blocked by the apply of T4a, T4b, T4c and T5 (gate keyed on the patch-ids of their landed commits, per the
`pb:pb-gate-lifecycle` skill) and by one hour of the new metrics running. The agent records, as a bead comment, the
live values of: `rate(pg_claude_code_recorder_process_cpu_seconds_total[5m])` averaged over the hour,
`histogram_quantile(0.5|0.99, ...settle_tick_duration_seconds_bucket)` over the hour,
`pg_claude_code_recorder_settle_tick_last_duration_seconds`, `pg_claude_code_recorder_wal_bytes`,
`increase(...query_duration_seconds_bucket{le="+Inf"}[1h]) - increase(...{le="10"}[1h])`, and the
`conversations_by_stage` gauge. Without a Prometheus, it scrapes the loopback metrics endpoint twice, five minutes
apart, and computes the same rates. It also saves one 30 s CPU profile (`scripts/profile-cpu.sh`) as the attribution
baseline and records its path. Closing T6 unblocks T1, T2, T3.

## T1. `changedSinceSettle` reads only its own conversation's generations

Files: `internal/settle/settle.go` (`changedSinceSettle`, currently `settle.go:556`; also reached on the removal path
through `remove.go:415`, under the copier lock).

Change: for each file of the conversation, read that file's current generation with the existing per-file query
`Store.Generation(file, gen)` (`store/query.go:149`, primary key `(file_id, gen)`), and compare `CopyOffset` with the
manifest size as before. `Generation` returns `store.ErrNotFound` for a missing row: map `errors.Is(err,
store.ErrNotFound)` to offset 0 (what the old map lookup did); any other store error returns `false`, as the old
`AllGenerations` error did. The old code returned `false` on a store error before comparing anything, whereas
per-file reads may return `true` earlier on a mismatch; that ordering difference is accepted and is a test case.
Per-file reads give no whole-table snapshot; nothing needs one (each file's row is compared independently and
`c.files` came from an earlier read), so the loss of cross-file atomicity is harmless.

Behaviour that MUST NOT change (each a test case, compared against a test-local copy of the old function,
`oldChangedSinceSettle`, which uses `AllGenerations`, as the oracle):

1. A file the manifest does not list: changed.
2. A snapshot-mode file with a different `CurGen` or `LastSize` than the manifest: changed; equal: unchanged. No
   `Generation` read is made for it.
3. An append-mode file with a different `CurGen`, or a current-generation copy offset different from the manifest
   size: changed; equal: unchanged.
4. A current generation with no row: compared as offset 0.
5. A manifest that fails to unmarshal: changed. No settled row: unchanged. A store error (induced by closing the
   store before the call): unchanged.

Tests (package `internal/settle`, real SQLite store, T0 counting driver):

- Equivalence: the five cases, asserted equal to the oracle on every fixture.
- Work bound: run the same conversation with 0 and with 2,000 unrelated generation rows; assert `RowsRead()` for the
  call is identical in both runs and `Queries()` is at most `len(appendFiles) + 1` (the `+1` is the `SettledOf`
  read). The old code fails this: its one `AllGenerations` query returns a number of rows that grows with the table,
  so a query counter alone would not catch it (this is why rows are counted too).
- Source guard: a Go-source scan (the style of `cmd/pg-claude-code-recorder/unit_tier_guard_test.go`) that fails if
  `changedSinceSettle` or any function in `remove.go` calls `AllGenerations`. `AllGenerations` stays in the store for
  its other callers.
- The removal path (`remove.go:415`) stays covered by the existing `remove_test.go`; run it.

Negative controls the implementer MUST try: put `AllGenerations()` back into `changedSinceSettle` (work-bound and
source guard fail); drop the `ErrNotFound` mapping (case 4 fails).

Expected effect (from the profiles): removes the per-conversation 44.5% to 60.1% share and most of the 85.9%
allocation. The remaining cost is the list in "What T1 does and does not remove".

## T2. `transcriptFacts` finds a session's newest `cwd` through `records_session`

File: `internal/lifecycle/engine.go`, `transcriptFacts` (currently `engine.go:337-345`). The current SQL:

```sql
SELECT f.s FROM records r JOIN fields f ON f.rec_id=r.id AND f.name='cwd'
WHERE r.session=? AND r.source='claude-transcript' ORDER BY r.time_ns DESC, r.id DESC LIMIT 1
```

SQLite picks `fields_name_n (name=?)` as the outer loop (a scan of every `cwd` field) because the store has no
`sqlite_stat1` and cannot see that `session=?` is far more selective. The new query keeps the same predicates and
`ORDER BY ... LIMIT 1` and makes `records` the outer loop with `CROSS JOIN` (only `CROSS JOIN`: `INDEXED BY` can fail
at prepare time with "no query solution"), so the plan is `records_session` for the session, then `fields_rec
(rec_id, name)` per record. No index is added and `ANALYZE` is not run.

Extract the SQL as a package-level `const cwdQuery` that `transcriptFacts` uses, so the plan test checks the
production query and not a copy.

Honest cost: the new plan seeks `fields_rec` once for each transcript record of the session and sorts, so it is
O(records of that session). The two sibling queries in the same function (`MAX(time_ns)` at `engine.go:346`,
`DISTINCT file_id ... LIMIT 50` at `engine.go:366`) already cost that. That is a large improvement over scanning every
`cwd` in the store, and a session with about 10^5 records still costs about 10^5 index seeks. A composite
`records(session, source, time_ns)` index is the phase 6 follow-up. T2 does not touch `Engine.load()`
(`engine.go:94-110`, which scans every `session-registry` record on each `Show` and `List`), the per-conversation
`transcriptFacts` call in `List` (`engine.go:437`), or `countEndAsync` (it calls `Show`, so it contains
`transcriptFacts`; the profile shares for the two overlap and are not added).

Tests (package `internal/lifecycle`, real store built with `store.Open` and `Commit`; `stages_test.go` is pure today,
so this adds the package's first store-backed fixture):

- Equivalence against the old SQL kept in the test as the oracle, on a fixture with: (a) the newest record has no
  `cwd`; (b) two records tie on `time_ns` (id breaks the tie); (c) another session has a newer `cwd`; (d) the newest
  record's `cwd` is empty or NULL (the old query returns that row and the Go code then ignores an empty value, so the
  new query must return the same row); (e) a non-`claude-transcript` record has a newer `cwd` (source filter);
  (f) a session with no transcript records (both return no rows); (g) several files in one session; (h) several
  `cwd` fields on one record (compare with the same tie-break).
- Plan guard: run `EXPLAIN QUERY PLAN` on the OLD SQL in the test and assert its detail text mentions
  `fields_name_n`; if not, fail with "planner changed; the guard proves nothing". Then run it on `cwdQuery` and assert,
  with tolerant regular expressions over the `detail` column (the text format differs between SQLite versions, and the
  plan prints table aliases, not names), that one row matches `SEARCH.*USING INDEX records_session`, that no row
  matches `SCAN`, and that `fields_name_n` does not appear; a `USE TEMP B-TREE FOR ORDER BY` row is allowed. Log
  `sqlite_version()` so a driver bump that changes the text is diagnosable. The fixture needs no particular size
  (without `sqlite_stat1` the plan does not depend on table size).

Negative controls the implementer MUST try: remove the `CROSS JOIN` (plan guard fails); change the `ORDER BY` to
`r.id ASC` (the tie and newest cases fail).

Client-visible effect: `conversation.show` returns successfully inside the 30 s client timeout instead of timing out.
No wire field, operation, exit code or output format changes.

## T3. A passive WAL checkpoint loop, a WAL size limit, WAL metrics

Premise, stated plainly: SQLite already runs a passive autocheckpoint every 1,000 WAL pages on the writer's commit
(nothing here sets `wal_autocheckpoint`), and a passive checkpoint cannot reset a WAL that a reader still pins. The
likely reason the WAL stays at 7.4 GB is the long reads (T2 removes the worst of them). So T3's value is
measurement first: it shows whether checkpoints complete, how far behind they are, and how long they take. It does
not promise that `wal_bytes` stops growing; T7 checks that after T2.

Files: `internal/store/store.go` (`openDB`, `Close`, a new `Checkpoint` method), `internal/serve/daemon.go` (a new loop
in the `refreshing` wait group), `internal/obs/obs.go`, the catalogue, `grafana/pg-claude-code-recorder.json` (two
panels).

Change:

1. `Store.Checkpoint() (log, checkpointed int, err error)` runs `PRAGMA wal_checkpoint(PASSIVE)` and reads its three
   result columns (use `QueryRow(...).Scan`, not `Exec`). For PASSIVE the first column (busy) is 0 in practice, so
   the outcome is derived from the frame counts: `checkpointed == log` is complete, `checkpointed < log` is partial.
   Both counts are cumulative until the WAL restarts, not per-run deltas. A "database is locked" error is possible
   under checkpoint-lock contention and is recorded as `outcome=error`, not logged at WARN more than once a minute.
2. It runs on its OWN connection (a third `*sql.DB`, `MaxOpenConns(1)`, same DSN), not on the write handle: the
   write handle has `SetMaxOpenConns(1)`, so a checkpoint on it would hold the only write connection for the whole
   call (a 7.4 GB backlog could take minutes with `synchronous=FULL`) and queue every commit. PASSIVE does not take
   the write lock, so a separate connection is safe alongside a writer.
3. `Close` closes that connection before its own `wal_checkpoint(TRUNCATE)`. `Checkpoint` takes `s.mu` only to read
   the pointer and returns an error (not a panic) when the store is closed. The loop is part of `d.refreshing` and
   watches `d.stop` (as `settleLoop` does), so `Daemon.Close` waits for it before `st.Close()`; shutdown may wait for
   a running checkpoint (not cancellable) and that is accepted.
4. The loop runs every minute (a constant in phase 0). Its duration goes into histogram
   `wal_checkpoint_duration_seconds` (the same `LoopBuckets`) and its outcome into
   `loop_runs_total{loop="wal_checkpoint",outcome}`.
5. `PRAGMA journal_size_limit=1073741824` goes in the DSN of the write handle and the checkpoint connection
   (`_pragma=journal_size_limit(1073741824)`, as the existing pragmas are), never a one-off `Exec` (a pooled connection
   that is re-opened would lose it). It applies when the WAL restarts, which readers also block.
6. Gauges: `wal_frames` (frames in the WAL at the last passive checkpoint, in pages, not bytes),
   `wal_checkpointed_frames`, `wal_checkpoint_last_timestamp_seconds`. (`wal_bytes` shipped in T4b.)
7. Dashboard: two panels, `wal checkpoint frames` (`max(wal_frames)` and `max(wal_checkpointed_frames)`, unit
   `short`) and `wal checkpoint duration` (p99 of `wal_checkpoint_duration_seconds_bucket`, unit `s`).

Not in this task: `TRUNCATE` or `RESTART` checkpoints (they wait for readers and can stall writers; phase 6), a
bounded read pool, reclaiming the existing 7.4 GB file (see the restart section).

Tests (package `internal/store`, real SQLite file in a temp dir):

- Reader pin: write a few rows with `Commit` (keep writes tiny, or open the test store with `wal_autocheckpoint=0`,
  so the autocheckpoint never fires and perturbs the counts); pin a snapshot with `rdb.Begin()` plus a read; commit
  more rows; call `Checkpoint` and assert `checkpointed > 0` and `checkpointed < log`; roll the reader back, call
  again and assert `checkpointed == log`.
- `journal_size_limit` reads back 1073741824 through both connections, including after `SetMaxIdleConns(0)` forces a
  new connection (proves the DSN placement). The truncation behaviour itself is not tested in the unit tier.
- `Checkpoint` and `Close` run concurrently under `-race` without a data race or nil dereference; `Checkpoint` after
  `Close` returns an error.
- Loop (package `serve`): the loop function takes an injected tick channel and an injected `step`; the test sends a
  tick, waits for `step` to signal, closes the stop channel and requires the wait group to finish before a deadline,
  with no `sleep`; no `step` runs after stop. Starting a whole daemon is integration tier and is not done here.
- The registration check passes with the new names.

Negative controls: run `wal_checkpoint(TRUNCATE)` instead of PASSIVE (the reader-pin test changes); set the pragma by
`Exec` instead of the DSN (the reconnect assertion fails); drop `s.mu` from `Checkpoint` (`-race` fails).

## T7. Post-deploy verification (gated follow-up)

Blocked by the apply of T1, T2 and T3 (gate keyed on their patch-ids). Run these against the live daemon and record
the numbers on the bead (Grafana Explore on the Prometheus data source, or the loopback endpoint). Targets are
hypotheses to be set from the T6 baseline; the evidence-derived anchors are in brackets.

1. CPU: `avg_over_time(rate(pg_claude_code_recorder_process_cpu_seconds_total[5m])[1h:1m])` is below the T6 baseline
   by at least the settle share the profile attributed to `Tick` [baseline 1.2 to 1.3 cores in profiles; a planning
   target of 0.5 cores is revised after T6].
2. Tick: `histogram_quantile(0.99, sum by (le) (rate(pg_claude_code_recorder_settle_tick_duration_seconds_bucket[30m])))`
   is lower than the T6 value by at least half, and `settle_tick_last_duration_seconds` is below 60.
3. Requests: observations above 10 s stop accumulating:
   `increase(pg_claude_code_recorder_query_duration_seconds_bucket{le="+Inf"}[1h]) - increase(pg_claude_code_recorder_query_duration_seconds_bucket{le="10"}[1h])`
   [520 of 591 before]. This histogram has no `op` label, so it covers all operations.
4. WAL: `wal_checkpointed_frames` approaches `wal_frames` between bursts, and `wal_bytes` stops growing. The existing
   file does not shrink until the WAL resets and the file is truncated; if it is still large after a day, see the
   restart section.
5. Attribution: take one 30 s CPU profile (`scripts/profile-cpu.sh`) and compare the share under `Settler.Tick` and
   `AllGenerations` with the T6 profile. The profile is the acceptance for attribution; the metrics are the trend.
6. A post-fix allocation profile (`/debug/pprof/allocs`, `-sample_index=alloc_space`) no longer shows
   `AllGenerations` dominating, compared with the pre-fix 85.9%.

## Apply and restart (operator warning, goes into the plan's bead text and the README runbook)

`pn workspace apply` replaces the daemon, and the old daemon's `Close` runs `PRAGMA wal_checkpoint(TRUNCATE)`
(`store.go:395-397`) with no timeout or progress log. With a 7.4 GB WAL that can take minutes; during it the daemon
serves no requests, so `conversation.show` clients see a stall or the daemon-offline error (not `not_found`), the
`daemon-down` alert can page after 2 minutes (expect one page or silence it), and the disk is low, so watch free space.
Do not kill the daemon: a SIGKILL leaves the WAL to be replayed on the next open. The checkpoint is done when
`status` answers again and `wal_bytes` drops; T4a adds a `wal_truncate_done` event with `duration_ms` to `Close` so the
log says so. The first apply after T4a pays this once; a rollback (revert the commit and apply again) pays it again.
Launchd settings (KeepAlive, ExitTimeOut) live outside this package and were not checked.

## Rollback

T0 to T4c, T5 and T1 to T3 are code, dashboard and alert changes with no schema or data migration; revert the commit
and apply. The only persistent behaviour changes are the new passive checkpoint loop and the connection-level
`journal_size_limit`; neither writes anything that needs undoing on disk.

## Risks

- The first passive checkpoint has a large backlog and does I/O on the database file; it blocks neither readers nor
  writers (it uses its own connection) but it competes for disk bandwidth.
- T2 changes a query plan by hand. If a later `ANALYZE` makes the planner choose differently, the forced order still
  holds; the plan-guard test catches an accidental loss of the forcing.
- T1 assumes `Generation(file, gen)` returns the row `AllGenerations` would; the equivalence cases cover not-found
  and the error ordering, which are the places the two differ.
- New metrics change the exposition; any dashboard or alert elsewhere that selects on label sets must still match (a
  search of this repo found none outside this package).
- A downstream deployment repo may list alert uids and fail on the new one (T5).

## Later phases (named so phase 0 does not drift into them)

1. Observability completeness: every loop's duration and outcome (watcher sweep, copier ingest, parser, tracker poll,
   doctor, removal pass), per-sweep ingested versus skipped counts, lock waits, an `op` label on the query histogram
   (or labelled histograms), HELP lines, CPU and WAL alerts, `status` showing live and quiet counts and last tick
   duration, the unbounded `clone_private_size_bytes{file}` label.
2. Event-driven settle: a dirty set fed by copier commits and a due-time schedule from each conversation's quiet
   deadline; stage counts maintained incrementally; `refreshRemovalGauges` off the per-tick path; settle cadence
   decided in the spec.
3. Gauge refresh: lag maintained from commits, scrape-time refresh removed or cached.
4. Incremental sweep: compare size, mtime and inode with `files.last_size`, skip unchanged files, derive
   `SweepMissing` from the walk, honor `sweep.interval`.
5. Tracker and request paths: tail `requests.jsonl` from an offset, an index for `feed_marks.at_ns`, cancel a query
   when its client times out.
6. SQLite hygiene and doctor: `ANALYZE` decision, a `records(session, source, time_ns)` index, read-pool bound,
   `TRUNCATE` checkpoint policy, commits pruning, a rolling byte-budgeted doctor that does not hold the copier lock
   (spec amendment).
