# pa-monitor

Per-user daemon + TUI for monitoring active Claude Code sessions. Renders context usage, 5h-block + weekly-limit usage, burn rate, subagents, and shells. Emits OpenTelemetry metrics + events when the workspace observability stack is enabled.

## Architecture

Two cooperating processes per OS user:

- **`pa-monitor daemon`** — long-running, owns all state. Polls sessions, tracks 5h blocks and the weekly limit, manages caffeinate, dispatches nudges, emits OTel. Started by a LaunchAgent at login.
- **Clients** (TUI, CLI, cmux-bridge) — talk to the daemon over a Unix domain socket (gRPC). Stateless beyond per-process render caches. When the daemon is unreachable, clients show "daemon offline" rather than fabricating data from disk.

## Quick start

```bash
# Run the daemon (or let the LaunchAgent run it at login).
pa-monitor daemon

# Interactive TUI (always daemon-backed)
pa-monitor tui

# Dump daemon state
pa-monitor status

# Block-style "is any agent actively running a turn right now?"
# (NOT "is any work pending" — see Busy/idle gates below)
if pa-monitor agents-busy-check; then echo "busy"; fi

# Block-style "wait until nothing is actively running"
# (exit 0 = "idle reached", NOT "work finished" — see Busy/idle gates below)
pa-monitor wait-until-agents-finished

# Toggle caffeinate (persists across daemon restarts)
pa-monitor caffeinate on
pa-monitor caffeinate off
pa-monitor caffeinate toggle

# Send a nudge to a session, path, or cmux workspace
pa-monitor nudge session:abc-def --text="continue"
pa-monitor nudge path:/Users/me/project
pa-monitor nudge cmux:ws-1234

# Print details
pa-monitor info session:abc-def
pa-monitor info path:/Users/me/project

# Show the loaded config
pa-monitor config show
```

## Subcommands

| Subcommand                                           | Purpose                                                      |
| ---------------------------------------------------- | ------------------------------------------------------------ |
| `daemon`                                             | Run the long-running daemon (RPC server + tick loop).        |
| `tui`                                                | Interactive TUI (always daemon-backed).                      |
| `status`                                             | One-shot dump of daemon state.                               |
| `caffeinate on\|off\|toggle`                         | Drive the caffeinate manager.                                |
| `nudge <selector> [--text=...]`                      | Signal a session via the daemon.                             |
| `info <selector>`                                    | Print session or directory details.                          |
| `agents-busy-check [--consider-daemon-down-as-busy]` | Exit 0 iff any agent is busy (see caveat).                   |
| `wait-until-agents-finished`                         | Block until nothing is working (see caveat).                 |
| `cmux-bridge`                                        | Long-running process inside a cmux pane; drives the sidebar. |
| `config show`                                        | Print loaded config (read-only).                             |

`<selector>` accepts `session:<id>`, `path:<workspace-path>`, `cmux:<workspace-id>`, or a bare value (slash → path, otherwise session).

### Busy/idle gates: a blocked session counts as idle

`agents-busy-check` and `wait-until-agents-finished` both answer **"is any session actively
progressing right now?"** — not "is all work finished?". Per
`docs/adr/0024-pa-monitor-session-status-blocker-model.md` R3, the daemon's `IsAnyBusy` sums only
sessions with `status == working`. A **`blocked`** session — including `blocker = usage_limit`, i.e.
a session that still has work but is waiting out the 5h/weekly usage window — is **not** counted as
busy and is indistinguishable from `idle` at these two gates.

The exit-code contract is therefore:

| Exit | `agents-busy-check`                                          | `wait-until-agents-finished`                               |
| ---- | ------------------------------------------------------------ | ---------------------------------------------------------- |
| 0    | daemon up and ≥1 session `working`                           | **idle reached** — no session `working` for N ticks        |
| 1    | daemon up and no session `working` (prints `all idle`)       | timeout (`--maximum-wait` elapsed)                         |
| 2    | daemon unreachable (0 with `--consider-daemon-down-as-busy`) | daemon unavailable past `--reconnect-grace`; invalid flags |

