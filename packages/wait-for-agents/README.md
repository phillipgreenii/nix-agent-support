# wait-for-agents

Wait for AI agents to finish working before proceeding.

## Overview

This package provides a script that blocks until all AI agents have completed their work. It's useful for:

- Preventing system shutdown while agents are working
- Keeping the Mac awake during agent execution (with `--caffeinate`)
- Automating workflows that depend on agent completion

## Usage

```bash
# Wait with defaults (2 hour max)
wait-for-agents-to-finish

# Wait with a custom timeout
wait-for-agents-to-finish --maximum-wait 3600

# Require more consecutive idle observations before declaring idle
wait-for-agents-to-finish --consecutive-idle-checks 5

# Keep Mac awake while waiting (macOS only)
wait-for-agents-to-finish --caffeinate

# Show help
wait-for-agents-to-finish --help
```

## Options

| Option                        | Description                                                     | Default        |
| ----------------------------- | --------------------------------------------------------------- | -------------- |
| `--maximum-wait SECONDS`      | Maximum time to wait before timing out                          | 7200 (2 hours) |
| `--consecutive-idle-checks N` | Number of consecutive idle observations required before exiting | 3              |
| `--caffeinate`                | Keep Mac awake while waiting (macOS)                            | disabled       |
| `--time-between-checks SECS`  | **Accepted but ignored** — see "No poll interval" below         | -              |
| `-h, --help`                  | Show help message                                               | -              |
| `-v, --version`               | Show version information                                        | -              |

### No poll interval

`--time-between-checks` is still accepted so existing callers keep working, but it is **ignored**
(the wrapper prints a warning to stderr). `pa-monitor wait-until-agents-finished` observes the
daemon's `WatchState` push stream rather than polling, so there is no check-interval option to
forward. Tune the idle gate with `--consecutive-idle-checks` instead.

## Exit Codes

| Code | Meaning                                                                                                                        |
| ---- | ------------------------------------------------------------------------------------------------------------------------------ |
| 0    | Idle reached (no agent actively running a turn)                                                                                |
| 1    | Timeout reached (agents still working)                                                                                         |
| 2    | Daemon unavailable (also this wrapper's own arg checks: missing value, unknown option)                                         |
| 3    | A forwarded flag value rejected by `pa-monitor` itself (e.g. a non-numeric `--maximum-wait`/`--consecutive-idle-checks` value) |

### Exit 0 means "idle reached", not "work finished"

This wrapper delegates to `pa-monitor`, whose busy notion counts only sessions with
`status == working` (see `docs/adr/0024-pa-monitor-session-status-blocker-model.md` R3 and
`packages/pa-monitor/README.md` § "Busy/idle gates"). A session that is **`blocked`** — notably
blocked on the 5h/weekly usage limit, with work still pending — counts as **idle** here and does
**not** hold this wait open; it will resume on its own at the window reset.

Callers MUST therefore treat exit 0 as "nothing is actively progressing", not "all work is done". A
caller that MUST NOT proceed until pending work is genuinely finished MUST NOT rely on exit 0 alone;
it SHOULD additionally check the `blocked` count in `pa-monitor status` and re-wait after the usage
window resets. This is declared intent (ADR 0024 R3), not a defect in this wrapper.

## Dependencies

Requires the `pa-monitor` package, and a running `pa-monitor` daemon to answer the wait. With no
daemon reachable the wrapper exits 2 (`daemon unreachable`).

## Integration Examples

### With zm-stop-work

```bash
# Wait for agents before stopping work session
wait-for-agents-to-finish --caffeinate --maximum-wait 3600
task stop-work
```

### With Shutdown Scripts

```bash
#!/bin/bash
# Wait for agents before shutting down
if wait-for-agents-to-finish --maximum-wait 1800; then
    echo "All agents finished, proceeding with shutdown"
    sudo shutdown -h now
else
    echo "Timeout: agents still working"
    exit 1
fi
```

## How It Works

1. Translates its options into `pa-monitor wait-until-agents-finished` arguments and `exec`s it
2. `pa-monitor` streams `WatchState` from the daemon and exits 0 once no session has been `working`
   for `--consecutive-idle-checks` consecutive pushes, or 1 at `--maximum-wait`. Consecutive means
   consecutive **in time**: a gap of more than 2s between two pushes restarts the count (it prints
   `wait: <gap> unobserved, idle streak restarted`), because a session may have been `working` for
   the whole gap. See `packages/pa-monitor/README.md`'s "`--consecutive-idle-checks` counts
   observations that are consecutive in time".
3. With `--caffeinate`, also runs `caffeinate -w $$` so the Mac stays awake for the duration of the
   wait (the `exec` preserves the pid, so `caffeinate` exits with the wait)

The subcommand form is required, not cosmetic: `--wait-until-idle` was **removed** by
`docs/adr/0011-pa-monitor-daemon-otel-split.md`, and `pa-monitor` routes a leading flag to its TUI,
whose flag set would reject it with exit 2.
