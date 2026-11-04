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

### Linux: the inotify watch limit

On Linux the watcher uses inotify (`CGO_ENABLED=0`, standard library only) with one watch per
directory, and the sweep is a plain directory walk. inotify keeps no history, so every start
sweeps. The kernel caps watches per user with `fs.inotify.max_user_watches`; the Claude
directory needs roughly one watch per directory (about 13,000 on a large tree), so raise the
limit above the directory count, for example `sysctl fs.inotify.max_user_watches=524288`. When
the limit is hit, adding a watch fails with `ENOSPC` and `Start` returns that error. An inotify
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
  `state_free_bytes`, `state_used_bytes`, `requests_total{client,op,outcome}`,
  `query_duration_seconds`, `feed_latency_seconds`, `wait_duration_seconds`, `backfill_files_*`,
  `caught_up`, and the loop and process series: `settle_tick_duration_seconds`,
  `settle_tick_examined_total`, `settle_tick_transitions_total`, `settle_tick_last_duration_seconds`,
  `settle_tick_last_timestamp_seconds`, `refresh_gauges_duration_seconds`, `loop_runs_total{loop,outcome}`,
  `process_cpu_seconds_total`, `go_goroutines`, `go_memstats_heap_alloc_bytes`,
  `go_memstats_alloc_bytes_total`, `go_gc_pause_seconds_total`, `wal_bytes` and the database/sql pool figures `db_open_connections`,
  `db_in_use_connections`, `db_idle_connections`, `db_wait_total`, `db_wait_seconds_total`,
  `db_max_lifetime_closed_total` and `db_max_idle_closed_total` (label `pool`: `read` or `write`; a rising
  `db_wait_seconds_total` means callers queue for a connection). The `client` label is whitelisted (`clients` in the config adds names); any other
  name is `other`. The exporter is a small text renderer over the in-process registry, so the
  package takes no Prometheus client dependency.
- Log: JSONL on standard error (or `logFile`), service `pg-claude-code-recorder`, fields `ts`,
  `level`, `event`, and where they apply `file_id`, `session`, `generation`, `offset`,
  `parser_version`, `cursor`, `stage`. Each request logs `request` with the caller's `--client`. Each settle tick
  logs `settle_tick` (`outcome`, `duration_ms`, `examined`, `unchanged`, `settled_now`, `transitions`) and a tick
  over one minute also logs `settle_tick_slow`.
- Known limitation: `status` `stage_counts` omits `live` and `quiet`, while the dashboard and the metrics include
  them (phase 1). `status --watch` runs an O(files) loop on the daemon every 2 s, so do not leave it open while
  profiling.

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

## Conventions for later packets

- External commands go through `github.com/phillipgreenii/x`'s `cmdrun`, as `pg-router-probe` does.
- The pprof socket follows pg-connector's option (`$XDG_STATE_HOME/<instance>/pprof.sock`), not
  `stateDir`, so the profiler socket never shares a directory with the data store.
- `logs.maxBytes` does not exist: the shared hourly rotator that `manageLogs` enables rotates
  the logs; the daemon does not rotate them itself.
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
`parse_resets` marker. Only `text` fields (FTS5 word index) and `opaque` fields (FTS5 trigram index,
case sensitive, every hit verified against the copy) are search-indexed; no file contents are stored.

Typed scalars live in `fields(rec_id, name_id, s, n, f)`, `WITHOUT ROWID`, keyed `(rec_id, name_id)`;
the field name is stored once in `field_names(id, name UNIQUE)` and the query layer resolves a name to
its id (`name_id = (SELECT id FROM field_names WHERE name = ?)`). Invariants: a record has at most one
row per field name; when a record yields the same name twice, the LAST value wins and replaces the
whole row (`s`, `n` and `f` together, never a mix of two values); `fields_name_n` indexes
`(name_id, n)` for numeric filters. A field whose value another table already holds gets no row: `type` and
`session_id` are the record's own columns, and `project` and `file_session` (the path captures) are read back from
the file's path under the Claude directory; an older database that still stores them answers the same. This is
`schema_version` 2.

