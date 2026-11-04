# Plan: pg-claude-code-recorder phase 1, observability completeness

Repo root: `/Volumes/gitrepos/phillipg/nix-agent-support` (public repo: no employer-specific detail in code, tests,
comments or docs). Package: `packages/pg-claude-code-recorder`. Design of record:
`docs/superpowers/specs/2026-10-30-pg-claude-code-recorder-design.md` (the spec). Phase 0 plan:
`docs/superpowers/plans/2026-11-03-recorder-phase0-cpu-and-observability.md`. Sibling plans: phases 2 to 6, same
directory, same date prefix.

All task paths are relative to `packages/pg-claude-code-recorder` unless they start with `docs/` or `grafana/`.

## Why this phase first (measured evidence)

Phase 0 was verified on 2026-11-04 on build `0.0.0-8ab32e31` (loopback metrics, 500 s window after backfill):

- CPU fell from 1.2 to 1.3 cores to 0.63 cores (315.37 s over 500 s). A 30 s profile attributed 12.73 s of samples
  (about 0.42 cores); `Settler.Tick` is 39.0% of those samples (about 0.17 cores). The remaining 0.2 cores of the
  profile and the 0.2 cores outside it are NOT attributed to any loop by any metric: no metric says how long the
  watcher sweep, the copier, the parser, the tracker poll, the status loop or the doctor take.
- 76.9% of flat time is in syscalls (`rawsyscalln`). Nothing counts syscalls per loop, so the source (file stats,
  SQLite reads, fsync) is a guess.
- 109 requests exceeded 10 s in 66 minutes (11 in the last 500 s). The query histogram has no `op` label, so which
  operation is slow cannot be read from metrics. `query_timeout` events exist only for requests that hit 30 s.
- The slowest remaining measured work after the settle tick is `parser.UpdateGauges` (87.5% of `AllGenerations`
  allocation, 21.74 GB of 234.61 GB), run every 5 s by the refresh loop. Nothing times it separately from the rest
  of `refreshGauges`.
- Prometheus has no recorder scrape job on this machine yet (a separate deployment defect, tracked outside this
  plan), so every figure came from the loopback endpoint. Alert thresholds in this phase therefore come from the
  phase 0 loopback figures above plus a new baseline captured in T9 once a scrape exists.

This phase is first because phases 2 to 6 each need a number to prove themselves against, and four of them
(slow-request cause, sweep cost, tracker cost, doctor cost) have no number today.

Re-ranking of the six phases by measured cost (the order the follow-up beads SHOULD be worked in; the phase numbers
stay as named by phase 0):

| Rank | Phase                                | Measured basis                                                                                                                                          |
| ---- | ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1    | 1, observability completeness        | unattributed 0.4 of 0.63 cores; no op label for 109 slow requests                                                                                       |
| 2    | 3, gauge refresh                     | `UpdateGauges` and `Settler.refresh` are the two remaining `AllGenerations` callers (9.3% of allocation) and the status loop stats every file every 5 s |
| 3    | 2, event-driven settle               | `Settler.Tick` still 39% of samples, p99 about 59 s (target was below 60 s), last tick 37 s                                                             |
| 4    | 5, tracker, request and cancel paths | 109 slow requests; the cancel part is independent of cause and cheap                                                                                    |
| 5    | 4, incremental sweep                 | sweep runs every 15 min, so its steady CPU share is small; its cost is a spike, visible only after T3 and T4 below                                      |
| 6    | 6, SQLite hygiene and doctor         | WAL is 88 MB and flat after phase 0; the doctor runs once a day; both are bounded today                                                                 |

## Scope

In scope: duration and outcome for every periodic loop, per-pass work counters, lock waits, an `op` label on the query
histogram, HELP lines, the unbounded `clone_private_size_bytes{file}` label, CPU and WAL alerts, the `status` output,
dashboard panels, and the baseline the alerts use.

Out of scope, deliberately: changing what any loop does (phases 2 to 6 change behaviour; this phase only measures).
No schema change, no wire operation change beyond additive `status` fields, no new Go dependency.

## Ground rules

Inherited from phase 0 unchanged: real SQLite and real files in tests, read-count assertions never wall-clock
assertions, no slow test in the unit gate (V-3, V-4), every metric is a `const M...` in `internal/obs/obs.go` plus a
line in `grafana/pg_claude_code_recorder_metrics.txt` (else `check-signals-registration.sh` fails), labels are
bounded and enumerated in the catalogue header, every exported name carries the prefix `pg_claude_code_recorder_`,
no bead id in the repo, injected clock in tests (never sleep). Run Go tests as `nice -n 10 go test -p 4
./internal/<pkg>/...`; Nix checks through `pg-nix-log-wrapped` with an explicit timeout or `bgrun`.

