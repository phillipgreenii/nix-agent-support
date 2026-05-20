# pa-monitor Daemon + OTel Support Design

**Status**: Draft
**Date**: 2026-05-20
**Deciders**: phillipg

## Summary

Split today's `claude-agents-tui` single-binary into a long-running daemon
that owns all session tracking, caffeinate management, 5-hour billing block
and weekly limit tracking, OpenTelemetry emission, and a set of thin clients
(TUI, CLI subcommands, cmux bridge) that talk to the daemon over a Unix
socket. Rename the binary to `pa-monitor`. Move all code into the
`phillipgreenii-nix-agent-support` repository.

OpenTelemetry support is opt-in via the workspace's
`phillipgreenii.observability` module. When disabled, the daemon still runs
and serves clients; it just emits no telemetry.

## Goals

- Continuous time-series visibility into agent activity, regardless of
  whether a user has the TUI open.
- Track and surface: 5h-block usage, weekly-limit usage, per-session
  context-window usage, caffeinate rounds, nudges sent (including
  post-window nudges), session state transitions, subagent and shell counts,
  burn rates.
- One process per OS user owns the world. Clients are stateless except for
  per-process render caches.
- ZipRecruiter-specific labelling kept out of this repo; supplied via
  shell-out decorator binaries.

## Non-Goals

- Per-session-id metric labels (cardinality explosion). Session IDs appear
  only as event attributes.
- Cross-machine aggregation. Single-host only.
- A configuration UI. Config is nix-rendered into `config.toml`; runtime
  state (caffeinate toggle) lives separately.
- Long-lived OTel spans for 5h blocks, weekly windows, or caffeinate rounds
  (replaced by correlation-ID labels).

## Process Model

One binary, multiple subcommands. Each runtime process is a separate
invocation. Distribution stays simple — one Go module, one nix derivation,
one set of tests.

```
pa-monitor daemon                # long-running; owns all state
pa-monitor tui                   # interactive TUI client
pa-monitor status                # one-shot dump of daemon state
pa-monitor caffeinate on|off     # toggle caffeinate
pa-monitor nudge <selector>      # send nudge to session(s)
pa-monitor info <selector>       # query session or path details
pa-monitor agents-busy-check     # one-shot; exit 0 iff any agent busy
pa-monitor wait-until-agents-finished  # block until idle
pa-monitor cmux-bridge           # runs inside a cmux session
pa-monitor config show           # dump loaded config (read-only)
```

`<selector>` accepts:

- A Claude session id
- A workspace path (resolves to all sessions under it)
- A cmux workspace id (`CMUX_WORKSPACE_ID`)

### Binary naming and BTM display

The nix package wraps the daemon executable with
`pkgs.writeShellScriptBin "pa-monitor-daemon"`. The LaunchAgent's
`ProgramArguments[0]` is `${wrapper}/bin/pa-monitor-daemon`. macOS
Background Activity displays the basename: `pa-monitor-daemon`.

cmux-bridge follows the same pattern when launched from a cmux pane:
`pa-monitor-cmux-bridge` wrapper, shown as `pa-monitor-cmux-bridge` in BTM.

No `claude-agents-tui` symlink is shipped. Old binary name is removed.

## Daemon Responsibilities

Moved from today's TUI + headless modes into the daemon:

- Session enumeration and transcript parsing (`internal/session`,
  `internal/transcript`)
- Aggregate computation (`internal/aggregate`)
- 5-hour billing block tracking (via `ccusage blocks --active --json`)
- Weekly limit tracking (via `ccusage weekly --json --offline`)
- Burn-rate computation (`internal/burnrate`)
- Caffeinate management (`internal/caffeinate`)
- Nudge dispatch through the signal layer (`internal/signal`)
- Tree-state and path rollups (`internal/aggregate/pathtree`,
  `internal/treestate`)
- Label decoration (built-in detectors + decorator shell-out)
- OTel emission (metrics, events as logs, traces for discrete operations)
- Diagnostic logging to `$XDG_STATE_HOME/pa-monitor/daemon.log` (always on
  at warning level, regardless of OTel state)

