# pa-monitor

Per-user daemon + TUI for monitoring active Claude Code sessions. Renders context usage, 5h-block + weekly-limit usage, burn rate, subagents, and shells. Emits OpenTelemetry metrics + events when the workspace observability stack is enabled.

## Architecture

Two cooperating processes per OS user:

- **`pa-monitor daemon`** — long-running, owns all state. Polls sessions, tracks 5h blocks and the weekly limit, manages caffeinate, dispatches nudges, emits OTel. Started by a LaunchAgent at login.
- **Clients** (TUI, CLI, cmux-bridge) — talk to the daemon over a Unix domain socket (gRPC). Stateless beyond per-process render caches.

When OTel is disabled or the daemon is not running, the legacy local-poller path is still available via `pa-monitor tui` (no `--remote`).

## Quick start

```bash
# Run the daemon (or let the LaunchAgent run it at login).
pa-monitor daemon

# Interactive TUI talking to the daemon
pa-monitor tui --remote

# Interactive TUI in legacy local-poller mode (no daemon needed)
pa-monitor tui

# Dump daemon state
pa-monitor status

# Block-style "is anything running right now?"
if pa-monitor agents-busy-check; then echo "busy"; fi

# Block-style "wait for everything to settle"
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

| Subcommand                                           | Purpose                                                             |
| ---------------------------------------------------- | ------------------------------------------------------------------- |
| `daemon`                                             | Run the long-running daemon (RPC server + tick loop).               |
| `tui`                                                | Interactive TUI. Default uses local poller; `--remote` uses daemon. |
| `status`                                             | One-shot dump of daemon state.                                      |
| `caffeinate on\|off\|toggle`                         | Drive the caffeinate manager.                                       |
| `nudge <selector> [--text=...]`                      | Signal a session via the daemon.                                    |
| `info <selector>`                                    | Print session or directory details.                                 |
| `agents-busy-check [--consider-daemon-down-as-busy]` | Exit 0 iff any agent is busy.                                       |
| `wait-until-agents-finished`                         | Block until all agents idle.                                        |
| `cmux-bridge`                                        | Long-running process inside a cmux pane; drives the sidebar.        |
| `config show`                                        | Print loaded config (read-only).                                    |

`<selector>` accepts `session:<id>`, `path:<workspace-path>`, `cmux:<workspace-id>`, or a bare value (slash → path, otherwise session).

## OpenTelemetry

When `OTEL_EXPORTER_OTLP_ENDPOINT` is set (typically by the workspace observability LaunchAgent env), the daemon emits:

- **Metrics** — sessions-by-state gauge, caffeinate-active gauge, block + week cost gauges, transition counters (block/week limit hits, caffeinate rounds + grace, context limit hits, nudges sent).
- **Logs** — structured event records for block start/end/limit-hit, week start/end/limit-hit, caffeinate start/stop/grace-expired, nudge.sent, session.state.changed.

The disabled-state contract: when the env var is unset, the OTel SDK is never initialised and every emit is a nil-receiver no-op.

A Grafana dashboard ships at `grafana/pa-monitor-overview.json` and is registered via `phillipgreenii.observability.dashboardProviders.pa-monitor` when observability is enabled.

## Labels

Generic label keys (the contract) populated by built-in detectors and shell-out decorators:

`workspace.terminal`, `workspace.scope`, `workspace.project`, `workspace.repo`, `agent.kind`, `agent.role`, `agent.mode`, `model`, `plan_tier`, `block.id`, `week.id`.

Consumer-specific values (e.g. `workspace.scope=zr`) come from external shell-out decorators registered via the nix module. Decorators must reside under `/nix/store/` — paths are `filepath.Clean`-canonicalised and prefix-checked.

## Storage (XDG)

| Purpose                           | Path                                                                                                 |
| --------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Config                            | `$XDG_CONFIG_HOME/pa-monitor/config.toml`                                                            |
| Unix socket                       | `$XDG_RUNTIME_DIR/pa-monitor/daemon.sock` (Linux) / `$XDG_STATE_HOME/pa-monitor/daemon.sock` (macOS) |
| PID lock                          | same directory as the socket, file `daemon.pid`                                                      |
| Runtime state (caffeinate toggle) | `$XDG_STATE_HOME/pa-monitor/runtime.json`                                                            |
| Daemon stderr (LaunchAgent)       | `$XDG_STATE_HOME/pa-monitor/launchd-stderr.log`                                                      |

## Configuration

`config.toml` is nix-rendered; see `internal/config/config.go` for keys. Defaults work for most users.

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