Additional rules for this phase:

- Instrumentation MUST be cheap: a loop wrapper adds one clock read pair, one histogram observation and one counter
  add per pass. It MUST NOT add a store query. A test with the phase 0 counting driver asserts zero added queries.
- A per-event path (the copier's `Ingest`, the server's request path) MUST NOT take a registry lock more than twice
  per call; the registry is one mutex, so measure it (T2 test).

## Task list and order

- T1 (M): labelled histograms in the registry and exporter, HELP lines.
- T2 (M): a loop wrapper and the remaining loops (watcher sweep, copier ingest, parser drain, tracker tick and poll,
  status refresh, doctor, removal pass).
- T3 (S): per-pass work counters (ingested versus skipped, bytes copied) and lock-wait metrics.
- T4 (S): the `op` label on the query and feed histograms.
- T5 (S): bound `clone_private_size_bytes`.
- T6 (S): `status` shows live and quiet counts and the last tick duration.
- T7 (M): dashboard panels, README runbook, spec table.
- T8 (S): CPU and WAL alerts (needs T9 first for the threshold).
- T9 (S, gated follow-up): baseline capture after T1 to T7 are applied and a scrape exists.

Dependencies: T1 first (T2, T3 and T4 use labelled histograms). T2 before T3 (T3 reuses the wrapper). T4 independent
after T1. T5 and T6 independent. T7 after T2, T3, T4, T6. T9 is gated on the apply of T1 to T7 (`pb:pb-gate-lifecycle`,
keyed on the landed commits' patch-ids) and one hour of data. T8 after T9.

File overlap (the cause of the ordering): `internal/obs/obs.go` and the catalogue are edited by T1 to T5 (land them one
at a time, in numeric order); `internal/serve/daemon.go` by T2 and T6; `internal/server/server.go` by T4;
`grafana/*.json` by T7; `grafana/alerting/*` by T8.

## T1. Labelled histograms and HELP lines

Files: `internal/obs/obs.go` (`Registry`, `Histogram`, `RegisterHist`, `Observe`), `internal/exporter/exporter.go`
(`Render`), the catalogue.

Problem: `Observe(name, v)` takes no labels, so one histogram per loop would need one constant per loop, and the
query histogram cannot carry `op`.

Change: add `ObserveL(name, labels, v)` and `RegisterHistL(name, bounds, labelSets)`; the histogram map is keyed by
`key(name, labels)` exactly as counters are. `Observe(name, v)` stays and means `ObserveL(name, nil, v)`. `Snapshot`
returns the keyed map. `Render` splits the key with the existing `splitKey` and writes `le` after the other labels.
HELP: add `Registry.Help(name, text)` and have `Render` write `# HELP <prefix><name> <text>` before `# TYPE`; every
metric registered in `RegisterStandard` gets a one-line help string from a table next to the constants (a metric
without help fails the test below).

Tests: (`internal/obs`) two label sets of one histogram count independently; `Observe` still works; an unregistered
`ObserveL` uses `LagBuckets` as today; concurrent `ObserveL` under `-race`. (`internal/exporter`) rendered output for
a labelled histogram has `_bucket{op="x",le="1"}`, `_sum{op="x"}` and `_count{op="x"}` and passes the minimal
exposition grammar check phase 0 added; every name in `RegisterStandard` has a HELP line (iterate the snapshot).
Hand-applied negative controls: put `le` before the other labels; drop `_sum` labels.

Done when: tests pass (`-race` for obs), registration check passes. The unlabelled series names are unchanged, so
existing dashboards and alerts keep working (golden render test on the phase 0 histograms).

## T2. One loop wrapper, every loop

Files: `internal/serve/daemon.go`, a new `internal/serve/loops.go`, the catalogue, `internal/obs/obs.go`.

Change: replace the two hand-written timing blocks (`runTick`, `refreshGauges` defer, `checkpointOnce`) with one
`runLoop(name string, step func() error)` that records `loop_duration_seconds{loop}` (labelled histogram, buckets
`LoopBuckets`), `loop_runs_total{loop,outcome}` and `loop_last_timestamp_seconds{loop}`. Keep the existing
`settle_tick_*` names as they are (dashboards use them) and have `runTick` call the wrapper for the shared parts.
Loops, with the exact `loop` label values added to the enumerated catalogue line:

| loop value       | where it runs today                 | what one pass is                                                                 |
| ---------------- | ----------------------------------- | -------------------------------------------------------------------------------- |
| `watcher_sweep`  | `watcher.sweep` through `d.onSweep` | one full directory walk plus `SweepMissing` plus the per-file ingest it triggers |
| `copier_ingest`  | `Daemon.ingest` (per path)          | one ingest call (a per-event path: counter plus histogram only, no log line)     |
| `parser_drain`   | `parseLoop` `Drain`                 | one drain of the parser's queue                                                  |
| `tracker_tick`   | `trackLoop` `Tracker.Tick`          | liveness refresh                                                                 |
| `tracker_poll`   | `trackLoop` `Tracker.Poll`          | `ends/`, `requests.jsonl`, FIFO sweep                                            |
| `status_refresh` | `statusLoop` `refreshStatus`        | the O(files) status snapshot                                                     |
| `doctor`         | `doctorLoop`                        | one doctor run                                                                   |
| `removal_pass`   | `Settler.removeLocked` from `Tick`  | one removal pass                                                                 |

The wrapper takes the clock from the daemon (`d.clock`), not `time.Now`, so tests inject it (today `refreshGauges`
uses `time.Now`; fix that here).

Every loop also gets a stall signal: `loop_last_timestamp_seconds{loop}` is set at the END of a pass, and the T8
alert uses `time() - max(...)` against a per-loop bound. A pass that never ends is visible through a new gauge
`loop_in_flight_seconds{loop}`, the age of the oldest pass that has started and not finished (0 when idle), set by
the wrapper when a pass starts and cleared when it ends, and refreshed by the refresh loop.

Tests: (`internal/serve`) for each loop, a stub step records exactly one run with the right outcome; an erroring
step records `outcome=error` and the existing WARN lines are unchanged; `loop_in_flight_seconds` is non-zero while a
blocked step runs (a channel-gated stub, injected clock advanced) and 0 afterwards; each real loop is exercised once
through its production entry point with a `fakeclock` (no sleeping) and the run counter for its `loop` value
increases by exactly one. The T0 counting driver asserts the wrapper adds no store query.

Done when: tests pass, `go vet` clean, registration check passes.

## T3. Work counters and lock waits

Files: `internal/copier/copier.go` (`Ingest`, `SweepMissing`), `internal/watcher/watcher.go` (`sweep`),
`internal/serve/daemon.go`, `internal/obs/obs.go`, the catalogue.

Change: counters, all bounded:

- `ingest_results_total{result}` with `result` in `copied new_generation unchanged skipped missing tombstone raced
error` (the `Result.Action` values; read `internal/copier/copier.go` for the exact set and enumerate it in the
  catalogue). This gives the per-sweep "ingested versus skipped" split as a rate.
- `sweep_files_total{outcome}` with `outcome` in `walked unchanged_candidate`: `walked` counts files the directory
  walk saw; `unchanged_candidate` counts files whose size, mtime and inode match the stored `files.last_size` row. In
  this phase the second counter is observation only (nothing is skipped; phase 4 uses the same rule to skip), so the
  saving phase 4 can make is known before it is built.
- `copier_lock_wait_seconds` (histogram, `LoopBuckets`) observed around acquiring `Copier.mu` in `Ingest`,
  `IngestBatch`, `SweepMissing` and `WithLock` (doctor), and `store_write_lock_wait_seconds` around the store's
  single-writer acquisition. Pool waits already exist (`db_wait_seconds_total`); this adds the lock waits that the
  pool metrics cannot see.
- `sweep_duration_seconds` is T2's `loop_duration_seconds{loop="watcher_sweep"}`; do not add a duplicate.

Tests: a real store plus a temp directory of 3 appended files: first sweep reports `new_generation` 3; a second
sweep with no change reports `unchanged` 3 and `unchanged_candidate` 3; appending to one reports `copied` 1 and
`unchanged_candidate` 2. Lock wait: hold `Copier.mu` in a goroutine, call `Ingest`, assert the histogram sample
count is 1 and the sum is positive using the injected clock (no wall-clock bound). Negative controls: count
`unchanged` as `skipped`; observe the lock wait after acquiring instead of before.

Done when: tests pass and registration passes.

## T4. `op` label on the request histograms

Files: `internal/server/server.go` (`observe`, `finish`, `fail`), `internal/obs/obs.go`, the catalogue.

Change: `query_duration_seconds` and `feed_latency_seconds` become labelled histograms with `op`, the registered
operation name or `unknown` (`opLabel` already bounds it; there are 16 operations). The unlabelled series name is
kept as the aggregate by also observing with no labels, so existing dashboards and any alert on
`query_duration_seconds_bucket` keep working (double observe; cost is one more lock round trip per request, which
T2's per-event rule allows). Add `requests_inflight{op}` gauge (incremented on entry, decremented on exit, including
panic and timeout paths) so a stuck request is visible before it finishes. Log a WARN `slow_request` with `op`,
`client`, `ms` for any request over 10 s (the phase 0 verification threshold), capped at one line per op per
minute.

Tests: two operations with a stub handler each; each op label gets exactly its own samples; the aggregate equals the
sum; a panicking handler decrements `requests_inflight`; a handler over 10 s on an injected clock logs one
`slow_request` and a second within a minute logs none. Negative control: skip the decrement on the error path.

Done when: tests pass and the existing wire golden tests are unchanged.

## T5. Bound `clone_private_size_bytes`

Files: `internal/copier/copier.go` (`copier.go:1042`, the only setter), `internal/obs/obs.go`, the catalogue, any
dashboard expression that uses the series (`rg -n clone_private_size_bytes grafana README.md`).

Change: the series has a per-file `file` label, which the catalogue header forbids. Replace it with two unlabelled
gauges, `clone_private_size_bytes_max` and `clone_private_size_bytes_sum`, and a counter `clone_private_files_total`
(the count of private clones created). If an operator needs the per-file breakdown it is in the log: emit an event
`clone_private` with `file` and `bytes` (already emitted? check; if not, add it). The old series stops being written;
a release note in the README says dashboards using it must move to the new names. Remove the stale series from the
registry (it is not registered at zero, so it simply disappears).

Tests: ingest three files with private clones of different sizes; the max equals the largest, the sum equals the
total; no series has a `file` label (render the registry and assert no `file=` anywhere). Negative control: restore
the label.

Done when: tests pass, the registration check passes, and `rg 'file=' ` over the rendered metrics in the test is
empty.

## T6. `status` shows live and quiet counts and the last tick duration

Files: `internal/serve/statuscache.go`, `internal/serve/ops.go` (`opStatus`), `internal/wire/wire.go`, the CLI
renderer under `cmd`, README.

Problem: `status` prints `stage_counts` from persisted stage rows, which exist only for held, settled and later
stages; live and quiet are derived, so the CLI omits them while the metrics include them.

Change (additive wire fields only; the wire golden tests gain new fields but no field is renamed or removed):
`stage_counts` gains `live` and `quiet` from the same derivation the settler uses (`Settler.refresh` already
computes `counts`; have the settler publish the last `counts` map and the time of the tick, and have the status
snapshot read that instead of recomputing). Add `last_tick` with `duration_ms`, `examined`, `age_s` (since the tick
ended), and `loops`: for each loop in T2, `last_duration_ms`, `last_outcome`, `age_s`, `in_flight_s`. A daemon that
has not completed a tick reports `last_tick` as absent, not zero. The CLI prints one line per loop under a `loops`
heading, and `--json` carries the same data.

Tests: a daemon fixture with 2 live, 1 quiet, 1 held and 1 settled conversation: `status --json` has all five
counts; the stage counts equal the `conversations_by_stage` gauges after a tick (the two MUST agree, test both from
one tick); before the first tick `last_tick` is absent; the CLI text golden covers the new lines. The wire golden is
regenerated and the diff is reviewed as additive only.

Done when: tests pass. README known-limitation sentence about `stage_counts` omitting live and quiet is removed.

## T7. Dashboard, README, spec

Files: `grafana/pg-claude-code-recorder.json`, `README.md`, spec `### Signals` section.

Dashboard (keep every existing title unique and unchanged; panels appended, `$__rate_interval` of at least 5m for
loop histograms, prefixed names, "no alert covers this panel yet" unless T8 covers it): `loop duration p99 by loop`
(`histogram_quantile(0.99, sum by (le, loop) (rate(...loop_duration_seconds_bucket[$__rate_interval])))`), `loop
runs by loop and outcome`, `loop in flight`, `ingest results`, `sweep walked vs unchanged candidate`, `lock waits`
(copier and store, p99), `request duration p99 by op`, `requests in flight by op`, `slow requests (over 10 s) by op`.
README: the runbook gains "which loop is slow" (read `loop duration p99 by loop`, then `status` loops table, then the
log line for that loop) and "which request is slow" (`request duration p99 by op`, then the `slow_request` log
lines). Spec: replace the phase 0 sentence "every periodic loop MUST report duration and run outcome (phase 1
completes the list)" with the full list from the T2 table, and add the new metric names, the `slow_request` event
and the new alert rows.