Clients (TUI, CLI, cmux-bridge) own only rendering, RPC, and per-process
state.

## RPC Surface

gRPC over a Unix domain socket. Schemas under `internal/proto/`. Generated
Go code committed to the tree (no codegen at build time on the consumer
side; codegen is a nix-driven dev task).

Socket path:

- Linux: `$XDG_RUNTIME_DIR/pa-monitor/daemon.sock`
- macOS: `$XDG_STATE_HOME/pa-monitor/daemon.sock`
  (`XDG_RUNTIME_DIR` is not set by default on macOS)

Socket permissions `0600`. No authentication beyond filesystem owner.

PID lock at the same directory: `daemon.pid`. Acquired via `flock`.

### Methods (rough; finalised at impl)

| Method                                     | Shape         | Use                   |
| ------------------------------------------ | ------------- | --------------------- |
| `GetState() → DaemonState`                 | unary         | one-shot status       |
| `WatchState(StateFilter) stream`           | server-stream | TUI live updates      |
| `Caffeinate(action) → CaffeinateResponse`  | unary         | on/off/toggle         |
| `Nudge(Selector, text?) → NudgeResponse`   | unary         | send nudge            |
| `IsAnyBusy() → BusyResponse`               | unary         | `agents-busy-check`   |
| `GetSessionInfo(Selector) → SessionDetail` | unary         | `info`                |
| `GetPathInfo(path) → PathRollup`           | unary         | `info`                |
| `Drain() → DrainResponse`                  | unary         | clean shutdown signal |

### Heartbeat

Server-streaming RPCs (`WatchState`) emit a `Heartbeat{ts, daemon_uptime_s}`
message every 2 seconds (configurable) when no real events are pending. A
client that receives no message within 2 × heartbeat interval treats the
stream as dead, closes it, and reconnects with backoff.

This catches daemons that hold the socket but are stuck mid-handler — HTTP/2
PING alone cannot.

Unary RPCs rely on a gRPC deadline (default 2 s). No app-level heartbeat
there.

## Storage Layout (XDG)

| Purpose                                       | Path                                                                  |
| --------------------------------------------- | --------------------------------------------------------------------- |
| Config (nix-rendered)                         | `$XDG_CONFIG_HOME/pa-monitor/config.toml`                             |
| Label rules (nix-rendered)                    | `$XDG_CONFIG_HOME/pa-monitor/labels.toml`                             |
| Daemon log                                    | `$XDG_STATE_HOME/pa-monitor/daemon.log` (rotated by size, lumberjack) |
| Unix socket                                   | `$XDG_STATE_HOME/pa-monitor/daemon.sock`                              |
| PID lock                                      | `$XDG_STATE_HOME/pa-monitor/daemon.pid`                               |
| Runtime state (caffeinate toggle persistence) | `$XDG_STATE_HOME/pa-monitor/runtime.json`                             |
| Built-in data caches if needed                | `$XDG_DATA_HOME/pa-monitor/`                                          |

When OTel is on, diagnostic warnings still write to `daemon.log`. Event-log
records go only to OTel.

## Labels

### Stable label keys (the contract)

| Key                  | Values                                                                                                             | Source                                    |
| -------------------- | ------------------------------------------------------------------------------------------------------------------ | ----------------------------------------- |
| `workspace.terminal` | `cmux`, `tmux`, `direct`, `none`                                                                                   | built-in detector                         |
| `workspace.scope`    | decorator-provided (`gascity`, `zr`, `personal`, …); omitted when no decorator fires                               | built-in (gascity) + shell-out decorators |
| `workspace.repo`     | canonical git origin, e.g. `github.com/phillipgreenii/nix-agent-support`; or `local:<short-hash>` when no remote   | built-in `repo` detector                  |
| `workspace.project`  | first non-empty of: `$GC_RIG`, `$WORKSPACE`, git worktree basename when distinct from repo root; omitted otherwise | built-in `project` detector               |
| `agent.kind`         | `claude`, `codex`, …                                                                                               | built-in (from transcript model)          |
| `agent.role`         | `polecat`, `mayor`, `witness`, …, or `user`; or decorator-supplied                                                 | built-in (gascity) + decorators           |
| `agent.mode`         | `interactive`, `headless`                                                                                          | built-in (tty detection)                  |
| `model`              | claude model id                                                                                                    | transcript                                |
| `plan_tier`          | `pro`, `max_5x`, `max_20x`                                                                                         | config                                    |
| `block.id`           | UTC hour of block start: `YYYY-MM-DDTHHZ`                                                                          | block tracker                             |
| `week.id`            | ISO week: `YYYY-Www`                                                                                               | week tracker                              |