(`wait-until-agents-finished`'s doc comment lists `3 = invalid args`, but its `flag.ExitOnError`
flag set exits **2** on an unparseable flag before that code can be reached — so 3 is not observable
today. Callers MUST NOT distinguish "bad flags" from "no daemon" by exit code.)

Callers MUST read these codes as statements about **activity**, not about **completion**:

- `agents-busy-check` exit 1 (`all idle`) MUST NOT be read as "no work is pending". It is reported
  while sessions are blocked on a usage window with work still queued.
- `wait-until-agents-finished` exit 0 means "idle reached" and MUST NOT be read as "work finished".
  It is returned after `--consecutive-idle-checks` ticks with zero `working` sessions, even when
  every session is blocked on a usage window and will resume on its own at the window reset
  (auto-resume).
- A caller that MUST NOT proceed until pending work is genuinely done MUST NOT rely on these gates
  alone. It SHOULD additionally check for `blocked` sessions — `pa-monitor status` prints a
  `sessions: N working, N blocked, N idle` line — and re-wait after the window resets.
- The same caveat applies to the packaged `wait-for-agents-to-finish` wrapper, which delegates here.

This is declared intent, not a defect: ADR 0024 R3 deliberately defines `busy` as `status == working`
only, because a `blocked` session is not "actively progressing". It stands until a superseding
decision; whether a `usage_limit`-blocked session SHOULD hold these gates open is an open question
for pa-monitor's behavior docs, not something to change here.

## Nudge delivery

The daemon never invokes `cmux`: cmux's control socket is only usable from a process
inside the cmux process tree, and the daemon runs as a LaunchAgent (not a cmux
descendant). Instead, each cmux workspace runs a `cmux-bridge` (a cmux descendant) that
opens a bidirectional `BridgeChannel` gRPC stream to the daemon. To nudge a session the
daemon resolves the owning cmux **server PID** (via `ps` ancestry — socket-free) and
pushes a `Deliver{pid}` command down that bridge's stream; the bridge resolves the surface
locally and runs `cmux send`/`send-key`, replying with an ack. The daemon keeps a
per-server bridge registry maintained by a periodic reaper (dead bridges pruned). If no
live bridge exists for a target, the nudge waits briefly in the pending queue and is then
dropped (`pa_monitor.nudge.dropped_no_bridge_total`). Non-cmux terminals
(tmux/ghostty/vscode) are still delivered directly by the daemon. See
`docs/adr/0022-nudge-delivery-via-cmux-bridge.md`.

## OpenTelemetry