```sh
echo '{"where":{"field":"type","eq":"assistant"},"group":["model"],"aggregate":[{"fn":"sum","field":"usage.output_tokens","distinct":"message.id","as":"output_tokens"}]}' | pg-claude-code-recorder query
echo '{"mode":"substring","q":"--no-verify","fields":["tool_use.input"]}' | pg-claude-code-recorder search
pg-claude-code-recorder find <session-id|text>
```

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
  `query_timeouts_total{op}`.
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
  the operator has checked the new file. The `rebuild` command (not `serve --rebuild`) runs in bulk
  mode: one write transaction spans many files and commits at 100,000 records or 64 MiB of parsed
  copy bytes, with `synchronous=OFF`, a 256 MiB page cache, a 1 GiB WAL autocheckpoint and FTS5
  automerge off; at the end it restores `synchronous=FULL` and the default automerge, merges the FTS5
  segments and runs `wal_checkpoint(TRUNCATE)`. A rebuild is repeatable, so a crash in the middle
  may leave an unusable half file: a marker row (`meta.rebuild_unfinished`) is written durably first
  and cleared last, and `serve` (and every other start) refuses a file that still has it, telling
  the operator to run `rebuild` again. The `rebuild` command logs a `rebuild_progress` line every 30
  seconds (`--progress-interval` changes it) with `records_parsed`, `records_per_sec` and
  `commits_per_sec` over the interval, `files_done` of `files_total`, and the byte sizes of `index.db`
  and its WAL, then a `rebuild_summary` with the totals and `wall_sec`; these are log fields only (the
  command opens no metrics endpoint). `feed_waiters` is the gauge of waiters in progress.

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
`remove.maxPerHour` (200), `held.recheck`, `format.holdRate` (0.01: a source whose bad-line or unknown-key
rate over the last day is above it is not removed) and `doctor.interval` (24h; doctor also runs from a timer
inside the daemon, so no external scheduler is needed).

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

Removal readiness checklist: the daemon-down and free-space alerts are registered (phase 7c); `doctor` has
compared every copy against the Claude files and its last run is recent; pa-monitor `sessions` and ccpool resume
have moved to the recorder; the state directory is backed up (excluding `recorder.sock` and temporary files);
`remove --dry-run` prints what would go and how many GB.

Signals: `quarantine_files`, `quarantine_bytes`, `quarantine_oldest_age_seconds`,
`removal_quarantine_mismatch_total`, `removal_backlog_conversations`, `removal_backlog_bytes`,
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

## Signals, alerts and the dashboard (phase 7c)

Files under `grafana/` (the location follows `packages/pg-connector/grafana`):

| File                                  | What it is                                                                                                                                                                                                                                                                                                                               |
| ------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `alerting/alerts.yaml`                | fourteen Grafana rules, uid `pg-ccrec-<name>`, registered by `darwin/modules/pg-claude-code-recorder` while a daemon is deployed                                                                                                                                                                                                         |
| `alerting/alerts-delete.yaml`         | the `deleteRules`-only twin (same uids), registered instead while no daemon is deployed; the two are never loaded together                                                                                                                                                                                                               |
| `alerting/rule-tests/`                | one promtool test file per rule, run through `packages/pg-connector/grafana/check-alert-rules.sh`                                                                                                                                                                                                                                        |
| `pg_claude_code_recorder_metrics.txt` | the metric catalogue (the Signals table): name, type, labels, with the enumerated label values                                                                                                                                                                                                                                           |
| `pg-claude-code-recorder.json`        | the dashboard: copy and parse lag, Conversations by stage, held age, dropped bytes by reason, free space and its slope, removal backlog, Processes without an inbox, request rate by client, settle tick duration, settle tick work, loop runs by outcome, recorder cpu cores, go heap, go goroutines, refresh gauges duration, wal size |
| `check-alert-structure.sh`            | structure check S1 to S8 with `--self-test` negative controls (a 41-character uid, a missing `noDataState`, a missing guard, a twin that differs, and more)                                                                                                                                                                              |
| `check-crash-share-label-table.sh`    | runs the crash-share PromQL against `client/testdata/labels.json`, the same label table the daemon, client and command tests use                                                                                                                                                                                                         |
| `check-signals-registration.sh`       | catalogue against the Go `M*` constants, rule uids against `registeredRuleUIDs`, the twin, the dashboard and this section, with `--self-test`                                                                                                                                                                                            |