Empty / nil values are dropped before emission. Absent label = "not
detected" by Prometheus convention.

### Cardinality caps

Daemon enforces a per-label-key cap on distinct values. Default: 50. Excess
values bucket to `other`. Caps apply to: `workspace.repo`,
`workspace.project`, `agent.role`, and any decorator-supplied keys.

`block.id` and `week.id` are uncapped at the daemon level (block: ~5/day,
week: 1/week). Series with stale IDs naturally stop emitting once their
window ends; long-term cardinality is bounded by the collector's retention
policy (set elsewhere in `phillipgreenii.observability`).

### Detectors

Built-in (ship in `internal/labels/detectors/`):

- `terminal` — reads env (`CMUX_WORKSPACE_ID`, `TMUX`) and parent process
  tree.
- `gascity` — reads `GC_*` env, maps to `workspace.scope=gascity`,
  `workspace.project=$GC_RIG`, `agent.role=$GC_AGENT`. Polecat detection
  lives here.
- `repo` — walks up from session cwd for `.git`. Runs
  `git config --get remote.origin.url`. Normalises:
  - `git@github.com:owner/repo.git` → `github.com/owner/repo`
  - `https://github.com/owner/repo.git` → `github.com/owner/repo`
  - Stripped trailing `.git`.

  Fallback when no remote: `local:<short-hash of git-common-dir abspath>`.

- `project` — resolution order:
  1. `$GC_RIG`
  2. `$WORKSPACE`
  3. Worktree basename if differs from repo basename
  4. Omitted.
- `agent` — `kind` from transcript model id; `mode` from `isatty(stdin)` on
  the agent process when discoverable, else `headless`.

### Decorators (shell-out, optional)

Configured in nix-rendered `config.toml`:

```toml
[[decorator]]
name = "zr-labels"
command = "/nix/store/.../bin/pa-monitor-decorator-zr"
timeout_ms = 2000
```

Protocol:

- Daemon spawns decorator with `PA_MONITOR_DECORATE=1`, session JSON on
  stdin.
- Decorator emits JSON `{ "labels": { ... } }` on stdout, exits 0.
- Non-zero exit, timeout, or parse failure: log warning, skip that
  decorator's labels for that session.

Constraints:

- Daemon rejects decorator paths that are not absolute paths under
  `/nix/store/`. This forces decorators to come from nix-managed,
  reproducible builds.
- Decorators run **only at session discovery**. Labels cache for the
  session's lifetime. No periodic re-decoration.
- On key conflict between decorator output and built-in detectors,
  decorator wins.

Effect on consumers:

- `phillipg-nix-ziprecruiter` ships a `pa-monitor-decorator-zr` package
  and registers it via this repo's nix module. No code lives in this repo
  to know about ZipRecruiter.

## Signals

### Metrics (low-cardinality)

