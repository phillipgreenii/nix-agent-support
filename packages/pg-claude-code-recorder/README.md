# pg-claude-code-recorder

The recorder for Claude Code state (docket `pg2-v77tsx`; design
`docs/superpowers/specs/2026-10-30-pg-claude-code-recorder-design.md`). The sections below
follow the phases as they landed.

## Packaging

A Go module built with `mkGoApp` (Pattern A, `.claude/rules/package-versioning.md`),
a committed `gomod2nix.toml` and `update-deps.sh`.

## The darwin-only cgo file and the Apple SDK

`internal/platform/platform_darwin.go` is the only cgo file (`//go:build darwin && cgo`).
It links `CoreFoundation` and calls one function, which proves the cgo toolchain and the
Apple SDK are reachable. Nothing is added to `default.nix` for it: `mkGoApp` builds with
the nixpkgs darwin stdenv, whose clang wrapper and default Apple SDK supply the C compiler,
the `CoreFoundation` framework headers and the linker, and cgo is on by default there
(`CGO_ENABLED=1`). On Linux the build is `CGO_ENABLED=0`; `platform_other.go` takes over and
the `-linux-cross-compile` check (`GOOS=linux CGO_ENABLED=0 go vet ./...`) keeps that building.

## Build tags and checks

| Tag           | Tier                                                                                                           | Check                                                                         |
| ------------- | -------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| none          | unit (fast, hermetic, `-timeout 120s` per package)                                                             | `pg-claude-code-recorder-go-tests`                                            |
| `integration` | daemon, subprocess, real external tool                                                                         | `pg-claude-code-recorder-integration-tests` (on demand, `-race`, 60m timeout) |
| `faults`      | fault injection                                                                                                | guarded by `-integration-tests-gating`                                        |
| `soak`        | soak (`checks.<system>.pg-claude-code-recorder-soak`, budgets in `internal/parser/testdata/soak-budgets.json`) | guarded by `-integration-tests-gating`                                        |

The `ablation` tag is a development measurement harness for the rebuild's write path, not a tier
and not a check: it is never part of a gate, and its build is only vetted by the gating check
below. When and how to run it: `docs/ablation/README.md`.

`TestUnitTierHasNoIntegrationTests` fails a unit-tier test file that execs a built binary,
builds Go, or needs a real external tool. `pg-claude-code-recorder-integration-tests-gating`
proves each tag splits every package that carries it and that the module vets with and
without each tag. `pg-claude-code-recorder-linux-cross-compile` is the Linux build guard.
The integration check MUST NOT be wired into a hook, `run-unit-tests` or pre-land
(V-4, `docs/adr/0111-real-process-test-suites-split-by-cost-driver-under-v-4.md`).

The unit gate has a per-package time budget: the check runs `go test -timeout 120s`, so a
package whose tests exceed it fails.

## The watcher

`internal/watcher` reports changed paths under the Claude directory and
`<controlStateDir>/ends` to a sink. Every event is a hint: the watcher filters the path, does one
`lstat`, then reports; event flags are never trusted. On darwin one FSEvents stream (a small cgo
wrapper, `backend_darwin.go`) covers both roots. After downtime it replays FSEvents only when the
gap is under about 10 minutes and the volume UUID is unchanged (otherwise it logs `fsevents_gap`
and sweeps), and it sweeps on `MustScanSubDirs` and every `sweep.interval` (default 15 minutes).
The darwin sweep uses `getattrlistbulk`; it reports device, inode, birth time, size and
modification time per file. `Wake(path)` and `Rescan()` are the test seams. `fsnotify` is not
used (kqueue needs one file descriptor per watched file).

### What is ingested and walked

One path-shape allowlist (`internal/scope/shape.go`) decides what the recorder ingests, and both
`scope.Match` (the per-event filter) and the sweep walker's directory predicate
(`scope.DescendDir`) answer from it, so they cannot disagree. Paths are relative to the Claude
directory:

- `history.jsonl`;
- `sessions/**`;
- `projects/<project>/<name>` where the name ends in `.jsonl` (transcripts and `.status.jsonl`),
  `.session-mode.json` or `.compaction.json`;
- `projects/<project>/<uuid>/subagents/**`, `.../tool-results/**` and
  `.../workflows/**`, where a session directory is recognized by its UUID shape, never by a list of
  names to skip;
- the control `ends` root, walked whole.

A session directory name must be a lower-case UUID (`scope.IsUUID`, the same shape as `settle`'s
session pattern); Claude writes lower-case ids, and an upper-case directory is not walked.

Everything else (`file-history`, `plugins`, `security`, `paste-cache`, `usage-data`, `state`,
`telemetry`, `shell-snapshots`, a non-UUID directory under a project such as `spikes-*`, `memory` or
`artifacts`, project-level `sessions-index.json` and `.orphaned-*` / `.superseded-*` copies) is
neither walked nor ingested. The sweep roots are `projects/`, `sessions/` and the ends root, plus a
direct `lstat` of `history.jsonl`; the Claude directory itself is never read, and a directory the
predicate rejects is skipped before it is opened (in both the darwin `getattrlistbulk` walk and the
portable `WalkDir`). A hint for a rejected path costs one string comparison on its first path
component. The project-level files that no source reads were dropped. The FSEvents stream still
covers the whole Claude directory (it cannot filter by subdirectory), so rejected events still
arrive and are dropped by `Match`.

`scratchpad/` is not ingested (an operator decision); `workflows/` is ingested as before. What that
means for removal:

- A Conversation whose directory holds a scratchpad file the recorder never recorded is removed as
  usual; the file is left in place, untouched, and the session directory stays because it is not
  empty.
- A scratchpad file an EARLIER version already recorded stays in the store, is never re-ingested
  (so its copy goes stale), and is ignored by removal: it belongs to no Conversation for the
  stage, verify, hold and removal steps, so a change to it on disk cannot hold the Conversation, and
  removal never deletes or quarantines it. Those old records stay until a one-time, operator-run
  store cleanup removes them; extract of an already-settled Conversation still restores them from
  its manifest.
- Behavior change for renames: the sweep still reads the identity of every file in the directories
  it walks, before the scope filter, so a file renamed from an ingested name to a rejected one in the
  same walked directory (for example `projects/p/X.jsonl` to `projects/p/.orphaned-X.jsonl`) has its
  old path closed, as before, and is not tombstoned as deleted. A file moved out of the walked
  directories (for example into `file-history/`) is not seen, so its old path is tombstoned.

The scale check `TestSoakPrunedSweep` (tag `soak`, over 100,000 synthetic files) runs in the
`pg-claude-code-recorder-soak` check, never in the unit gate.

### Linux: the inotify watch limit

On Linux the watcher uses inotify (`CGO_ENABLED=0`, standard library only) with one watch per
WATCHED directory, and the sweep is a plain directory walk. The watch walk uses the same directory
predicate as the sweep (`watchtree.go`; only the inotify syscalls are in `backend_linux.go`), so
only allowed directories get a watch: `projects/` and below down to the session directories and
their `subagents`, `tool-results` and `workflows` trees, `sessions/`, and the ends root. A
configured root (the Claude directory itself, the ends root) is always watched, so `history.jsonl`
and a newly created `projects/` are seen; a directory created later is checked against the
predicate first. The count is therefore the number of allowed directories, not of every
directory in the Claude directory; the previous figure of about 13,000 described the unpruned walk and
no longer applies (measure it on your tree). Linux runtime behavior of this walk has only been
compile-checked. inotify keeps no history, so every start sweeps. The kernel caps watches per user
with `fs.inotify.max_user_watches`; raise it above the allowed-directory count, for example
`sysctl fs.inotify.max_user_watches=524288`. A symlinked `history.jsonl` is skipped (the sweep
`lstat`s it and ingests regular files only), as it always was. When the limit is hit, adding a watch fails with `ENOSPC` and `Start` returns that error. An inotify
queue overflow (`fs.inotify.max_queued_events`) triggers a sweep. Linux reports no birth time
(`FileInfo.Birth` is zero).

## Serve, status and coverage (phase 1d)

`pg-claude-code-recorder serve --config FILE` composes the watcher, the copier and the store and
listens on `recorder.sock` (mode 0600, HTTP/1.1 with JSON bodies: `GET /v1/hello`,
`POST /v1/<operation>`). The Go client side reads `PG_CLAUDE_CODE_RECORDER_SOCKET`, else the
config's `socket` (the module renders `$XDG_STATE_HOME/pg-claude-code-recorder/recorder.sock`, the client default). `internal/server` owns the framing and
the operation registry (`Register(name, kind, handler)`); phases 2, 3, 4 and 6 add operations
there. `status` and `coverage` are the two operations this phase serves.

Config: `$XDG_CONFIG_HOME/pg-claude-code-recorder/config.toml` (override with `--config` or
`PG_CLAUDE_CODE_RECORDER_CONFIG`). An option `a.b` is the key `b` in the table `[a]`; durations
are Go duration strings with a `d` suffix for days. Keys a later phase owns are tolerated.
`controlStateDir` is optional; when set, `<controlStateDir>/ends` is the watcher's second root and
`coverage` lists it with its presence and file count.