OTel is configured via the `[otel]` block in `~/.config/pa-monitor/config.toml` (see
[Configuration](#configuration)) and is read by the daemon, cmux-bridge, and TUI alike — a single
source of truth. When no endpoint is configured the OTel SDK is never initialised and every emit is
a nil-receiver no-op.

The daemon emits:

- **Metrics**
  - Observable gauges: `pa_monitor.sessions.count` (by `state` + workspace/agent/model labels), `pa_monitor.sessions.errored` (by `kind`), `pa_monitor.caffeinate.active`, `pa_monitor.auto_resume.enabled`, `pa_monitor.block.cost.usd`, `pa_monitor.week.cost.usd`, and per-active-session `pa_monitor.session.info` / `pa_monitor.session.tokens` / `pa_monitor.session.cost.usd` (keyed by `session_id`, one row per non-Dormant session).
  - Counters: `pa_monitor.block.usage.limit_hits_total`, `pa_monitor.week.usage.limit_hits_total`, `pa_monitor.caffeinate.rounds_total`, `pa_monitor.caffeinate.grace_expirations_total`, `pa_monitor.signal.sends_total`, `pa_monitor.nudge.queued_total`, `pa_monitor.nudge.suppressed_total`, `pa_monitor.session.api_error.observed_total`, `pa_monitor.session.context.limit_hits_total` (context-window-exceeded / "prompt is too long" hits, by `model`), `pa_monitor.signaler.binary_missing_total` (fired once per required signaler binary — `tmux`/`cmux` — not found on the daemon's PATH at startup, by `signaler` + `binary`).
- **Logs** — structured event records: `block.usage.limit_hit`, `week.usage.limit_hit`, `caffeinate.start`, `caffeinate.grace_expired`, `nudge.queued`, `nudge.sent`, `nudge.suppressed`, `session.api_error.observed`, `session.context.limit_hit`, `signaler.binary_missing`.

The cmux-bridge and TUI additionally emit:

- **`pa_monitor.daemon.connected{component="cmux-bridge"|"tui"}`** — gauge, 1 = connected, 0 =
  disconnected. A provisioned Grafana alert (`grafana/alerting/daemon-connection.yaml`) fires when
  `min by (component)(pa_monitor_daemon_connected) < 1` for more than 1m (`noDataState: OK`).
- **`pa_monitor.client.reexec{component,attempt,outcome}`** — counter, incremented once per
  client self-restart decision (see [Client self-restart](#client-self-restart-on-version-mismatch)).
  `outcome` is `attempt` (a re-exec is about to run), `exhausted` (the attempt budget was spent), or
  `exec_failed` (the exec itself failed). The `exhausted`/`exec_failed` increments make the give-up
  state alertable — a persistent condition, not a one-shot log. Each increment carries a companion
  `client.reexec` log event for trace context.

### cmux-bridge pane output

The cmux-bridge pane shows only timestamped (`2006-01-02 15:04:05`), prefix-less, operator-facing
lines: startup banner, a `⚠ daemon version differs from this bridge — restart this bridge …` warning
(emitted on each (re)connect when the running daemon's build differs from the bridge's own; with
client self-restart enabled this is replaced by a `↻ restarting …` notice or a persistent
`⚠ bridge could not auto-restart …` give-up line — see
[Client self-restart](#client-self-restart-on-version-mismatch)), caffeinate/auto-nudge
state changes, session roster events (`+/-<pid>`), and `Lost connection to daemon` /
`Connection to daemon restored` (shown once per episode). Low-level
RPC/transport/retry detail goes to `~/.cache/pa-monitor/cmux-bridge.log` and to OTel logs — never
the pane.

### Client self-restart on version mismatch

When a `darwin-rebuild` restarts the launchd **daemon** on a new build, any long-running
`cmux-bridge` / `tui` keeps executing the **old** client binary against the newer daemon. With
`auto_restart_on_version_mismatch = true` (default off — see [Configuration](#configuration)), on a
wire-version mismatch the client re-executes itself in place via `execve(2)` (same PID and
controlling TTY, so cmux keeps tracking the same pane) to pick up the new build. The exec target is
resolved via `PATH` (the `darwin-rebuild`-flipped profile symlink), not the running build's
`/nix/store` path. Restarts are bounded (a small fixed cap with a short backoff so the attempts span
the activation window); if the mismatch does not clear within the budget the client **gives up**,
reverts to the disabled warn-only behavior, and surfaces a persistent "restart this client manually"
error. Config is read once at startup, so a client already running when the flag is first enabled
only warns on the first upgrade — restart it once manually; every later upgrade auto-restarts. See
`docs/adr/0027-pa-monitor-client-self-restart-on-version-mismatch.md` and `internal/reexec`.

A Grafana dashboard ships at `grafana/pa-monitor-overview.json` and is registered via
`phillipgreenii.observability.dashboardProviders.pa-monitor` when observability is enabled.
A full-width **usage-limit banner** sits at the very top: each row appears only while that
window's authoritative `used_percentage` is at the cap (the 5h or weekly limit is hit) and shows
a live countdown to the window reset (`resets_at - now`); the banner is blank when no limit is
active.

## Labels

Generic label keys (the contract) populated by built-in detectors and shell-out decorators:

`workspace.terminal`, `workspace.scope`, `workspace.project`, `workspace.repo`, `agent.kind`, `agent.role`, `agent.mode`, `model`, `plan_tier`, `block.id`, `week.id`.

Consumer-specific values (e.g. `workspace.scope=zr`) come from external shell-out decorators registered via the nix module. Decorators must reside under `/nix/store/` — the binary path is `filepath.Clean`-canonicalised and prefix-checked.

A decorator's `command` is a shell-words string: the first word is the `/nix/store/` binary and any remaining words are passed as arguments, and an optional `[decorator.env]` table forwards extra environment variables to the child (merged onto the minimal base env of `PA_MONITOR_DECORATE=1` + a fixed `PATH`). This lets a **generic** decorator be configured with flags and env directly from `config.toml` — no per-consumer `writeShellScriptBin` wrapper is required:

```toml
[[decorator]]
name = "scope"
command = "/nix/store/…-pa-monitor-decorator/bin/decorator -rule scope"
timeout_ms = 1500

[decorator.env]
PA_MONITOR_SCOPE_RULES = "ziprecruiter:~/zr"
```

A single-path `command` with no arguments and no `[decorator.env]` behaves exactly as before.

## Storage (XDG)

| Purpose                           | Path                                                                                                 |
| --------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Config                            | `$XDG_CONFIG_HOME/pa-monitor/config.toml`                                                            |
| Unix socket                       | `$XDG_RUNTIME_DIR/pa-monitor/daemon.sock` (Linux) / `$XDG_STATE_HOME/pa-monitor/daemon.sock` (macOS) |
| PID lock                          | same directory as the socket, file `daemon.pid`                                                      |
| Runtime state (caffeinate toggle) | `$XDG_STATE_HOME/pa-monitor/runtime.json`                                                            |
| Daemon stderr (LaunchAgent)       | `$XDG_STATE_HOME/pa-monitor/launchd-stderr.log`                                                      |

## Configuration

`config.toml` (`~/.config/pa-monitor/config.toml`) is nix-rendered; see `internal/config/config.go`
for all keys. Defaults work for most users.

### `[otel]` block

Controls OpenTelemetry export for the daemon, cmux-bridge, and TUI. There is no `protocol` key —
the exporters are gRPC-only; the endpoint URL scheme selects transport (`http://` = insecure gRPC).

```toml
[otel]
endpoint = "http://127.0.0.1:4317"

[otel.resource_attributes]
# Optional extra resource attributes attached to every export.
# "deployment.environment" = "dev"
```

| Key                          | Type       | Description                                    |
| ---------------------------- | ---------- | ---------------------------------------------- |
| `otel.endpoint`              | string     | OTLP gRPC endpoint. Omit to disable OTel.      |
| `otel.resource_attributes.*` | string map | Extra resource attributes (key = value pairs). |

### Other keys

| Key                                | Type | Default | Description                                                                                                                                                     |
| ---------------------------------- | ---- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `auto_restart_on_version_mismatch` | bool | `false` | Opt the `cmux-bridge` and `tui` into re-executing themselves on a daemon-version upgrade — see [Client self-restart](#client-self-restart-on-version-mismatch). |

## TUI symbols

Status glyph shown next to each session (see `internal/render/modals.go` for the in-app legend):

| Glyph | Status   | Meaning                                 |
| ----- | -------- | --------------------------------------- |
| `●`   | working  | actively producing output               |
| `○`   | idle     | waiting for input                       |
| `◐`   | blocked  | has work but can't proceed (see status) |
| `⏸`   | paused   | blocked on a usage/rate limit           |
| `?`   | awaiting | blocked on human input                  |
| `☾`   | dormant  | idle 20m+ (resumable)                   |
| `⊘`   | auth     | authentication failure — run `/login`   |
| `⚠`   | error    | retryable error (auto-resuming)         |
| `✗`   | error    | non-retryable error                     |

## Keybindings (TUI)

| Key                  | Action                              |
| -------------------- | ----------------------------------- |
| `up`/`down`, `j`/`k` | Move cursor                         |
| `enter`              | Open session details                |
| `esc`                | Close session details               |
| `t`                  | Toggle tokens vs. cost display      |
| `a`                  | Toggle active-only vs. all sessions |
| `n`                  | Toggle name vs. id display          |
| `C`                  | Toggle caffeinate                   |
| `q`/`ctrl+c`         | Quit                                |