| Name                                            | Type        | Labels                                               | Source                |
| ----------------------------------------------- | ----------- | ---------------------------------------------------- | --------------------- |
| `pa_monitor.sessions.count`                     | gauge       | `state`, `workspace.scope`, `agent.role`             | poller snapshot       |
| `pa_monitor.subagents.count`                    | gauge       | —                                                    | poller                |
| `pa_monitor.shells.count`                       | gauge       | —                                                    | poller                |
| `pa_monitor.block.cost_usd`                     | gauge       | `plan_tier`, `block.id`                              | aggregate             |
| `pa_monitor.block.window_pct`                   | gauge       | `plan_tier`, `block.id`                              | aggregate             |
| `pa_monitor.block.usage.limit_hits_total`       | counter     | `plan_tier`, `block.id`                              | transition            |
| `pa_monitor.week.cost_usd`                      | gauge       | `plan_tier`, `week.id`                               | ccusage weekly        |
| `pa_monitor.week.window_pct`                    | gauge       | `plan_tier`, `week.id`                               | ccusage weekly        |
| `pa_monitor.week.usage.limit_hits_total`        | counter     | `plan_tier`, `week.id`, `source={computed,observed}` | transition            |
| `pa_monitor.session.context_pct`                | histogram   | `model`, `workspace.repo`                            | per-session at sample |
| `pa_monitor.session.context.limit_hits_total`   | counter     | `model`, `workspace.repo`, `workspace.scope`         | transition            |
| `pa_monitor.burn.usd_per_hour`                  | gauge       | `window={short,long}`                                | burnrate              |
| `pa_monitor.caffeinate.active`                  | gauge (0/1) | —                                                    | caffeinate manager    |
| `pa_monitor.caffeinate.rounds_total`            | counter     | `cause={agents_active,manual}`                       | transition            |
| `pa_monitor.caffeinate.grace_expirations_total` | counter     | —                                                    | transition            |
| `pa_monitor.signal.sends_total`                 | counter     | `signaler`, `outcome`, `post_window`                 | signal layer          |

`post_window=true` answers the "wake-ups after the 5h window" question:
true iff the signal was sent while the session was paused awaiting the next
block reset.

### Events (OTel logs)

Low-rate; one record per state transition. All carry resource attrs
identifying the daemon. Per-event attrs below.

| Event                       | Attrs                                                                   |
| --------------------------- | ----------------------------------------------------------------------- |
| `block.window.start`        | `block.id`, `plan_tier`, `block_start_ts`, `block_end_ts`               |
| `block.window.end`          | `block.id`, `plan_tier`, `cost_usd`, `window_pct`, `exhausted`          |
| `block.usage.limit_hit`     | `block.id`, `plan_tier`, `cost_usd`                                     |
| `week.window.start`         | `week.id`, `plan_tier`, `week_start_ts`                                 |
| `week.window.end`           | `week.id`, `plan_tier`, `cost_usd`, `window_pct`                        |
| `week.usage.limit_hit`      | `week.id`, `plan_tier`, `cost_usd`, `source` (`computed` or `observed`) |
| `session.discovered`        | `session.id`, `model`, `workspace.*`, `agent.*`                         |
| `session.state.changed`     | `session.id`, `from`, `to`, `model`, `workspace.repo`, `agent.role`     |
| `session.context.limit_hit` | `session.id`, `model`, `context_pct`, `workspace.repo`                  |
| `caffeinate.start`          | `cause`                                                                 |
| `caffeinate.stop`           | `cause`, `duration_s`                                                   |
| `caffeinate.grace_expired`  | `duration_s`                                                            |
| `nudge.sent`                | `signaler`, `target_kind`, `target_id_hash`, `post_window`, `outcome`   |

`session.id` appears only in events, never in metric labels.

When OTel is off, these events go to `daemon.log` as JSON-line records at
info level.

### Traces

Traces are the OpenTelemetry signal for causal records of discrete
operations. Each operation produces a tree of spans (start, end, attrs,
parent link). Long-lived spans (hours+) are an anti-pattern: they only
flush on close, making them invisible during the window of interest.

v1 trace surface — discrete operations only:

| Span                           | Attrs                                               |
| ------------------------------ | --------------------------------------------------- |
| `nudge` (root)                 | `signaler`, `target_kind`, `post_window`, `outcome` |
| `nudge.resolve_target` (child) | resolved selector → set of session ids              |
| `nudge.signal_send` (child)    | `signaler`, `pid_hash`                              |

No spans for poll ticks (periodic noise — already covered by metrics). No
spans for blocks, weeks, or caffeinate rounds — correlation is via `block.id`
/ `week.id` labels on metrics and event attrs.

### Disabled-state contract

- Nix layer: when `phillipgreenii.observability.enable = false`,
  `mkEmitterEnv` returns `{}`. LaunchAgent gets no `OTEL_*` env.