Sweep pacing: `sweep.max_files_per_second` (default 100, `0` unlimited) caps how many files per
second the sweep and the first catch-up ingest; hint-driven ingest of live files is never paced, so
copy lag is unaffected by the cap except that a catch-up holds the watcher's lock (a pass over N
files takes at least N / rate seconds, so keep N / rate under the 600 s `loop-stalled` pass bound:
57,000 files need at least 95 files per second). A sweep skips a file the copier last found caught up when its size, mtime and inode are unchanged, and
nothing expires that skip on a timer (`sweep.verify_every` was retired: a config that still sets it is
refused at start with a message naming it). The full verification runs only on request:
`pg-claude-code-recorder sweep --force [--wait]` empties the skip cache and the `seen` table and starts a
sweep now that ingests every file (`--wait` blocks until it finishes). Residual risk: a rewrite that keeps
the same size, mtime and inode is invisible to the incremental sweep; it is found by the doctor (its 24 h
cycle) or by `sweep --force`.
Snapshot files (the registry and session-mode JSON files) of 64 KiB or less are never skipped: they are re-read
on every sweep, so a same-size rewrite of one is found within one sweep. Larger snapshot files follow the same skip
rule as transcripts. The first sweep after a start with an empty `seen` table is a full verification (it skips
nothing, and counts in `sweep_verify_forced_total` like a forced sweep). Every sweep logs one `sweep_done` line with
`walked`, `skipped`, `ingested`, `missing`, `duration_ms` and `verify` (true for a full verification); its duration is
also in `loop_duration_seconds{loop="watcher_sweep"}`.
The caught-up cache is persisted in the `seen` table of `index.db` (path, file id, device, inode, birth
time, size, mtime, generation; derived data, see `docs/schema-v3-design.md` 5.1), so a restart skips every
file that has not changed instead of ingesting all of them again. The first start after an upgrade finds
the table empty, logs `seen empty: full verification sweep` and runs one full sweep; the table is also
emptied by `serve --rebuild`, by a restored database and by a build with another seen epoch. Gauges:
`seen_rows` and `seen_load_seconds`.
A row loads only while its device, inode and birth time still equal the file's stored identity. A snapshot
file whose device number changed (for example across a reboot) without its content changing has that
identity re-stamped by the sweep that finds it unchanged, so the first sweep after a restart on an unchanged
tree skips every walked file. The one sweep after such a change still ingests those files once.

First run copies newest first with a per-file throttle (`copy.throttle`, default 2ms after each
file that moved bytes), and answers `degraded: true` (exit 5 from the command, partial answer on
stdout) until the first full sweep is done; `--allow-partial` turns that into exit 0. Phase 1 has no
parser, so parse lag is reported as 0 and `caught_up` means the first backfill is done.

### Signals

- Metrics: Prometheus text on `GET /metrics`, every series prefixed `pg_claude_code_recorder_`.
  **Address and config key:** off by default; set `metrics.addr` in the config (a loopback
  `host:port`), or `--metrics-addr`, or `PG_CLAUDE_CODE_RECORDER_METRICS_ADDR`. The darwin module
  packet (7a) chooses the port and registers the scrape. Series: `dropped_bytes_total{reason}`
  (every reason registered at zero), `copy_lag_bytes`, `copy_lag_seconds`,
  `copy_lag_seconds_histogram`, `last_sweep_timestamp_seconds` (the heartbeat),
  `state_used_bytes` (database-derived with no directory walk: the copies the database lists plus
  `index.db`, its `-wal`/`-shm`, the journal and the watcher checkpoint, plus `quarantine_bytes`;
  it excludes archives, manifests, the doctor cursor and orphans, so it steps down when first
  deployed), `requests_total{client,op,outcome}`,
  `query_duration_seconds{op}`, `feed_latency_seconds{op}` (no unlabelled series: use `sum without (op)`),
  `requests_inflight{op}`, `wait_duration_seconds`, `backfill_files_*`,
  `caught_up`, and the loop and process series: `settle_tick_duration_seconds`,
  `settle_tick_examined_total`, `settle_tick_transitions_total`, `settle_tick_last_duration_seconds`,
  `settle_tick_last_timestamp_seconds`, `settle_run_lock_wait_total{op}` and
  `settle_run_lock_wait_seconds_total{op}` (time callers queued for the settler's run lock; a `settle_run_lock_wait_slow`
  warning is logged for a wait of 5 s or more), `refresh_gauges_duration_seconds`, `loop_runs_total{loop,outcome}`,
  `process_cpu_seconds_total`, `go_goroutines`, `go_memstats_heap_alloc_bytes`,
  `go_memstats_alloc_bytes_total`, `go_gc_pause_seconds_total`, `wal_bytes` and the database/sql pool figures `db_open_connections`,
  `db_in_use_connections`, `db_idle_connections`, `db_wait_total`, `db_wait_seconds_total`,
  `field_type_mismatch_total{source,type,field}` (a catalogued field whose value did not convert losslessly to its
  declared type; see `internal/source/storage.go`; registered when first incremented, so a clean run has no series),
  `sweep_files_total{outcome}` (walked, unchanged_candidate, skipped, ingested; skipped plus ingested is the walked count of a sweep), `sweep_skipped_ratio` (the last sweep, skipped over walked), `sweep_lstat_fallbacks_total` (files the walk did not show present, so checked with an lstat), `sweep_verify_forced_total` (full-verification sweeps: a forced sweep, or the first sweep after a start with an empty seen table) and `sweep_changed_total{reason}` (new, size, mtime, ino,
  not_cached: why a walked file is not known unchanged), `db_max_lifetime_closed_total` and `db_max_idle_closed_total` (label `pool`: `read` or `write`; a rising
  `db_wait_seconds_total` means callers queue for a connection; the read pool is bounded by `query.max_connections`, default 8, at least 2, 0 means unbounded, and the read panel is "read pool wait"). The `client` label is whitelisted (`clients` in the config adds names); any other
  name is `other`. The exporter is a small text renderer over the in-process registry, so the
  package takes no Prometheus client dependency.
- Log: JSONL on standard error (or `logFile`), service `pg-claude-code-recorder`, fields `ts`,
  `level`, `event`, and where they apply `file_id`, `session`, `generation`, `offset`,
  `parser_version`, `cursor`, `stage`. Each request logs `request` with the caller's `--client`. Each settle tick
  logs `settle_tick` (`outcome`, `duration_ms`, `examined`, `unchanged`, `settled_now`, `transitions`) and a tick
  over one minute also logs `settle_tick_slow` with `duration_ms`, `examined` and the phase breakdown
  (`reads_ms`, `prefilter_ms`, `evaluate_ms`, `removal_ms`, `refresh_ms`). A tick still running after a minute logs `settle_tick_stuck` (`in_flight_ms`) while it is stuck, and the gauge `settle_tick_in_flight_seconds` is the age of the running tick (0 when none; the last-timestamp gauge only moves when a tick ends). A start logs one `startup_phase`
  (`phase`, `duration_ms`) per phase, a `startup_phase_begin` first for the phases that can run long, and one
  `startup_complete` (`duration_ms`, `phases`) when it serves.
- `status` shows `stage_counts` with `live` and `quiet` included (the figures the last settle refresh set on the
  `conversations_by_stage` gauge), `last_tick` (`duration_ms`, `examined`, `age_s`; absent until a tick completes)
  and a `loops` table for all 11 loops (`last_duration_ms`, `last_outcome`, `age_s`, `in_flight_s`, `stalled`; a loop
  that has not run has no duration, outcome or age). A loop is `stalled` when a pass has run over 600 s, or when its
  last pass ended longer ago than its bound: `watcher_sweep` 3 times `sweep.interval`, `tracker_tick` and
  `tracker_poll` 120 s, `status_refresh` 60 s; other loops, and `parser_drain`, have no age bound. The bounds live in
  `internal/obs/stall.go`. `status --watch` runs an O(files) loop on the daemon every 2 s, so do not leave it open while
  profiling.
- The `status` copy and parse lag figures are as last recorded: the status loop (every 5 s) takes each file's size from
  the copier's last recorded size and its offsets from the maintained lag state, and makes a `Lstat` only for the
  exceptions: a file whose recorded size differs from its copy offset, or whose offsets changed within `status.fresh`
  (default 2 minutes). A file that changed on disk without a watcher event shows in `status` only after the next sweep
  (up to `sweep.interval`); the `coverage` command still stats every file and shows it at once. `status` carries
  `stat_exceptions` (files stat-ed by the last pass) and `last_full_stat_age_s` (age of the last coverage run or
  watcher sweep), and a file entry's `size_source` is `recorded` or `stat`.
  Sizes are as last recorded; run `coverage` for the on-disk truth.
- The copy and parse lag gauges (`copy_lag_bytes`, `copy_lag_seconds`, parse lag) are maintained from commits: each
  copier and parser commit adjusts the lag state, so a refresh reads no whole table. `lag_rebuild_total{result}`
  (`match` or `drift`) counts the periodic reconcile against a full rebuild, and a `lag_drift` warning is logged when
  the maintained state differed from the rebuilt one (lag bytes and unknown kinds; lag seconds is not compared).
- Parse lag seconds (`parse_lag_seconds`, `parse_lag_s` in `status --json`) is how long parse has been behind copy:
  one in-memory value per generation, the time copy first got ahead of parse. It is set when the copy offset advances
  past the parse offset while they were equal and cleared when parse catches up; a further copy or a partial parse
  does not move it. The figure is the oldest such value over generations, and `status` health reads it (behind at 60
  s or more). It is not read from the commits table, so a restart cannot recover it: at start every generation with
  parse behind copy is seeded to "now", which understates the lag once after a restart and is correct from then on.
  It is not part of the doctor comparison, since a journal replay cannot know parse progress.

### pprof and the profiling harness