Flake checks (build one with `nix build .#checks.<system>.<name>`): `test-pg-claude-code-recorder-alert-rules`,
`test-pg-claude-code-recorder-alert-structure` and `test-pg-claude-code-recorder-signals-registration`. Every rule uid is also in
`registeredRuleUIDs` of `packages/pg-router-probe/cmd/pg-router-probe/run.go` (a deployment that passes its own `--rule-uid` list replaces
that list and must carry the fourteen too).

Alerts and their thresholds (the thresholds are the builder's calls; a literal that stands for a config default is named in the rule):

| Alert                | Fires when                                                                                              | Severity | No data  | First action                      |
| -------------------- | ------------------------------------------------------------------------------------------------------- | -------- | -------- | --------------------------------- |
| `daemon-down`        | `up` of the scrape target is 0 for 2 minutes (scrape interval 30s); `SYSTEM_PAUSE` guard                | critical | Alerting | `status`; the launch agent log    |
| `heartbeat-stale`    | scraped (`and on() up == 1`) and the last sweep is older than 2700 s (3 times `sweep.interval`)         | warning  | Alerting | `status`; pprof goroutine dump    |
| `copy-lag`           | copy lag over 120 s for 10 minutes                                                                      | warning  | OK       | `coverage`                        |
| `parse-stuck`        | parse lag bytes above 0 and the parse progress ratio unchanged for 10 minutes                           | warning  | OK       | `coverage`; the log               |
| `held-old`           | oldest held Conversation over 259200 s (the `held.alertAfter` default, 3 days)                          | warning  | OK       | `held`                            |
| `free-space`         | free bytes under 20 GiB (the `minFreeBytes` default), or the 6-hour slope predicts full within 24 hours | critical | Alerting | the Disk full runbook             |
| `unknown-key-burst`  | more than 100 new unknown keys in an hour                                                               | warning  | OK       | `coverage`; update the parser     |
| `data-loss`          | any increase of `dropped_bytes_total` in an hour (the firing alert carries the `reason`)                | warning  | OK       | `coverage`; the log               |
| `removal-backlog`    | the removal queue is over 4800 Conversations (24 hours of the default `remove.maxPerHour` of 200)       | warning  | OK       | `remove --dry-run`                |
| `doctor-mismatch`    | `doctor_mismatch_files` above 0, or a non-zero `doctor_last_run_timestamp` older than 2 days            | critical | Alerting | `doctor`                          |
| `control-inbox-gone` | a live Process in `control_inbox_state{state="gone"}` for 10 minutes (never `never_loaded`)             | warning  | OK       | `pg-claude-control list`          |
| `crash-share`        | crashed plus suspected over 20 percent of a day's ends, with at least 20 ends                           | warning  | OK       | `conversations --crashed --since` |
| `removal-stuck`      | the oldest quarantined file is over an hour old                                                         | warning  | OK       | `remove --dry-run`; the log       |
| `settle-tick-slow`   | the last settle tick took over 60 s (the tick interval) for 15 minutes                                  | warning  | OK       | `status`; the log; pprof CPU      |

Grafana has no inhibition, so each guard is in the expression: `daemon-down` adds the `SYSTEM_PAUSE` gate to a value that is never empty (a pause cannot read as NoData); `heartbeat-stale` and
`doctor-mismatch` read 0 while the daemon is not scraped (so `daemon-down` alone pages); `free-space` is deliberately unguarded and
reads the free-bytes figure the node reports, so a full disk pages whether or not the daemon is scraped. A `doctor_last_run_timestamp`
of 0 means doctor has not run yet (the daemon first runs it one `doctor.interval` after start): that is not stale, and the removal
readiness checklist treats it as not ready.

Signals added by the second review: `conversation_end_total{registry_present,exit_observed,clear_entry,exit_entry}` (evidence classes,
each `true` or `false`; never a label such as crashed), `registry_stale_at_discovery_total` and the log event `startup_stale_registries{n}`;
`quarantine_files`, `quarantine_bytes`, `quarantine_oldest_age_seconds`, `removal_quarantine_mismatch_total` and the events
`quarantined`, `quarantine_restored`, `quarantine_orphan_found_on_start`; `torn_tail_age_seconds{source}` and the event
`torn_tail_pending`; `doctor_last_run_timestamp` and `doctor_mismatch_files`; `control_inbox_state{state=present|gone|never_loaded}`,
`control_requests_total{verb=submit|interrupt|run|read|ping|other, outcome=ok|rejected|duplicate|refused|other}`,
`end_reason_source_files_total{outcome=ingested|removed|invalid}`, `held_conversations{reason}` and `skipped_files_total{reason}`;
`requests_total{client,op,outcome}` where `client` is a whitelisted name and anything else is `other`. No label is per file or per
session. The catalogue file lists every name with its type and labels.

Two exporter facts the alerts rely on: `bad_lines_total` and `unknown_keys_total` carry no `source` or `type` label (the design listed
them by source and type; the exporter has none), and they are registered when first incremented rather than at zero, so a first burst
that starts from an absent series reads as no increase until the second scrape.

### Apply and restart

`pn workspace apply` replaces the daemon, and the old daemon's `Close` runs `PRAGMA wal_checkpoint(TRUNCATE)` with
no timeout or progress log. With a very large WAL (several GB) that can take minutes. During it the daemon serves
no requests, so `conversation.show` clients see a stall or the daemon-offline error (not `not_found`), the
`daemon-down` alert can page after 2 minutes (expect one page or silence it), and the disk is low, so watch free
space. Do not kill the daemon: a SIGKILL leaves the WAL to be replayed on the next open. The checkpoint is done when
`status` answers again and `wal_bytes` drops; the `wal_truncate_done` event (with `duration_ms`) says so in the log.
The first apply after that change pays this once, and a rollback (revert the commit and apply again) pays it again.
Launchd settings (`KeepAlive`, `ExitTimeOut`) live outside this package and were not checked. The operator runs the
apply.

### Recorder CPU is high: what to look at

1. Open the dashboard panel `recorder cpu cores` (CPU in cores, with the GC pause share as a second series).
2. Open `settle tick duration` and `loop runs by outcome`: a p99 near or over 60 s, or a loop with `error` runs,
   points at the settle tick.
3. `rg settle_tick` in the recorder log: `examined`, `unchanged`, `transitions` and `duration_ms` per tick.
4. Take a 30 s profile with `scripts/profile-cpu.sh --sock SOCK --seconds 30` (pprof is on in the deployed
   configuration).
5. Compare with `wal size`: a WAL that keeps growing makes every read slower. Do not leave `status --watch` open
   while profiling.

### Turning on `remove.enable`

**There is no undo other than `extract`.** Once `remove.enable` is true, the recorder removes the Claude files of Conversations settled
and quiet for `remove.after`; resuming a removed Conversation needs an `extract`, and so the recorder. This section is written only
because the `daemon-down` and `free-space` alerts are registered (phase 7c): turn removal on only while both are live
(`darwin/modules/pg-claude-code-recorder` registers `alerts.yaml` while a daemon is deployed).

Readiness checklist, all of it before setting `remove.enable = true`:

1. The `daemon-down` and `free-space` alerts are registered and not firing (and `doctor-mismatch`, `data-loss` and `removal-stuck` are quiet).
2. `doctor` has compared every copy against the Claude files by full hash, and its last run is recent: `doctor_last_run_timestamp` is
   non-zero and under 2 days old (a stale doctor, or one that never ran, is not ready) with `doctor_mismatch_files` at 0.
3. The clients have moved: pa-monitor `sessions` and ccpool resume read the recorder, not the Claude directory.
4. The state directory is backed up (excluding `recorder.sock` and temporary files).
5. `pg-claude-code-recorder remove --dry-run` prints what would go and how many GB, and that is what you expect.

`remove.maxPerRun` and `remove.maxPerHour` spread the backlog; `removal_backlog_conversations` shows it.