- Daemon: missing `OTEL_EXPORTER_OTLP_ENDPOINT` → emitter constructor
  returns nil. All `Record*` helpers are no-ops via nil-check. SDK is never
  initialised; no runtime cost.
- Events fall back to JSON-line records in `daemon.log`.

## Block and Week ID Computation

### Block ID

Source: `ccusage blocks --active --json`. The block's `startTime` already
identifies the window. Formatted to UTC hour for stability:

```
block.id = startTime.UTC().Format("2006-01-02T15Z")
```

Example: `2026-05-20T14Z`.

### Week ID

Source: `ccusage weekly --json --offline`. ccusage groups by Monday-anchored
calendar weeks in local time, exposing `period: "YYYY-MM-DD"` (the Monday).

```
week.id = ISO week of (period date interpreted as UTC midnight)
        = "2026-W21"
```

Anthropic's actual weekly-reset boundary is not authoritatively documented
in this environment. ccusage's Monday-anchor may not match Anthropic's
billing reset. v1 ships with ccusage's grouping; the `source` attribute on
`week.usage.limit_hit` events distinguishes `computed` (we crossed our
threshold) from `observed` (Claude transcript shows a weekly-limit signal).
Discrepancies between the two sources can be reviewed after collection.

If the reset turns out to be account-anchored or otherwise non-Monday, the
`week.id` format swaps to anchor-relative without changing the label key.

## Plan Caps

`internal/ccusage/plan_caps.go` extends:

```go
type PlanCaps struct {
    BlockCapUSD float64
    WeekCapUSD  float64
}
```

Config keys added: `week_cap_pro_usd`, `week_cap_max5_usd`,
`week_cap_max20_usd`. Defaults align with Anthropic's published plan
limits at the time of implementation. Values pulled from existing
`config.toml` knobs; nix-rendered.

## Degraded-Mode Behaviour

Required behaviour for all clients when the daemon socket is unavailable
or unhealthy.

### TUI

- **Fresh start, no daemon**: empty UI body, top status pill reads
  `CONNECTING…`. Backoff reconnect (1 s → 30 s cap).
- **Mid-session drop**: keep rendering last in-memory state. Pill switches
  to red `OFFLINE — last data Ns ago`, age ticks up. **No on-disk state
  cache.** Cache lives only in process memory.
- **Reconnect succeeds**: pill clears, UI resumes from new stream.

### cmux-bridge

- Stays alive on socket drop. Cmux sidebar status changes to
  `pa-monitor: daemon offline`. Nudge requests via the bridge return
  `unavailable` to the caller.
- Reconnect with backoff. Sidebar restores on success.

### wait-until-agents-finished

- Mid-wait drop: enter "reconnecting" phase. Stderr emits
  `daemon connection lost, retrying (grace 30s)...`. Backoff retry.
- Socket returns before grace: resume stream. **Idle-tick counter resets**
  (daemon may have restarted; cannot trust state continuity).
- Grace expires: exit 2.
- Daemon never available at start: exit 2 immediately.

Reconnect grace is a flag: `--reconnect-grace=Ns` (default 30 s).

### agents-busy-check

Exit codes:

| State                                             | Exit | bash `if` |
| ------------------------------------------------- | ---- | --------- |
| Daemon up, ≥1 agent busy                          | 0    | true      |
| Daemon up, no agents busy                         | 1    | false     |
| Daemon down (default)                             | 2    | false     |
| Daemon down with `--consider-daemon-down-as-busy` | 0    | true      |

Stderr emits a one-line outcome description; stdout silent so bash idioms
work cleanly.

### One-shot CLI (status, info, caffeinate, nudge)

Daemon unavailable: exit non-zero with a clear stderr message including the
socket path and the launchctl command to restart the daemon.

## Daemon Lifecycle and Cleanup

The daemon takes a `flock` on the PID file at startup. Cleanup must be
robust across every failure path the OS can produce.

### Startup sequence

1. Resolve `$XDG_STATE_HOME/pa-monitor/`. Create if missing.
2. Open `daemon.pid` for write, take `flock` (non-blocking).
3. If lock fails: read existing pid. If pid alive AND process matches
   (`/proc` on Linux; pid+start-time on macOS via `ps`): exit 1 with
   "another daemon already running, pid=…". If stale: continue.
