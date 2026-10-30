# pg-claude-code-recorder

Scaffold of the recorder for Claude Code state (docket `pg2-v77tsx`; design
`docs/superpowers/specs/2026-10-30-pg-claude-code-recorder-design.md`). This
package carries no recorder logic yet: it builds, has its checks and guards, and
holds the conventions later packets land into.

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

| Tag           | Tier                                               | Check                                                                         |
| ------------- | -------------------------------------------------- | ----------------------------------------------------------------------------- |
| none          | unit (fast, hermetic, `-timeout 120s` per package) | `pg-claude-code-recorder-go-tests`                                            |
| `integration` | daemon, subprocess, real external tool             | `pg-claude-code-recorder-integration-tests` (on demand, `-race`, 60m timeout) |
| `faults`      | fault injection                                    | guarded by `-integration-tests-gating`                                        |
| `soak`        | soak (its check lands in phase 2)                  | guarded by `-integration-tests-gating`                                        |

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
config's `socket` (default `<stateDir>/recorder.sock`). `internal/server` owns the framing and
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
  `caught_up`. The `client` label is whitelisted (`clients` in the config adds names); any other
  name is `other`. The exporter is a small text renderer over the in-process registry, so the
  package takes no Prometheus client dependency.
- Log: JSONL on standard error (or `logFile`), service `pg-claude-code-recorder`, fields `ts`,
  `level`, `event`, and where they apply `file_id`, `session_id`, `generation`, `offset`,
  `parser_version`, `cursor`, `stage`. Each request logs `request` with the caller's `--client`.

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