Done when: registration and alert-structure checks pass and `jq` parses the dashboard.

## T8. CPU and WAL alerts

Files: `grafana/alerting/alerts.yaml`, `alerts-delete.yaml`, `grafana/check-alert-structure.sh` (`EXPECTED_UIDS`,
rule count), `grafana/alerting/rule-tests/`, the spec alert table, and every list of rule uids
(`rg -n 'pg-ccrec-copy-lag'` over the repo; the probe package keeps one).

Rules (thresholds are placeholders until T9 supplies the baseline; the plan commits to a rule: threshold is the
baseline p99 over a day times 2, rounded up, and the value used is written in the rule description):

- `pg-ccrec-cpu-high`: `rate(process_cpu_seconds_total[10m])` above the threshold for 30 m, warning, `noDataState`
  OK (a scraped-down daemon is covered by `daemon-down`). Starting value from the phase 0 figure: 1.0 core (about
  1.6 times the 0.63 measured; revisit in T9).
- `pg-ccrec-wal-large`: `max(wal_bytes)` above 1 GiB for 15 m, warning. (Phase 0 left the WAL at 88 MB; before the
  checkpoint loop it reached 13.9 GB.)
- `pg-ccrec-loop-stalled`: per `loop` (via the metric's own label), `time() - loop_last_timestamp_seconds` above a
  per-loop bound (`watcher_sweep` 3 x `sweep.interval`; `tracker_*` 120 s; `status_refresh` 60 s; `parser_drain`
  excluded because it is event-driven) or `loop_in_flight_seconds` above 600, for 10 m, warning. The bound for each
  loop is written in the rule as a literal with a comment naming its source setting, as `heartbeat-stale` does.
- `pg-ccrec-request-slow`: `sum(increase(query_duration_seconds_count[1h])) - sum(increase(...bucket{le="10"}[1h]))`
  above 20, warning (phase 0 saw 109 in 66 min; the rule is meant to stop firing after phases 2, 3 and 5).

Each rule has a promtool unit test for fire, no-fire and short-breach cases, and a description that begins with
`pg-claude-code-recorder ` and names a first command, as the existing rules do. A downstream deployment repo may
keep its own alert uid list; this plan cannot edit it, so the report names that follow-up.

Done when: structure check and rule tests pass.

## T9. Baseline capture (gated follow-up)

Blocked on the apply of T1 to T7 and one hour of data with a scrape (or the loopback endpoint when no scrape job
exists, as phase 0 did). Record as a bead comment: CPU cores averaged over the hour, per-loop p50 and p99, per-op p99,
`slow_request` count, WAL bytes, in-flight maximum. Re-set T8's thresholds from it if they disagree by more than
a factor of 2.

## Review findings folded in (corrections to the tasks above; where they disagree, this section wins)

1. Existing loops. `loop_runs_total{loop}` already has `settle_tick`, `refresh_gauges` and `wal_checkpoint`. The
   final loop set is those three plus the eight in T2's table (11 values). T2 moves the durations of the three
   existing loops into `loop_duration_seconds{loop}` and KEEPS the old unlabelled histograms
   (`settle_tick_duration_seconds`, `refresh_gauges_duration_seconds`, `wal_checkpoint_duration_seconds`) as
   written, so dashboards and the existing alert keep working; end state: three old histograms plus one labelled
   family that includes all 11 loops. T6 (`status` loops table) and T8 (`loop-stalled`) cover all 11; the stalled
   rule carries the `loop` label into its summary and description so the alert names the loop.
2. `watcher_sweep` is wrapped around the WALK, not just the `onSweep` callback: add an `OnSweepDone`-style timing
   hook in `watcher.sweep` (the walk happens before `OnSweep`). `removal_pass` is timed inside `Settler.Tick`
   through an injected `Timer` callback in `settle.Config` (a daemon-side wrapper cannot see inside), and its
   duration is documented as nested inside `settle_tick` (not additive). The operator `remove` operation is NOT
   counted as the loop. The tracker's grace-window goroutines are out of scope and listed as such.
3. T3 `ingest_results_total{result}`: values are the real `copier.Result.Action` strings, not invented ones.
   The task first lists them with `rg -n 'Action:' internal/copier` (they include hyphenated `new-generation`,
   plus `snapshot`, `link`, `tombstone`, `missing`, `raced`, `skipped`, `unchanged`, `copied`) and the catalogue
   enumerates exactly them, with hyphens mapped to underscores in the label value by one function under test. An
   ingest that returns an error is `result="error"` (error is not an Action, the wrapper adds it).
4. T4 double observation is dropped: mixed label sets under one family make `sum(...)` double count. Instead the
   histogram becomes labelled with `op` ONLY, the unlabelled series is removed, and every expression that read it
   (`rg -n 'query_duration_seconds|feed_latency_seconds' grafana README.md`, the phase 0 alerts and panels) is
   rewritten with `sum without (op)` in the same task. The `request-slow` rule uses `sum` over all `op`. The golden
   render test is updated and its diff reviewed.
5. T5: the old series persists once written (it is set at one site and is not registered at zero); say that, and
   list the tests to rewrite (`clone_test.go`, `copier_test.go` assertions on the labelled series). Names:
   `clone_private_size_max_bytes` and `clone_private_size_total_bytes` (unit last, no `_sum` on a gauge). The
   release note goes in the commit message and the dashboard panel description, not a README release-notes section.
6. Test seams. `settleLoop`, `trackLoop`, `refreshLoop` use real `time.NewTicker`; `statusLoop` uses `time.After`;
   `doctorLoop` uses `time.NewTimer`. T2 starts with a step that introduces a `Daemon.ticker(d)` seam (an injected
   function returning a channel and a stop), defaulting to the real ticker, so each production loop body is driven
   by the fake clock. Where a loop body is a small function (`parseLoop`'s drain), the test calls the step directly
   and a separate test covers the wiring through the seam. The counting driver is the phase 0 task's
   `internal/store/countdriver` package (named explicitly in T2, T3 and T6).
7. Per-event cost rule, corrected: `copier_ingest` is the one per-event loop and gets a LIGHT path: one histogram
   observe and one counter add for `ingest_results_total` (two lock acquisitions); no per-ingest last-timestamp, no
   in-flight set, no `loop_runs_total` add (the results counter is the run count). The rule "at most two registry
   lock acquisitions per call" is tested by a counting `Registry` wrapper hook (`Registry.OnLock` test seam added in
   T1, nil in production).
8. `loop_in_flight_seconds` is computed per loop from a start-time map held by the wrapper (concurrent passes keep
   the oldest start), and is refreshed on EVERY scrape through `scrapeRefresh` as well as by the refresh loop, so a
   stalled refresh loop still reports (it is process-local, no store read).
9. Alerts: `request-slow` becomes a ratio, requests over 10 s divided by all requests over 1 h, above 2% with a
   floor of 50 requests (phase 0 saw 109 of a few thousand, so it fires now and stops after phases 2, 3 and 5;
   the description says so). `cpu-high` and every expression use the prefixed names. T8 depends on T2 (needs
   `loop_last_timestamp_seconds`). Promtool tests add a stale-data and `noDataState` case. `settle-tick-slow`
   stays the only settle-specific stall alert; `refresh_gauges` stalls are covered by `loop-stalled`.
10. UX: T6's `status` prints a `stalled` marker beside any loop whose age exceeds its bound (the same bounds as the
    alert, defined once in code and read by both the CLI and a test that compares them with the alert literals).
11. Negative controls added: T1 (drop a HELP line), T2 (skip the in-flight clear), T6 (derive `live` as `quiet`).

## Risks and rollback

- Registry mutex contention from per-request double observation: bounded by the T2/T4 rule and measured by the
  `lock waits` panel itself. Rollback: revert the commit; no schema or wire change besides additive `status` fields.
- A renamed series breaks a dashboard: T5 is the only removal; it is announced in the README. The aggregate request
  histograms keep their old names.
- Cardinality: `loop` (8) x `outcome` (2), `op` (16 plus unknown), `result` (about 8): all bounded and enumerated.

## Proposed bead breakdown

One epic with nine children matching T1 to T9; dependency edges as in "Task list and order"; T9 is a gated
verification bead (the `pb:pb-gate-lifecycle` skill), T8 depends on T9. Sizes: T1 M, T2 M, T3 S, T4 S, T5 S, T6 S,
T7 M, T8 S, T9 S. Labels: `agent-support`, `pg-claude-code-recorder`.