4. Write current pid to `daemon.pid`.
5. Remove any stale `daemon.sock` file. Bind new socket. Chmod 0600.
6. Register cleanup defers and signal handlers (`SIGTERM`, `SIGINT`).
7. Serve until signalled.

### Cleanup defers (in reverse-registration order)

- Close gRPC server.
- Close and unlink socket file.
- Release flock and unlink pidfile.
- Flush OTel exporter (with 5 s deadline).

### Failure-path test matrix

Each row is a required `lifecycle_test.go` case using real subprocesses and
real Unix sockets. No mocking at this layer.

1. Clean shutdown via `SIGTERM`. Both files removed.
2. `SIGKILL`. Next start detects stale pidfile (pid not alive). Reclaims
   both files.
3. Panic mid-handler. Recover deferred. Cleanup fires. Both files gone.
4. Crash before defer registration (simulate via env-controlled exit in
   init). Next start: pid not alive → reclaim. Stale socket unlinked.
5. Concurrent start race. Two daemon processes launched within milliseconds.
   Exactly one acquires the flock; loser exits 1 with the "already running"
   message.
6. Pid recycled. Pidfile lists pid N, but pid N is now an unrelated process.
   Detected via pid+start-time check; reclaim.
7. Socket file exists but no listener (stale from prior crash). First
   connect attempt fails. Next start: unlink + recreate.
8. Disk full on pidfile write. Start fails cleanly; no half-state.
9. Socket parent directory removed mid-run. Daemon logs fatal, exits with
   non-zero status, no half-cleanup.
10. Filesystem permission denied on socket dir. Start fails with clear
    stderr.

### Client recovery test matrix

Each client owns tests for recovery from socket disappearance and from
hung-but-alive daemons.

| Client                          | Tests                                                                                                                                                                                                                                         |
| ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| TUI                             | Mid-stream socket drop → OFFLINE pill renders → last data preserved → daemon restart → auto-reconnect → pill clears → data resumes. Daemon never available at start → CONNECTING pill persists. Heartbeat miss with socket alive → reconnect. |
| cmux-bridge                     | Socket drop → sidebar shows offline → nudge requests rejected with `unavailable` → reconnect → sidebar restores.                                                                                                                              |
| wait-until-agents-finished      | Drop within grace → reconnect → idle-tick counter resets and reconverges. Drop past grace → exit 2. Daemon never available → exit 2.                                                                                                          |
| agents-busy-check               | Daemon down (default) → exit 2. Daemon down with `--consider-daemon-down-as-busy` → exit 0. Deadline exceeded → exit 2.                                                                                                                       |
| status, info, caffeinate, nudge | Daemon down → exit non-zero with helpful stderr.                                                                                                                                                                                              |
| Heartbeat (cross-cutting)       | Mock daemon holds socket but emits no heartbeats → client detects within 2 × interval, closes, reconnects.                                                                                                                                    |

All client tests use a binary built once by the test harness and real
subprocess invocation. No transport mocking. Daemon mocks are minimal —
just enough to reproduce the failure mode under test.

## Nix Wiring

### Module: `phillipgreenii-nix-agent-support`

Home-manager module
(`home/programs/pa-monitor/default.nix`) gains options:

```nix
phillipgreenii.programs.pa-monitor = {
  enable = lib.mkEnableOption "pa-monitor (Claude agents monitor)";
  package = lib.mkPackageOption pkgs "pa-monitor" { };
  daemon.enable = lib.mkEnableOption "pa-monitor daemon LaunchAgent";
  config = {
    # mirrors today's claude-agents-tui config keys, rendered to TOML
    planTier = lib.mkOption { type = lib.types.str; default = "max_5x"; };
    # ...
  };
  decorators = lib.mkOption {
    type = lib.types.listOf (lib.types.submodule { ... });
    default = [ ];
    description = "Shell-out label decorators; each must reside under /nix/store/.";
  };
};
```

When `daemon.enable = true`:

- Render `$XDG_CONFIG_HOME/pa-monitor/config.toml` with the configured
  values and decorator entries.