`serve --pprof` (or `PG_CLAUDE_CODE_RECORDER_PPROF_ADDR`, which wins) opens the opt-in endpoint
through `github.com/phillipgreenii/x/pprofserver` on `$XDG_STATE_HOME/pg-claude-code-recorder/pprof.sock`,
mode 0600. **Recorded mismatch:** the design says elsewhere that `pprof.sock` sits in the state
directory; this follows the more specific packaging rule above (pg-connector's option), so the
profiler socket never shares a directory with the data store.

`scripts/profile-cpu.sh --sock SOCK --seconds N [--pid PID]` runs the runbook recipe
(`curl --unix-socket ... /debug/pprof/profile?seconds=N`, then `go tool pprof -top`) and prints
`cpu_percent_of_one_core`. The real-tree check runs it for 600 seconds; the bounds are the
copier and watcher at or below 5% of one core with no client attached, the whole daemon at or
below 10% once the parser and feed run (re-measure, not derived).

### Exit codes and errors

0 success, 1 unexpected, 2 usage, 3 daemon offline, 4 not found, 5 degraded or behind, 6 wait
timed out, 7 cursor expired, 8 refused, 9 doctor mismatch (`--help` prints the table). Without
`--json` stderr gets `error: <name>` and one actionable line; with `--json` one
`{"error","exit","message"}` object. `status` always answers: offline it prints the socket path,
launch agent state (`launchctl list <label>`, label from `PG_CLAUDE_CODE_RECORDER_LAUNCH_LABEL`,
default `com.phillipg.pg-claude-code-recorder` until phase 7a names it), the log path and the
state directory size read from disk, and exits 3.

### Schema bump: refuse to start, do not rebuild implicitly

Decision: when `index.db` carries an older `schema_version` than the binary needs, `serve` refuses to start
(option b) rather than rebuilding on its own (option a). It logs one `schema_rebuild_required` error event
(`found`, `known`, `command`), prints `error: schema_rebuild_required` with the rebuild steps, leaves `index.db`
untouched and exits 8 (refused). A newer `schema_version` and other open failures keep the generic `open_failed`
path.

Rationale: a full rebuild is not bounded in any useful way (4758 s for 17.0M records on 2026-11-04, and it grows
with the journal), runs with the daemon's socket down, needs the free-space preflight to pass and sets the old
file aside, so it is a deliberate operator step, not a side effect of a launch agent restart. An automatic
rebuild would also make a downgrade-and-upgrade flap silently cost an hour. The rebuild command already exists
and is safe to run (`index.db.pre-rebuild` keeps the old file).

Operator steps: stop the launch agent, run `pg-claude-code-recorder rebuild`, start the launch agent
(`launchctl kickstart gui/$(id -u)/com.phillipg.pg-claude-code-recorder`).

Launch agent: the agent's script runs `serve` and turns exit 8 into exit 0, and its `KeepAlive` is
`{ SuccessfulExit = false; }` (launchd's dictionary cannot key on one exit code), so a refused start runs once
and stays stopped instead of looping, while a crash (any other non-zero exit) is still restarted. SIGTERM and
SIGINT are forwarded to the daemon. A start after the next login or boot runs once more and refuses again until
the rebuild is done.

Alerting: the `schema_rebuild_required` log event is shipped to Loki by the daemon's log source and the Grafana
rule `pg-ccrec-schema-rebuild-required` fires on any such event in the last hour (it clears an hour later;
`pg-ccrec-daemon-down` stays up until the daemon is back). The `status` offline answer (exit 3, launch agent
state, log path) shows the same state on the command line.

## Conventions for later packets

- External commands go through `github.com/phillipgreenii/x`'s `cmdrun`, as `pg-router-probe` does.
- The pprof socket follows pg-connector's option (`$XDG_STATE_HOME/<instance>/pprof.sock`), not
  `stateDir`, so the profiler socket never shares a directory with the data store.
- `logs.maxBytes` does not exist: the shared hourly rotator that `manageLogs` enables rotates
  the logs; the daemon does not rotate them itself.
  Retention is unchanged: the per-service knobs are `manageLogs.thresholdBytes` (default 20 MiB) and
  `manageLogs.archiveCount` (default 1) on the recorder `userAgents` entry in
  `home/programs/pg-claude-code-recorder/default.nix` (the entry takes the defaults). At about 0.99 MB/h in
  steady state the live file holds about a day plus one archive; Loki (168 h retention) is the incident source.
  A parser-version re-parse is a burst, not steady state. Its per-file `parser_reset` event is therefore logged
  at debug level, so it is absent from the default info-level log (tests and `EventsNamed` still see it).
- Home Manager scope (this repo's `CLAUDE.md`, phase 7a): a terminal MUST NOT take the
  LaunchAgent without a personal revision that declares `launchdServices.userAgents`;
  `execPath` is unsupported in the HM scope; OTel settings are read null-safely from
  `osConfig.phillipgreenii.observability`; the eval-test stub matches personal's
  `lib/launchd-service-submodule.nix`.
- Fixtures use generic `example-*` names (the identifier guard, `scripts/identifier-allowlists/pg-claude-code-recorder.txt`).

## Parse, query and search (phase 2)

Sources are compiled in (`internal/source`): `claude-transcript` (session and subagent transcripts)
and `claude-history`. The parser (`internal/parser`) reads each copy from its parse offset to the
last newline within the copy offset and commits records, typed fields, text indexes and the new
parse offset in one transaction; a re-parse replaces a file generation atomically behind a
`parse_resets` marker. `text` fields are search-indexed in the FTS5 word index, and so are `opaque` fields (the `schema_version` 3
layout has no trigram table; a `schema_version` 2 file kept one and its word search did not reach tool bodies). Exact-punctuation and partial-token lookups over
opaque fields use `mode=substring`, which scans the copies (see "Substring search" below); no file contents are
stored in `index.db`.

In a `schema_version` 3 `index.db`, a field a source declares with `record:` or `ext:` storage (`cwd`,
`git_branch`, `version`, `entrypoint`, `user_type`, and the `usage.*` counters) is filtered, grouped, ordered
and selected through its typed column, with the same field names and answers as the version 2 layout, which
read `fields`. A raw value that did not convert to the column's type is kept in a
`fields` row; it counts as present for `exists`, select, group and order, and never matches a typed comparison.

Typed scalars live in `fields(rec_id, name_id, s, n, f)`, `WITHOUT ROWID`, keyed `(rec_id, name_id)`;
the field name is stored once in `field_names(id, name UNIQUE)` and the query layer resolves a name to
its id (`name_id = (SELECT id FROM field_names WHERE name = ?)`). Invariants: a record has at most one
row per field name; when a record yields the same name twice, the LAST value wins and replaces the
whole row (`s`, `n` and `f` together, never a mix of two values); `fields_name_n` indexes
`(name_id, n)` for numeric filters. A field whose value another table already holds gets no row: `type` and
`session_id` are the record's own columns, and `project` and `file_session` (the path captures) are read back from
the file's path under the Claude directory; an older database that still stores them answers the same.

**Schema version 3** (the design is `docs/schema-v3-design.md`): `records` carries `session_ref`, an integer
into `sessions` (`session_id`, `project`, first time, first version, entrypoint and user type), instead of the
36-character session text and its two indexes; `session_stats` and `session_files` hold the per-session last
time, record count and files; `cwd`, `git_branch`, `version`, `entrypoint` and `user_type` are interned ids
(`lk_*` tables) on the record; the usage counters of an assistant line are one `ext_assistant_usage` row;
there is no trigram index; `records_cwd` and `records_branch` are partial indexes on the lookup ids. The
wire protocol, field names and answers are unchanged (a parity test rebuilds one corpus into both layouts and
compares them). Interning `source` and `type` was not taken. The daemon always builds this layout, and a
`schema_version` 2 `index.db` is refused by a version 3 binary until it is rebuilt (see "Rebuild").

**Schema version 4** adds `records.own_time`, a marker that the line carried its own `timestamp` (so
`records.time_ns` is that value and not the time the recorder saw the bytes). A record with the marker gets no
`timestamp` row in `fields`; the query layer serves the `timestamp` field (filter, `exists`, select, order,
group, aggregate) from `time_ns` where the marker is set, and a line with no timestamp of its own, and every
child record (which has none), still has no `timestamp`, exactly as before. The wire protocol, field names and
answers are unchanged (a parity test compares the served answers with the former stored rows over lines with
and without a timestamp). A `timestamp` range filter no longer uses the `fields_name_n` index; the `time`
field, which has always read `time_ns`, is the indexed way to bound a time. Every `schema_version` 3 or older
`index.db` is refused by a version 4 binary until it is rebuilt: **a rebuild to version 4 is required**, and
because no version 3 file was ever deployed, rebuilding straight to version 4 is the one rebuild to run (see
"Rebuild").

**Untimed records carry no time.** A record whose line has no timestamp of its own stores `time_ns = 0` and
`own_time = 0`, not the time the recorder copied it (a record time is stored only when it is known to be the
record's own). A child record keeps its line's time. Every reader of `time_ns` skips a zero: a session's first
and last time and the lifecycle last-activity and cwd lookups ignore untimed rows, and a `time` range filter
never matches one (so a lone upper bound does not return them). Existing rows are re-derived through the parser
version: a source whose stamped `ParserVersion` differs from the stored one is re-parsed from its stored
snapshots on the next parse, so this change bumps every source's version (transcript and history to 3, the
others to 2) and the live daemon re-parses on first start; the copies, not the rows, are the source of truth.

```sh
echo '{"where":{"field":"type","eq":"assistant"},"group":["model"],"aggregate":[{"fn":"sum","field":"usage.output_tokens","distinct":"message.id","as":"output_tokens"}]}' | pg-claude-code-recorder query
echo '{"mode":"substring","q":"--no-verify","fields":["tool_use.input"]}' | pg-claude-code-recorder search
pg-claude-code-recorder find <session-id|text>
```

### Word search over tool bodies

In an `index.db` built without the trigram table, `mode=word` reaches the opaque fields too (`tool_use.input`,
`tool_result.body`, saved tool-result bodies, pasted contents), by name in `fields` or, with no `fields`, along
with the prose fields. A query is a word or phrase match over case-insensitive tokens: several words are an AND,
a double-quoted string is a phrase. Punctuation splits tokens, so `--no-verify` matches the phrase `no verify`
(and `git commit --no-verify` and `no-verify` alike) but not the exact characters, and a partial token such as
`verif` matches nothing. Use `mode=substring` for exact punctuation and partial tokens. With the trigram table
present (a `schema_version` 2 file) a word search over an opaque field returns no hits.

### Substring search

`mode=substring` finds a literal, case-sensitive string (a path, a flag such as `--no-verify`, a partial token)
in the opaque fields (`tool_use.input`, `tool_result.body`, saved tool-result bodies, pasted contents; `fields`
narrows it to some of them). It does not use a full-text index: the candidates are the records of those fields
that match the request's `where` filter (session, time range, cwd, project and the rest), newest first, and each
candidate's text is read from the recorder's own copy and searched for the literal. It never reads the original
Claude tree. `match_at` is the byte offset of the match in the field text, and `snippet` the text around it.

Cost is bounded by a byte budget, not by refusing unscoped requests. One request reads at most
`DefaultSubstringBudget` (256 MiB) of copy text, and stops earlier at the daemon's query deadline
(`query.timeout`). When it stops with candidates left it answers normally (never an error) with
`partial: true`, `scanned_bytes` (how much text it read), `scanned_to` (the time of the oldest record it
reached) and a `note`; the hits it returns are correct but may be incomplete, so a miss with `partial: true`
is not a proof of absence. Narrow the request (a `session_id` or `time` or `cwd` filter) or search below
`scanned_to` to cover the rest. Why a budget and not a rejection: an agent with only a string and no session
still gets the most recent matches, the wire shape needs no new error, and a scoped request that fits the
budget is exact and not partial. `limit` and `more` behave as for word search.

A substring query under three characters returns no hits and `too_short: true`. Config key
`parse.maxLineBytes` bounds a line (longer lines are bad lines). The soak check
(`nix build .#checks.<system>.pg-claude-code-recorder-soak -L`, on demand) fails at 1.5 times the
committed baseline.

## Remaining sources, links, the change feed and the Go client (phase 3)

Six more sources are compiled in: `claude-subagent-meta` (`subagents/agent-*.meta.json`, linked to its
`tool_use_id`), `claude-tool-result-body` (`tool-results/*`, linked to its `tool_result` by
`tool_use_id` for `toolu_*` names and by the saved-to path otherwise), `session-registry`
(`sessions/<pid>.json`, one record per version keyed by `registry_key` = pid, `procStart` and content
hash, plus a `registry_deleted` record when the file vanishes), `claude-status` (append),
`session-mode` and `session-compaction`. A snapshot source writes one record per stored version
(generation = version); the default query shows the newest version, `old_generations` all of them.
`query` follows `parent`, `result`, `call`, `body`, `subagents`, `status`, `mode`, `registry` and
`compaction`.

**Rename history of a session.** The registry file holds only the current `name`, but every version of it
is kept, so a rename is a later version with a different `name`. The default query (and
`pg-claude-code-recorder processes --session ID`, which shows the newest version) therefore shows only the
latest name; add `old_generations` to list every version, oldest first:

```text
echo '{"where":{"all":[{"field":"source","eq":"session-registry"},{"field":"session","eq":"<session id>"}]},"old_generations":true,"select":["time","name","name_source","name_since","status"],"order":[{"by":"time"}]}' | pg-claude-code-recorder query
```

One row is one stored version, so consecutive rows with the same `name` are status changes, not renames;
a rename is a row whose `name` (and usually `name_since`) differs from the row before it. This query is
pinned by `TestRegistryRenameHistoryQuery` in `internal/serve/sources_test.go`.

A tool-results body links back to the `tool_result` that names it through stored fields only: the
transcript parser stores the saved-to path a `tool_result` preview names as `saved_to`, and the link
matches it to the body's `rel_path`. No full-text index is read.

Operations beyond `query` and `search`: `hello` (`GET /v1/hello`: `protocol_version`,
`schema_version`, `epoch`, `capabilities`, `caught_up`, `degraded`), `feed`, `snapshot` and `raw`.

- **Cursor** `<epoch>:<sequence>`, opaque. A record's sequence is twice its id; a reset (a re-parse or
  a new generation) is its own entry just before the records it brings. `feed` takes `after`, `where`,
  `project`, `wait_ms` (at most 60,000; `wait.max`) and `limit` (default 1,000, at most 10,000) and
  answers `entries`, `next_cursor` (advanced even when nothing matched), `more` and `timed_out`.
  A reset is delivered to every feed whose filter could match any record of that file (judged from the
  file's source and record types).
- **cursor_expired** (HTTP 410, exit 7) carries `cursor`, the head to resync from: the cursor is from
  an old epoch (a rebuild bumps it), older than `feed.retention` (default 14 days of commit time, kept
  as `feed_marks` rows written with each parse commit), or beyond the head. Resync with `snapshot`:
  pages of the state at a cursor (pass the first answer's `cursor` as `at` and `next_page` as `page`),
  then tail `feed` after that cursor. Resets are never pruned, so the feed after a snapshot always
  carries the remaining ones.
- **Waiting**: the daemon admits `wait.maxWaiters` (default 256) waiters, then answers `busy` (429);
  a waiter wakes on a parse commit and answers only when a committed record matches its filter.
  `serve` closes waiting connections on shutdown, so a client reconnects with the same cursor.
- **Read deadline**: every read (a socket query and every read-pool statement) runs under the deadline
  `query.timeout` (default 30s), so no reader pins the WAL snapshot longer; a read transaction is never
  held while a waiter waits (it reads, releases, waits, then reads again). A read that hits the
  deadline answers `query_timeout` (504, exit 6, `client.ErrQueryTimeout`, retryable) and counts in
  `query_timeouts_total{op}`; it also logs one `query_timeout` with `request_id`, `query_kind`, `args` (the key
  arguments, credential-named keys omitted), `wait_ms` (elapsed time not spent running in SQLite: the wait for a
  read slot plus the handler's own work) and `exec_ms` (time statements and transactions ran).
- **Read connection memory and batching**: read connections use a larger SQLite page cache and a bounded memory map, because every fresh connection
  to a file of tens of gigabytes starts cold: `query.readCacheMiB` (default 64) is the per-connection
  `cache_size` ceiling and `query.readMmapMiB` (default 512, 0 disables) the per-connection `mmap_size`
  ceiling. Read connections are recycled rarely (6 hours, 30 minutes idle) so the cache survives. A
  multi-statement read (coverage, the settle pass over settled Conversations, the lifecycle load) runs in one
  read transaction: one WAL read-lock round trip and one snapshot instead of one transaction per statement.
- **Filter field census**: each query, search or feed request whose filter compiled counts the catalogued
  field names its filter leaves named in `query_filter_fields_total{op,field}` (op is query, search or
  feed) and logs one `query_filter_fields` info line (`op`, `fields` as `name=count,...`). Names and
  counts only, never values or query text, so the label set is bounded by the catalog. It tells which
  stored fields are never filtered on and could be dropped or read on demand from the copies.
- **raw** answers a record (`{"record":{file,gen,offset,item}}`) or a file range. Every reader of
  stored bytes (raw, snippets, substring verification, field reads) goes through
  `Store.OpenPointer`, one abstraction over a copy, a snapshot version and an archive.
- **Rebuild**: `serve` rebuilds `index.db` itself when it is missing (journal first, then a re-parse);
  `rebuild` (daemon stopped) or `serve --rebuild` forces it and sets the old index aside as
  `index.db.pre-rebuild`. A rebuild bumps the epoch, counts `index_rebuilds_total` and logs
  `index_rebuilt`. A binary that meets a newer `schema_version` refuses to start and says so;
  `--rebuild` is the way past it. An OLDER `schema_version` (1, before field names were interned) is
  refused the same way, untouched and not migrated in place: stop the daemon and run `rebuild` (or
  `serve --rebuild`), which re-derives records, fields and text indexes from the journal and the
  copies into a new file and keeps the old one as `index.db.pre-rebuild`; nothing is deleted before
  the operator has checked the new file. **Deploying `schema_version` 4** (which includes the version 3
  layout below): a version 4 binary refuses an `index.db` of version 3 or older the same way, so run the one
  rebuild below once, to version 4. **Deploying `schema_version` 3**: the first start of a version 3
  binary over a version 2 `index.db` fails with that message and the daemon stays down until the operator
  stops it and runs `pg-claude-code-recorder rebuild` (bulk mode, the daemon stopped). Before it moves
  anything the rebuild checks the free space of the state directory: it needs 85 percent of the size of
  `index.db` plus its WAL (the estimated new file) plus 1 GiB for the bulk WAL, and the old file stays on
  disk beside the new one as `index.db.pre-rebuild`. Below that it refuses with an error naming the free
  and the needed bytes, logs `rebuild_refused_low_space`, counts `rebuild_refused_total`, and leaves the
  old `index.db` where it was; free the space and run it again. The rebuild takes hours (the parse ran at
  2 to 3 MB/s on the measured fixture): leave the machine running and plan for the daemon to be down for
  that window; the `pre-rebuild` file can be deleted once the new index has been checked, and rolling back is
  restoring it with the version 2 binary. The `rebuild` command (not `serve --rebuild`) runs in bulk
  mode: one write transaction spans many files and commits at 100,000 records or 64 MiB of parsed
  copy bytes, with `synchronous=OFF`, a 256 MiB page cache, a 1 GiB WAL autocheckpoint and FTS5
  automerge off; at the end it restores `synchronous=FULL` and the default automerge, merges the FTS5
  segments and runs `wal_checkpoint(TRUNCATE)`. A rebuild is repeatable, so a crash in the middle
  may leave an unusable half file: a marker row (`meta.rebuild_unfinished`) is written durably first
  and cleared last, and `serve` (and every other start) refuses a file that still has it, telling
  the operator to run `rebuild` again. The `rebuild` command logs a `rebuild_progress` line every 30
  seconds (`--progress-interval` changes it) with `records_parsed`, `records_per_sec` and
  `commits_per_sec` over the interval, `files_done` of `files_total`, and the byte sizes of `index.db`
  and its WAL, then a `rebuild_summary` with the totals and `wall_sec`, and after the finish a
  `rebuild_profile` line with the wall time, `parse_sec`, `records_per_sec`, the time spent committing
  batches, building the deferred indexes, optimizing FTS5 and checkpointing, and the byte sizes of the new
  and the `pre-rebuild` file; the same numbers are stored as JSON in `meta.last_rebuild_profile` of the new
  `index.db`. These are log fields and a meta row only (the command opens no metrics endpoint). `feed_waiters` is the gauge of waiters in progress.

```sh
pg-claude-code-recorder snapshot --all --where '{"field":"type","eq":"assistant"}'
pg-claude-code-recorder feed --after 3:918431 --wait 30s --where '{"field":"type","eq":"assistant"}'
pg-claude-code-recorder raw --record f_5:1:0:-1
```

### The Go client

`client` (outside `internal/`, so other workspace modules import it) has `Dial(ctx, Options)`,
`Hello`, `Query`, `Search`, `Feed(ctx, filter, after)` (an iterator: it applies a reset before
de-duplicating by record id, backs off on busy and unavailable from 1 s doubling to 30 s, and
reconnects with the same cursor after a daemon restart; persist `Cursor()`), `Snapshot` and `Raw`,
and the typed errors `ErrDown`, `ErrBehind`, `ErrBusy`, `ErrQueryTimeout`, `ErrUnavailable`, `ErrBadRequest`,
`ErrNotFound` and `ErrCursorExpired` (match with `errors.Is`; `*client.Error.Cursor` is the resync
cursor). The socket is `PG_CLAUDE_CODE_RECORDER_SOCKET`, else `$XDG_STATE_HOME/pg-claude-code-recorder/recorder.sock`;
connecting has a timeout. A higher `protocol_version` than the client knows is refused (`ErrProtocol`).
`client/clienttest` starts a real daemon in process over throwaway directories for tests of packages
that use the client (`Options.Procs`-style seams: `Env.Procs` is the fake process table). `Conversation(id)`,
`Process(pid, procStart)`, `ConversationList`, `ProcessList`, `EndLabel(evidence)`, `Ended(EndQuery)` and
`Crashed(since)` are described under Conversation and Process facts below.

The wire-contract suite is `client/contract_test.go` (in process, over the socket); the
write-to-feed latency test is `client/latency_integration_test.go` (tag `integration`, logs its
measurement, enforces a 3 s bound).

## Conversation and Process facts, end evidence and the crash query (phase 4)

The recorder stores **evidence**, never a label (ADR 0015). Each Process is a pid plus its start time
(`procStart`); the `procs` table (written through the journal, so a rebuild replays it) holds what no
file records: first seen, last seen alive, the exit instant, whether the registry file was still
present at that instant, `registry_stale_at_discovery` and the host binding. Everything else is a query
over the parsed records: every registry version and its deletion time (`session-registry`), `/exit` and
`/clear` entries (`claude-history`'s `slash_command` field, parsed only up to the last newline, so a torn
`/exit` counts as no entry until its newline arrives) and the control mod's end files
(`claude-control-end`, `<controlStateDir>/ends/{session}.{ms}.json`, schema
`claude-marketplace/pg-claude-control/schema/session-end.schema.json`).

- **Exit watch**: a kqueue `NOTE_EXIT` watch on darwin (`internal/proc`), a one-second liveness poll that
  checks the start time elsewhere. A Process is watched from the first time its registry file is seen;
  after a restart the watch is registered again for every known Process that is still alive. Registering
  on an already exited pid fails (ESRCH): that is the recorded fact, an exit the recorder did not observe,
  and the Conversation is `unknown`. The presence of the registry file is one `lstat` right after the
  event; the **grace-window fallback** (`process.grace`, default off; `process.graceWindow`, default
  1s, used only when it is on) reads a file still present again after the window. It is off because the
  registry-versus-exit order was measured (pg2-8wt1uz, 2026-10-30): on `/exit`, Ctrl-D, SIGTERM, SIGHUP and a
  closed terminal Claude deletes the file 0.8 to 1.9 s before the pid exits (15 of 15 runs; 3 runs per path, one
  Claude version, one machine), and only SIGKILL leaves it, removed by a later Claude startup, not on a timer. The
  option stays for a Claude version where the order changes.
- **Stale at discovery**: a registry file whose pid is gone, or whose start time differs from `procStart`
  (a reused pid is dead), at the Process's _first_ discovery sets `registry_stale_at_discovery` (counter
  `registry_stale_at_discovery_total`; the startup log event `startup_stale_registries{n}` after the first
  sweep). A second discovery sets nothing.
- **Host binding**, read once at the Process's start with the KERN_PROCARGS2 sysctl (not `ps`): cmux
  surface and workspace ids, tmux socket plus pane id, otherwise `TERM_PROGRAM`.
- **Control inbox**: `process.list` carries `control_inbox` (`<pid>.fifo` present); gauges
  `control_inbox_state{state=present|gone|never_loaded}` and `processes_without_control_inbox`. The
  recorder removes `<pid>.fifo` when its Process exits and sweeps at startup for FIFOs whose pid is not a
  live Process with a matching start time (its first write into the control state directory), and removes an
  end file once it has been ingested and a day has passed (its second). It also reads
  `<controlStateDir>/requests.jsonl` (a read beyond the `ends/` root): `control_requests_total{verb,outcome}`
  counts each line once (keyed by ts, id and verb; polled every `process.livenessInterval`, default 5s;
  an unknown verb or outcome is `other`; a burst of more than 1,000 requests between two polls is
  undercounted, because the mod keeps the last 1,000). `end_reason_source_files_total{outcome}` counts end
  files `ingested`, `invalid` (by the schema) and `removed`; `conversation_end_total{registry_present,
exit_observed,clear_entry,exit_entry}` counts evidence classes (never labels).

Operations (the evidence list is shared): `conversation.show`, `conversation.list` (any evidence field is
a filter, and a filter returns a superset of its label's rows), `process.show` and `process.list`.
A Conversation is **open** while a live Process holds it (stage `live`) and **closed** otherwise (stage
`quiet` in this build; settle owns the rest of the stage machine). A session a Process left by an
in-process `/resume` has no end and is omitted by the crash query.

**Labels** are derived by `client.EndLabel(evidence)`, first match wins: `/clear` entry with a new session
in the registry is `cleared`; a typed `/exit` after the Process started is `exited`; an exit the recorder
did not observe is `unknown`; the registry file still present at the exit is `crashed`; otherwise
`closed` (normally). The mod's `end_reason` is detail only. The label table is
`client/testdata/labels.json`, loaded by the daemon test (`internal/serve/labels_integration_test.go`), the client test
(`client/labels_test.go`) and the command test (`cmd/pg-claude-code-recorder/labels_integration_test.go`); each calls
`clienttest.Covered`, and a guard fails when a consumer stops loading every row.

```text
pg-claude-code-recorder conversations --crashed --since 1d
pg-claude-code-recorder conversations --suspected-crash      # crashed plus unknown rows with a stale registry file
pg-claude-code-recorder conversations --ended exited --cwd /example/work --json
pg-claude-code-recorder processes --live
```

`/clear` probe (a first task of this phase): on 2026-10-30, 79 of 79 `/clear` entries of a real
`history.jsonl` whose transcript was present were stored under the **old** session id (the transcript of
that id has no record after the entry), so the history join is by the Conversation that was cleared.

Tests: `go test ./...` (unit); the real-child tests (`internal/proc/real_integration_test.go`,
`internal/serve/realchild_integration_test.go`, tag `integration`) run a real kqueue watch against a
real child process.

## Removal, extract, held, where and doctor (phase 6)

Removal takes a settled Conversation's files out of the Claude directory once it has been quiet for
`remove.after` (30 days). It is **off by default** (`remove.enable = false`), and there is **no undo
except `extract`**. Keys (all in `config.toml`): `remove.enable`, `remove.after`, `remove.maxPerRun` (50),
`remove.maxPerHour` (200), `remove.dueShare` (0.10) and `remove.dueEvery` (30 days; see "When removal is due" below), `held.recheck` and `doctor.interval` (24h; doctor also runs from a timer
inside the daemon, so no external scheduler is needed). The doctor keys are described under "Doctor" below. Removal never judges the parse: the only gate on the bytes is the full-hash match between the original and the archive (a mismatch starts a new generation and removes nothing). A wrong parse is raised by the `bad-line-rate` and `unknown-key-burst` alerts instead, and fixed by re-parsing the archive, so no format hold stands in the way of removal.

The order, so a crash at any point loses nothing: re-run the check and compare each Claude file's full hash
with the archive (in clone mode the archive is first written and verified from the clone); commit "removal
pending" with each file's inode, size and hash; per file, require no registry file naming the session and no
live Process, rename it into `.pg-recorder-quarantine` (a directory beside the file, on the block list and the
watcher filter), list the holders of the quarantined inode (a holder restores the file), re-hash it, flush the
archive with `F_FULLFSYNC`, re-check size and modification time and unlink; then commit "removed" and tidy the
empty directories. A restart scans the quarantine, maps entries to the pending list by inode and hash, unlinks
a matching entry with no new Process and restores any other; an unlisted entry is logged as
`quarantine_orphan_found_on_start` and left. A restore is an exclusive link: a new file at the path is never
overwritten, both are kept and the Conversation is held with reason `restore_conflict`. The residual window is
a write between the final re-check and the unlink by a holder the listing missed.

```text
pg-claude-code-recorder remove --dry-run                 # works while removal is disabled; says so first
pg-claude-code-recorder where <session>                  # stage, blocked_by, next_eligible, clear
pg-claude-code-recorder held [--retry]                   # reason codes with a remedy line each
pg-claude-code-recorder extract <session> [--to DIR]     # byte-exact, mtime now; prints the claude --resume line
pg-claude-code-recorder doctor                           # exit 9 on a mismatch; writes doctor.json
```

### Doctor

`doctor` verifies every copy against its original by full hash at least once per `doctor.interval` plus one cycle. It does
this in byte-budgeted slices that do not hold the copier lock across files: each slice hashes at most
`doctor.bytes_per_slice` (256 MiB) of the copy, one slice runs every `doctor.slice_every` (1m, at least 1s), and a cursor
file in the state directory carries the position and the mismatches found so far, so a restart loses nothing.
**The byte budget counts bytes of one side, the copy**: the original is read as well, so the disk reads of a slice are about
twice the budget. A file or copy that was reclaimed between the snapshot and the hash is requeued once and then dropped
(`doctor_slices_total{outcome="skipped_gone"}`); a mismatch is always re-hashed under the lock before it is repaired. A
slice deferred because the copy is lagging counts as `deferred` and stretches the cycle. `doctor.bytes_per_slice = 0`
selects the old synchronous doctor.

At start the daemon projects the cycle length (copy bytes over `doctor.bytes_per_slice`, rounded up, times
`doctor.slice_every`; `doctor_projected_cycle_seconds`) and refuses to start when it exceeds `doctor.interval`, unless
`doctor.allow_slow_cycle = true`. The CLI `doctor` runs inside the daemon: a run during a rolling cycle completes the remaining
work synchronously and prints the cycle state when it is mid-way.

`doctor_last_run_timestamp` is the end of the last completed cycle, not of a slice. `doctor_mismatch_files` is updated after
every slice, so a find is visible within minutes and the `doctor-mismatch` alert does not wait for the cycle end; `doctor.json`
is written at the cycle end. Signals: `doctor_cycle_progress_ratio`, `doctor_cycle_duration_seconds`,
`doctor_cursor_age_seconds`, `doctor_projected_cycle_seconds`, `doctor_slice_duration_seconds` (a plain histogram),
`doctor_slices_total{outcome}` (ok, deferred, error, skipped_gone) and `doctor_race_skips_total`; the dashboard panels are
"doctor cycle progress" and "doctor slice duration".

Removal is run by the operator first: run `remove --all-eligible --yes` on a roughly fixed period (the daemon removes nothing on its own while `remove.enable` is false, the default).
Readiness checklist for the later opt-in of automatic removal: the daemon-down alert is registered (phase 7c); `doctor` has
completed a rolling cycle over every copy (its last completed cycle is recent); pa-monitor `sessions` and ccpool resume
have moved to the recorder; the state directory is backed up (excluding `recorder.sock` and temporary files);
`remove --dry-run` prints what would go and how many GB.

Signals: `quarantine_files`, `quarantine_bytes`, `quarantine_oldest_age_seconds`,
`removal_quarantine_mismatch_total`, `removal_backlog_conversations`, `removal_backlog_bytes`, `removal_quiet_share`, `removal_last_run_timestamp`, `removal_due`,
`conversations_removed_total`, `skipped_files_total{reason}`, `dropped_bytes_total{reason="removal_race"}`,
`doctor_last_run_timestamp` and `doctor_mismatch_files` (from `doctor.json`); events `quarantined`,
`quarantine_restored`, `quarantine_orphan_found_on_start`. The alert rules are phase 7c.

### Durability in normal running, group commit and the removal barrier

`index.db` runs at `synchronous=NORMAL` in WAL mode: a process crash loses nothing and an OS crash or power
loss can only roll back the newest commits, which the journal, the copies and the stored parse offsets
re-derive. The parse worker coalesces wake-ups (`Parser.Coalesce`, a 300 ms quiet window capped at 5 s) and
`Drain` then parses every dirty file in one transaction bounded by 50,000 records or 16 MiB, bumping
`parse_seq` once per transaction. Only the commits a delete depends on (`CommitFull`) are FULL. Before any
original is quarantined, removal runs `Store.Barrier` (journal, TRUNCATE checkpoint under FULL and
`fullfsync`, `index.db` flush) after flushing the archive, and refuses with `durability_barrier` on any
failure. The table-by-table reasoning, the macOS `fullfsync` answer and the benchmark are in
`docs/investigations/2026-11-04-index-durability-and-group-commit.md`. The benchmark is
`go test -tags soak -run '^$' -bench BenchmarkLiveCommit ./internal/parser/`.

The transcript parse benchmark is `go test -run '^$' -bench Parse -benchmem
./internal/source/claudetranscript/` (ns/op and allocs/op are per line, over the five fixture lines in
`testdata/lines.jsonl`; `TestParseMatchesGolden` pins the records those lines yield). Field extraction
reads a per-type plan cached on first use, and unknown-key detection opens only the levels the struct
declares as objects instead of decoding the whole line into a generic tree. Measured on an Apple M3 Pro
(arm64, `-count 5`): before, 43.8 to 51.2 us and 14366 B and 195 allocs per line; after, 26.8 to 33.1 us
and 8500 B and 121 allocs per line.

### Operations that can be blocked for lack of space (audit)

Operator ruling (Phillip, 2026-11-05), verbatim: "i don't want the recording tell me there is low space on the system. it isn't its job. it can alert me that it can't do its own job because insufficient space. ie, if it wasnt to do a archive, but there isn't enough space for the copy and compression, then sure, tell me about that. but a generic alert that there isn't less than 20gb space available is not in the scope of this tool."

The recorder alerts only when one of its own operations is refused or fails because the volume is full: `blocked_no_space_total{op}` and the `blocked_no_space` log event (fields `op`, `needed_bytes`, `had_bytes`, `why`; a figure that cannot be measured reads `unknown`), alert `blocked-no-space`. A host free-space level is never an alert input.

| Operation (`op`)                                         | Where it can fail for space                                                        | Before the audit                                                                                                                                        | Now                                                                                                                                                                                          |
| -------------------------------------------------------- | ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Copy a file into the store (`copy`)                      | the copier's append (`ENOSPC` mid-copy; offset kept, partial bytes trimmed, heals) | `ingest_failed` warning and `ingest_results_total{result="error"}`, space not told apart                                                                | `blocked_no_space_total{op="copy"}` with the free bytes; needed is `unknown`                                                                                                                 |
| Parse and index writes (`parse_index`)                   | the parser transaction and `index.db` commits (SQLite `database or disk is full`)  | `parse_failed` and `parse_all_failed` warnings only; `parse-stuck` fires on lag, cause not named                                                        | `blocked_no_space_total{op="parse_index"}`                                                                                                                                                   |
| Compression and archive segment writes (`archive`)       | `WriteSegmentFile` during settle                                                   | `settle_pass_failed` warning, the pass counted as an error                                                                                              | `blocked_no_space_total{op="archive"}` with free bytes; the Conversation stays unsettled and retries                                                                                         |
| The removal run's durability barrier (`removal_barrier`) | journal, TRUNCATE checkpoint and flush before any original is quarantined          | `removal_barrier_failed` warning, the Conversation held as `durability_barrier`                                                                         | `blocked_no_space_total{op="removal_barrier"}`                                                                                                                                               |
| WAL checkpoint and truncate (`index_checkpoint`)         | `wal_checkpoint_failed` and `wal_truncate_failed`                                  | warnings only                                                                                                                                           | `blocked_no_space_total{op="index_checkpoint"}`                                                                                                                                              |
| The rebuild free-space preflight (`rebuild`)             | `rebuild refused: N bytes free ...`, before anything is moved                      | `rebuild_refused_low_space` event and `rebuild_refused_total`, no alert path (the command runs in its own process and prints the error to the operator) | also `blocked_no_space_total{op="rebuild"}` and `blocked_no_space` with exact needed and had bytes; the operator sees the refusal on the command line, and a daemon scrape does not carry it |

Two figures remain that look like host free-space signals and are NOT alerts: the `state_dir_low_space` gauge with its `state_dir_low` event (the daemon's own low-space mode, which stops backfilling Conversations not yet copied and settles early, keyed on `minFreeBytes`), and the free bytes in `status` and `doctor`. They are the recorder's own behavior switch and a reading, not a page; no alert rule, dashboard panel or doc line reads them as a warning. `copy-lag` names a state directory low on space only as one cause to look at. A refusal that low-space mode causes (a Conversation not backfilled) is not yet counted as a blocked operation: the audit leaves it as is because it is keyed on the host threshold the operator ruled out.

## Signals, alerts and the dashboard (phase 7c)

Files under `grafana/` (the location follows `packages/pg-connector/grafana`):

| File                                  | What it is                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `alerting/alerts.yaml`                | twenty Grafana rules, uid `pg-ccrec-<name>`, registered by `darwin/modules/pg-claude-code-recorder` while a daemon is deployed                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `alerting/alerts-delete.yaml`         | the `deleteRules`-only twin (same uids), registered instead while no daemon is deployed; the two are never loaded together                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `alerting/alerts-retired-delete.yaml` | the retired-uid-only `deleteRules` file (`pg-ccrec-free-space`, `pg-ccrec-removal-backlog`, `pg-ccrec-unknown-key-burst`, `pg-ccrec-bad-line-rate`), registered next to `alerts.yaml` while the daemon is deployed so a removed rule is still deleted from Grafana                                                                                                                                                                                                                                                                                            |
| `alerting/rule-tests/`                | one promtool test file per rule, run through `packages/pg-connector/grafana/check-alert-rules.sh`                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `pg_claude_code_recorder_metrics.txt` | the metric catalogue (the Signals table): name, type, labels, with the enumerated label values                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `pg-claude-code-recorder.json`        | the dashboard: copy and parse lag, Conversations by stage, held age, dropped bytes by reason, removal backlog, Processes without an inbox, request rate by client, settle tick duration, settle tick work, loop runs by outcome, recorder cpu cores, go heap, go goroutines, refresh gauges duration, wal size, loop duration p99 by loop, loop runs by loop and outcome, loop in flight, ingest results, sweep walked vs unchanged candidate, sweep walked vs skipped, lock waits, request duration p99 by op, requests in flight by op, slow requests by op |
| `check-alert-structure.sh`            | structure check S1 to S8 with `--self-test` negative controls (a 41-character uid, a missing `noDataState`, a missing guard, a twin that differs, and more)                                                                                                                                                                                                                                                                                                                                                                                                   |
| `check-crash-share-label-table.sh`    | runs the crash-share PromQL against `client/testdata/labels.json`, the same label table the daemon, client and command tests use                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `check-signals-registration.sh`       | catalogue against the Go `M*` constants, rule uids against `registeredRuleUIDs`, the twin, the dashboard and this section, with `--self-test`                                                                                                                                                                                                                                                                                                                                                                                                                 |

Flake checks (build one with `nix build .#checks.<system>.<name>`): `test-pg-claude-code-recorder-alert-rules`,
`test-pg-claude-code-recorder-alert-structure` and `test-pg-claude-code-recorder-signals-registration`. Every rule uid is also in
`registeredRuleUIDs` of `packages/pg-router-probe/cmd/pg-router-probe/run.go` (a deployment that passes its own `--rule-uid` list replaces
that list and must carry the twenty too).

Alerts and their thresholds (the thresholds are the builder's calls; a literal that stands for a config default is named in the rule):

| Alert                 | Fires when                                                                                                                                                                                                                                               | Severity | No data  | First action                                           |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | -------- | ------------------------------------------------------ |
| `daemon-down`         | `up` of the scrape target is 0 for 2 minutes (scrape interval 30s); `SYSTEM_PAUSE` guard                                                                                                                                                                 | critical | Alerting | `status`; the launch agent log                         |
| `heartbeat-stale`     | scraped (`and on() up == 1`) and the last sweep is older than 2700 s (3 times `sweep.interval`)                                                                                                                                                          | warning  | Alerting | `status`; pprof goroutine dump                         |
| `copy-lag`            | copy lag over 120 s for 10 minutes                                                                                                                                                                                                                       | warning  | OK       | `coverage`                                             |
| `parse-stuck`         | parse lag bytes above 0 and the parse progress ratio unchanged for 10 minutes                                                                                                                                                                            | warning  | OK       | `coverage`; the log                                    |
| `held-old`            | oldest held Conversation over 259200 s (the `held.alertAfter` default, 3 days)                                                                                                                                                                           | warning  | OK       | `held`                                                 |
| `format-drift`        | (`bad_lines_total` plus `unknown_keys_total`) over `lines_parsed_total` above 1 percent (the default of the removed `format.holdRate`) over a day, with at least 100 lines parsed, for 15 minutes; replaces `unknown-key-burst` and `bad-line-rate`      |
| `data-loss`           | any increase of `dropped_bytes_total` in an hour (the firing alert carries the `reason`)                                                                                                                                                                 | warning  | OK       | `coverage`; the log                                    |
| `removal-due`         | `removal_due` is 1 for 30 minutes: over `remove.dueShare` (10 percent) of settled Conversations quiet longer than `remove.after`, or quiet ones exist and no removal run completed in `remove.dueEvery` (30 days); replaces `removal-backlog`            | warning  | OK       | `remove --dry-run`, then `remove --all-eligible --yes` |
| `doctor-mismatch`     | `doctor_mismatch_files` above 0, or a non-zero `doctor_last_run_timestamp` older than 2 days                                                                                                                                                             | critical | Alerting | `doctor`                                               |
| `control-inbox-gone`  | a live Process in `control_inbox_state{state="gone"}` for 10 minutes (never `never_loaded`)                                                                                                                                                              | warning  | OK       | `pg-claude-control list`                               |
| `crash-share`         | crashed plus suspected over 20 percent of a day's ends, with at least 20 ends                                                                                                                                                                            | warning  | OK       | `conversations --crashed --since`                      |
| `removal-stuck`       | the oldest quarantined file is over an hour old                                                                                                                                                                                                          | warning  | OK       | `remove --dry-run`; the log                            |
| `settle-tick-slow`    | the last settle tick took over 60 s (the tick interval) for 15 minutes                                                                                                                                                                                   | warning  | OK       | `status`; the log; pprof CPU                           |
| `field-type-mismatch` | any increase of `field_type_mismatch_total` in an hour (the firing alert carries `source`, `type` and `field`)                                                                                                                                           | warning  | OK       | `coverage`; update the catalogue                       |
| `blocked-no-space`    | any increase of `blocked_no_space_total` in 30 minutes (the firing alert carries `op`: `copy`, `parse_index`, `archive`, `removal_barrier`, `index_checkpoint` or `rebuild`); keys on the recorder's own refusal or failure, never on a free-space level | warning  | OK       | the `blocked_no_space` log event; free space           |
| `cpu-high`            | process CPU rate over 10 minutes above 1.0 core for 30 minutes (baseline 0.405 cores average; to be revisited after a day of data)                                                                                                                       | warning  | OK       | `status`; pprof CPU                                    |
| `wal-large`           | `wal_bytes` above 256 MiB (268435456 bytes) for 15 minutes (baseline flat at 24.3 MiB)                                                                                                                                                                   | warning  | OK       | `status`; the log                                      |
| `loop-stalled`        | per loop: a pass running over 600 s, or the last pass ended over its bound ago (`watcher_sweep` 2700 s assuming the default `sweep.interval`, `tracker_*` 120 s, `status_refresh` 60 s; `parser_drain` has no age bound), for 10 minutes                 | warning  | OK       | `status`; pprof goroutine dump                         |
| `request-slow`        | requests over 10 s above 2 percent of all requests in an hour, with at least 50 requests (baseline 1.2 percent)                                                                                                                                          | warning  | OK       | `status`; the `slow_request` log                       |

Grafana has no inhibition, so each guard is in the expression: `daemon-down` adds the `SYSTEM_PAUSE` gate to a value that is never empty (a pause cannot read as NoData); `heartbeat-stale` and
`doctor-mismatch` read 0 while the daemon is not scraped (so `daemon-down` alone pages). The recorder ships no generic free-space alert (it does alert when it cannot do its own job for lack of space, `blocked-no-space`; operator ruling, Phillip, 2026-11-04: "there shouldn't be a pg-claud-code-recorder specific file system alert." and "it's ok to measure specifics about the app, but host level alerts belong elsewhere"); its uid `pg-ccrec-free-space` stays in `alerts-retired-delete.yaml` (loaded with the rules) and `alerts-delete.yaml` so Grafana drops the already-provisioned rule. The daemon still refuses to copy below `minFreeBytes`, and `doctor` and `status` still report the free bytes (read from the filesystem). A `doctor_last_run_timestamp`
of 0 means doctor has not run yet (the daemon first runs it one `doctor.interval` after start): that is not stale, and the removal
readiness checklist treats it as not ready.

`sweep.interval` is accepted from 1 minute to 24 hours: a smaller value is clamped to 1 minute with a
startup WARN (`config_warning`) for one release instead of failing startup, and a larger one is
rejected. The `heartbeat-stale` and `loop-stalled` (`watcher_sweep`) alerts use the literal 2700 s,
which is 3 times the 15 minute default; it is coupled to the default, so when you change
`sweep.interval`, change both the setting and those literals in `grafana/alerting/alerts.yaml`.

Signals added by the second review: `conversation_end_total{registry_present,exit_observed,clear_entry,exit_entry}` (evidence classes,
each `true` or `false`; never a label such as crashed), `registry_stale_at_discovery_total` and the log event `startup_stale_registries{n}`;
`quarantine_files`, `quarantine_bytes`, `quarantine_oldest_age_seconds`, `removal_quarantine_mismatch_total` and the events
`quarantined`, `quarantine_restored`, `quarantine_orphan_found_on_start`; `torn_tail_age_seconds{source}` and the event
`torn_tail_pending`; `doctor_last_run_timestamp` and `doctor_mismatch_files`; `control_inbox_state{state=present|gone|never_loaded}`,
`control_requests_total{verb=submit|interrupt|run|read|ping|other, outcome=ok|rejected|duplicate|refused|other}`,
`end_reason_source_files_total{outcome=ingested|removed|invalid}`, `held_conversations{reason}` and `skipped_files_total{reason}`;
`requests_total{client,op,outcome}` where `client` is a whitelisted name and anything else is `other`. No label is per file or per
session. The catalogue file lists every name with its type and labels.

Two exporter facts the alerts rely on: `bad_lines_total{source}` and `unknown_keys_total{source,type}` are registered at zero for every
configured source (and, for the second, every known record type plus `other`) at start, so a first burst reads as an increase from 0;
and `lines_parsed_total` (every line handed to the parser, stored or bad, with no label) is the denominator of the `format-drift` alert; `unknown_keys_total` counts first-seen distinct (source, type, key) tuples, not occurrences (the per-occurrence totals stay in the
`unknown_keys` and `unknown_keys_total` tables and `coverage`), so a backfill of known keys does not move it.

### Apply and restart

`pn workspace apply` replaces the daemon, and the old daemon's `Close` runs `PRAGMA wal_checkpoint(TRUNCATE)` with
no timeout or progress log. With a very large WAL (several GB) that can take minutes. During it the daemon serves
no requests, so `conversation.show` clients see a stall or the daemon-offline error (not `not_found`), the
`daemon-down` alert can page after 2 minutes (expect one page or silence it), and the disk is low, so watch free
space. Do not kill the daemon: a SIGKILL leaves the WAL to be replayed on the next open. The checkpoint is done when
`status` answers again and `wal_bytes` drops; the `wal_truncate_done` event (with `duration_ms`) says so in the log.
The periodic path avoids that stall: when `wal.truncate_above` (512 MiB; 0 disables it) is set and the WAL file is over it,
the backfill has finished (`caught_up` is 1), none was tried in the last hour and a fresh passive checkpoint just taken
reached every frame, the checkpoint loop runs one TRUNCATE checkpoint. Its outcome is `wal_truncate_total{outcome}`
(ok, busy, error) with `wal_truncate_duration_seconds`, and the `wal_truncate_start` and `wal_truncate_done` events carry
`source=periodic`. The `wal-large` alert fires at 1 GiB, twice the default `wal.truncate_above`.
The first apply after that change pays this once, and a rollback (revert the commit and apply again) pays it again.
Launchd settings (`KeepAlive`, `ExitTimeOut`) live outside this package and were not checked. The operator runs the
apply.

### Clone re-sharing on settle (APFS)

In clone mode the copier seeds a copy with `clonefile`, and an append then gives that copy private blocks. The settle
step re-clones the copy from the Claude original, which shares those blocks again. The `clone_private_*` series
(`clone_private_size_max_bytes`, `clone_private_size_total_bytes`, `clone_private_files_total`) are set only when the
copier creates a clone and are cumulative since daemon start, so a settle never moves them; there is no
`clone_private_size_bytes`. The settle measurement is:

- `settle_clone_private_before_bytes_total` and `settle_clone_private_after_bytes_total`: cumulative since daemon start,
  the `ATTR_CMNEXT_PRIVATESIZE` of each preserved copy just before the settle re-clone replaced it, and of the clone that
  replaced it. A re-share is `rate(before) - rate(after)` (or the difference of the two totals across a settle); after
  is about 0 on APFS. Neither moves on Linux, in byte mode, or when the private size cannot be read.
- the `settle_clone_private` log event, one per file settled: `session`, `file`, `before_bytes`, `after_bytes`. Use it to
  repeat the live check on one Conversation: `rg settle_clone_private <recorder log>` after its settle, and expect
  `after_bytes` far below `before_bytes` for a file the copier appended to.

A unit test (fake clone policy) holds the counters and the event; an integration-tier test on real APFS
(`go test -tags integration -run Clonefile ./internal/settle`) holds that the real private size drops to 0 and that the
counters agree with it.

### Settle cadence

`settle.mode` picks the loop. `scan` (the default) runs the whole-store settle tick every `settle.tick` (default 5 minutes, at least 1 second; the
settle age is a day, so a stage change is late by at most one tick, and the default matches the 5 minute removal-gauge
cadence below; it was a fixed minute before and nothing downstream needs minute resolution). `event` keeps a dirty
set (sessions the copier or parser changed) and a due-time queue (the moment each quiet, held or removable session next
needs a decision), and sleeps until the earliest of them, never waking twice within `settle.min_wake` (default 5 s).
A full reconciliation scan, the same tick `scan` mode runs, runs at start and every `settle.reconcile` (default 1 h,
at least 1 minute): a missed event delays a settle by at most that interval and never loses it. Both loops must reach the
same decisions; an equivalence test holds that. `settle.mode` is read at start, so it changes only with a daemon
restart. A wake waits for a running reconcile (the settler's run lock), which is hourly and takes up to a tick's
duration. In `event` mode `settle_tick_duration_seconds` is the duration of a wake or a reconcile and
`settle_tick_examined_total` counts sessions processed per wake.

### files.last_copy_ns: the last-seen signal of an append-mode file

`files.last_copy_ns` is the recorder time of a file's newest copy commit. Applying a journal commit record sets it
(`MAX` of the old value and the record's time, in the same statement that sets `last_size`), so it is derived from the
journal: a replay reproduces it, and the doctor dump compares it as a `files` column. The settler's idle decision and
the removal backlog gauge (`LastSeenByFile`, and `Settler.lastSeen` for one file) take the newest of it, the file's
snapshot versions and tombstones, with the file's first-seen time as the floor, and no longer read the `commits`
table. It matters only after the original is gone (while the original exists its mtime decides). An append-mode file
has no snapshots, so without this column an actively copied file would look idle once its original was removed.

How an existing database gets the column (no schema version bump): `Open` runs `ALTER TABLE files ADD COLUMN
last_copy_ns INTEGER NOT NULL DEFAULT 0` when the column is missing and, in the same step, fills it once from
`SELECT file_id, MAX(seen_ns) FROM commits GROUP BY file_id`. A bump would instead force a full rebuild of a multi-GB
database for a column one aggregate can fill; measured on the live 13 GB database (106,500 commits over 54,900 files,
90,602 files rows) the aggregate takes 44 ms. The backfill value equals what a replay computes, so a migrated and a
rebuilt database agree. A file with no commit keeps 0, and the backfill never runs again once the column exists, so
dropping the `commits` table (see below) leaves it untouched.

### The commits table is gone

The index no longer stores one row per copy commit. The apply step for a journal commit record still advances
`generations.copy_offset`, `tail_hash`, `tail_len`, `files.last_size` and `files.last_copy_ns`, and no longer inserts
anything else. The journal keeps every commit record, so a rebuild loses nothing, and `Dump` (hence the doctor's
journal-versus-index check) no longer lists the table. No schema version bump is needed: a new database never creates
it, and `Open` never drops it. A database that still holds the table keeps it untouched and ignores it; the table
only wastes space, and the operator drops it once with `DROP TABLE commits` (then a `VACUUM`, or just let the freed pages
be reused). `addLastCopyColumn` still reads the table once when it adds `last_copy_ns` to a database that predates the
column, and does nothing when the table is absent.

### Removal gauges are approximate

`removal_backlog_conversations`, `removal_backlog_bytes` and `removal_quiet_share` are computed from what the event-driven flow already stored:
the newest last-seen time of a Conversation's files (`files.last_copy_ns`, snapshots, tombstones, first seen) and the bytes the
database holds for them (copy offset or snapshot size), in two batched statements however many Conversations there are.
They never stat an original file and refresh at most every 5 minutes (or right after a removal, a restore or a doctor
run). The original's modification time can be newer than the last-seen time (a change not ingested yet), so a
Conversation can count as ready slightly early or late. `remove` stays exact: it checks every Conversation's original
at removal time and refuses one modified recently, whatever the gauge says.

### A conversation did not settle when expected

1. Run `pg-claude-code-recorder where <session>` for the session's stage and, when held, the reason.
2. Check `settle.after` (default 24 h): every file of the Conversation must have been quiet that long, counted from the
   original file's modification time, and a live Process keeps it from settling.
3. In `event` mode, wait for the next reconcile (at most `settle.reconcile`): it re-evaluates every session. A session
   that settles only then points at a missed event, so keep the log lines.

### Recorder CPU is high: what to look at

1. Open the dashboard panel `recorder cpu cores` (CPU in cores, with the GC pause share as a second series).
2. Open `settle tick duration` and `loop runs by outcome`: a p99 near or over 60 s, or a loop with `error` runs,
   points at the settle tick.
3. `rg settle_tick` in the recorder log: `examined`, `unchanged`, `transitions` and `duration_ms` per tick.
4. Take a 30 s profile with `scripts/profile-cpu.sh --sock SOCK --seconds 30` (pprof is on in the deployed
   configuration).
5. Compare with `wal size`: a WAL that keeps growing makes every read slower. Do not leave `status --watch` open
   while profiling.

### Which loop is slow

1. Open the dashboard panel `loop duration p99 by loop`: the loop whose p99 is high (or climbing) is the suspect.
   `loop runs by loop and outcome` shows whether it is also failing, and `loop in flight` whether a pass is stuck
   running now. `removal_pass` is nested inside `settle_tick`, so a slow `settle_tick` with a slow `removal_pass` is
   one cause, not two.
2. Run `pg-claude-code-recorder status` and read the loops table: per loop, the last pass duration, its age, and a
   `STALLED` marker when the age or the running pass is over the loop's bound (600 s for a running pass; 120 s age for
   `tracker_tick` and `tracker_poll`, 60 s for `status_refresh`, 3 times `sweep.interval` for `watcher_sweep`).
3. `rg <loop name>` in the recorder log for that loop's lines (for the settle tick, `settle_tick` and
   `settle_tick_slow`), which carry `duration_ms`. Then take a profile as in the CPU steps above.

### Which request is slow

1. Open `request duration p99 by op`: the op whose p99 is high. `requests in flight by op` shows whether one is stuck
   now and `slow requests (over 10 s) by op` how many crossed 10 s in the last hour.
2. `rg slow_request` in the recorder log: each line over 10 s names the `op`, the `client` and `ms`, at most one per op
   per interval, so a burst shows as the first line plus the counter.
3. Compare with `lock waits` (a request waiting on the store write lock or the copier lock) and `wal size`.

### Registry lock cost per ingest

The metrics registry is one mutex, and every counter add, gauge set and histogram observation takes it once. A copying
ingest takes five acquisitions: three from the copier's own lag gauges and lag histogram, one from the loop duration
histogram and one from the ingest result counter. The last two are the instrumentation the observability work added,
and a test holds that share to at most two. The total is left as it is until the mutex shows as contended: a high
`lock waits` figure for the copier alongside a busy ingest rate, or a scrape that stalls ingest. The fix then is one
batched registry write per ingest (a single acquisition for all of an ingest's writes); per-series atomics are the
larger change to consider only if batching is not enough. The decision is recorded in the phase 1 plan's ground rules.

### Turning on `remove.enable`

**Operator-run removal comes first.** Start by running `pg-claude-code-recorder remove --all-eligible --yes` yourself on a roughly fixed period (preview with `remove --dry-run`); the daemon does not remove on its own. `remove.enable` (automatic removal) stays `false` by default and is a later opt-in, not the first step. The recorder does not judge host disk space and has no host free-space alert; it alerts only when it cannot do its own job for lack of space.

**There is no undo other than `extract`.** Once `remove.enable` is true, the recorder removes the Claude files of Conversations settled
and quiet for `remove.after`; resuming a removed Conversation needs an `extract`, and so the recorder. This section is written only
because the `daemon-down` alert is registered (phase 7c): turn automatic removal on only while it is live
(`darwin/modules/pg-claude-code-recorder` registers `alerts.yaml` while a daemon is deployed).

Readiness checklist, all of it before setting `remove.enable = true`:

1. The `daemon-down` alert is registered and not firing (and `doctor-mismatch`, `data-loss` and `removal-stuck` are quiet).
2. `doctor` has completed a rolling cycle that verified every copy against the Claude files by full hash, and that cycle is recent:
   `doctor_last_run_timestamp` (the end of the last completed cycle) is
   non-zero and under 2 days old (`doctor.interval` plus one cycle) (a stale doctor, or one that never ran, is not ready) with `doctor_mismatch_files` at 0.
3. The clients have moved: pa-monitor `sessions` and ccpool resume read the recorder, not the Claude directory.
4. The state directory is backed up (excluding `recorder.sock` and temporary files).
5. `pg-claude-code-recorder remove --dry-run` prints what would go and how many GB, and that is what you expect.

`remove.maxPerRun` and `remove.maxPerHour` spread the backlog; `removal_backlog_conversations` shows it.

### When removal is due

Removal is an operator-run command on a roughly fixed period, so the daemon says when it is time to run it. Three gauges:
`removal_quiet_share` (the settled Conversations quiet longer than `remove.after`, the ones `removal_backlog_conversations`
counts, as a share of all settled Conversations, 0 to 1), `removal_last_run_timestamp` (Unix time the last removal run
completed, 0 when none has; kept in `removal.json` of the state directory so a restart keeps it; an operator `remove --yes`
that completes counts even if it removed nothing, a dry run does not) and `removal_due`. `removal_due` is 1 when the share is
over `remove.dueShare` (default 0.10, 10 percent) or, with at least one quiet Conversation, no removal run has completed within
`remove.dueEvery` (default 30 days; a run that never completed counts as older than that). The "quiet for Y days" of the
operator's X-percent rule is `remove.after` (default 30 days), the same age removal itself uses, so a Conversation counted as
quiet is one removal would take. The alert `removal-due` fires when `removal_due` is 1 for 30 minutes and names the next step:
`remove --dry-run`, then `remove --all-eligible --yes`. The thresholds are daemon config keys, not literals in the rule.