- Install a LaunchAgent plist:

  ```nix
  launchd.user.agents.pa-monitor-daemon = {
    serviceConfig = {
      Label = "com.phillipg.pa-monitor-daemon";
      ProgramArguments = [ "${daemonWrapper}/bin/pa-monitor-daemon" ];
      RunAtLoad = true;
      KeepAlive = true;
      StandardErrorPath = "${stateDir}/launchd-stderr.log";
      EnvironmentVariables = config.phillipgreenii.observability.mkEmitterEnv {
        serviceName = "pa-monitor";
        protocol = "grpc";
      };
    };
  };
  ```

- `daemonWrapper = pkgs.writeShellScriptBin "pa-monitor-daemon"
"exec ${pa-monitor}/bin/pa-monitor daemon"` ensures BTM shows
  `pa-monitor-daemon`.

When `phillipgreenii.observability.enable = false`, `mkEmitterEnv` returns
`{}` and the daemon runs without OTel cleanly.

### Dashboard

A single generic dashboard ships from this repo, registered via:

```nix
phillipgreenii.observability.dashboardProviders.pa-monitor = {
  folder = "Claude Agents";
  dashboards = [ ./grafana/pa-monitor-overview.json ];
};
```

Panels:

- Sessions by state (stacked area, filterable by `workspace.scope`)
- 5h block cost vs cap, with current `block.id` shown
- Week cost vs cap, with `week.id`
- Block window-hit rate (1d, 7d)
- Week limit-hit rate (computed vs observed)
- Caffeinate active timeline + rounds counter
- Nudge counts split by `post_window`
- Per-model context distribution heatmap
- Burn rate (short vs long window)

Grafana template variables: `$scope`, `$repo`, `$agent_role`, `$model`,
`$plan_tier`. Single dashboard serves every consumer; ZipRecruiter does
not need its own variant.

### Consumer module (ZipRecruiter)

In `phillipg-nix-ziprecruiter`, a thin module sets values:

```nix
phillipgreenii.programs.pa-monitor = {
  enable = true;
  daemon.enable = true;
  decorators = [
    {
      name = "zr-labels";
      command = "${pkgs.pa-monitor-decorator-zr}/bin/pa-monitor-decorator-zr";
      timeout_ms = 2000;
    }
  ];
};
```

The decorator binary is packaged in that repo. It reads session JSON from
stdin and emits ZR-specific label values (`workspace.scope=zr`,
`workspace.project=<ZR product area>`, etc.) using the generic label keys
defined here.

## Code Layout

```
packages/pa-monitor/
  cmd/pa-monitor/
    main.go                     # subcommand dispatch
    daemon.go
    tui.go
    cli.go                      # status, info, caffeinate, nudge, config show
    busycheck.go                # agents-busy-check
    wait.go                     # wait-until-agents-finished
    cmuxbridge.go
  internal/
    core/                       # NEW: pure logic, no UI
      sessions.go               # was internal/session/*
      aggregate.go              # was internal/aggregate/*
      caffeinate.go             # was internal/caffeinate/*
      block.go                  # 5h block tracking + block.id
      week.go                   # weekly tracking + week.id
      burnrate.go               # was internal/burnrate/*
      transcript.go             # was internal/transcript/*
    daemon/
      server.go                 # gRPC server
      lifecycle.go              # pidfile, socket, cleanup
      lifecycle_test.go         # full failure-path matrix
      heartbeat.go
      runtime_state.go          # persistent runtime.json
    proto/
      pa_monitor.proto
      pa_monitor.pb.go          # generated
      pa_monitor_grpc.pb.go     # generated
    labels/
      detectors/
        terminal.go
        gascity.go
        repo.go
        project.go
        agent.go
      decorator.go              # shell-out runner
      cardinality_cap.go
    otel/
      emitter.go                # nil-safe; no-op when endpoint unset
      metrics.go
      events.go
      traces.go
    rpcclient/                  # thin client used by tui, cli, cmux-bridge
      client.go
      reconnect.go
    ccusage/
      adapter.go                # extended with weekly support
      plan_caps.go              # extended with WeekCapUSD
    signal/                     # unchanged interface; now invoked by daemon only
    render/                     # unchanged
    tui/                        # now consumes core state via RPC client
    cmuxstatus/                 # logic moved into cmd/pa-monitor/cmuxbridge.go
  grafana/
    pa-monitor-overview.json
```

## Migration Phases

Sized to stay reviewable. Each phase is a separate commit (or PR) with its
own tests passing.

1. **Carve `core/` package.** Move pure logic out of today's
   `internal/{session,aggregate,caffeinate,burnrate,transcript}`. No
   behaviour change. Existing TUI continues to consume via direct calls.
2. **Define proto schema + codegen.** Schema in `internal/proto/`. Generated
   files committed. Nix flake gains a `codegen` app for re-running.
3. **Daemon subcommand wrapping `core/`.** Implements all RPCs. Lifecycle
   tests pass with full failure matrix.
4. **OTel emitter + built-in detectors.** Nil-safe. No dependency on
   workspace observability stack — works against any OTLP endpoint or
   none.
5. **Decorator shell-out.** Constraints enforced (path under /nix/store/,
   timeout, JSON contract).
6. **5h block tracker emits `block.id`. Weekly tracker added.** Week limit
   detection (computed + observed). Plan caps extended.
7. **Refactor TUI to gRPC client.** Same look; reconnect logic; OFFLINE
   pill. Client recovery tests pass.
8. **CLI subcommands.** `status`, `info`, `caffeinate`, `nudge`,
   `agents-busy-check`, `wait-until-agents-finished`, `config show`.
9. **cmux-bridge subcommand.** Migrate `cmuxstatus` logic. Nudge proxy.
10. **Nix module.** LaunchAgent + dashboard registration + decorator config
    rendering. Replace today's `home/programs/claude-agents-tui/` module.
11. **Remove old `claude-agents-tui` binary path.** No symlink. Update any
    callers in this repo.

## Operational Concerns

### Caffeinate persistence

`pa-monitor caffeinate on` persists across daemon restarts. Runtime state
written to `$XDG_STATE_HOME/pa-monitor/runtime.json` (atomic write). Daemon
reads on startup.

### 5h-window restart behaviour

Today's poller has stale-pause logic with a `stalePauseGrace` of 5 minutes
past the rate-limit reset. The daemon preserves this behaviour: when a
block window resets, sessions paused awaiting reset receive an automatic
nudge from the daemon (existing semantics, now centralised). Sessions
abandoned for longer than the grace window are not auto-nudged.

### Decorator security

- Daemon refuses decorator entries whose `command` is not an absolute path
  under `/nix/store/`. Enforced at config-load time; daemon exits with a
  clear error if violated.
- Decorators inherit a minimal env: `PA_MONITOR_DECORATE=1`, `PATH`, `HOME`,
  `USER`, and the session env passed in JSON. No daemon-internal secrets.
- Decorators run with no working directory other than `/`. They have no
  socket access to the daemon (preventing recursion).

### Process visibility

All long-running pa-monitor processes (daemon, TUI when interactive,
cmux-bridge) appear in `ps` and Activity Monitor under the wrapper-script
basenames: `pa-monitor-daemon`, `pa-monitor`, `pa-monitor-cmux-bridge`.
Per workspace LaunchAgent naming policy.

## Open Items for Implementation Plan

These are deferred to the writing-plans phase, not blocking the spec:

- Concrete heartbeat interval default (2 s assumed).
- Reconnect-grace default (30 s assumed).
- Cardinality-cap default (50 assumed).
- Exact gRPC deadline values per RPC.
- Per-model context-pct histogram bucket boundaries.
- Whether `pa-monitor` (no subcommand) should default to `tui` or print
  help. Lean toward `tui` for ergonomics, but state explicitly.

## Related Decisions

- See: phillipgreenii-nix-support-apps docs/adr/0037-observability-module-redesign.md
- See: phillipg-nix-ziprecruiter docs/adr/0041-gascity-otel-decoupling.md
- A matching ADR will be recorded in this repo's `docs/adr/` once this
  spec is approved, summarising the binary split and the cross-repo move.
