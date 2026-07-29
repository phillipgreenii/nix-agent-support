# wait-for-agents

Wait for AI agents to finish working before proceeding.

## Overview

This package provides a script that blocks until all AI agents have completed their work. It's useful for:

- Preventing system shutdown while agents are working
- Keeping the Mac awake during agent execution (with `--caffeinate`)
- Automating workflows that depend on agent completion

## Usage

```bash
# Wait with defaults (2 hour max, check every 5 seconds)
wait-for-agents-to-finish

# Wait with custom timeout and interval
wait-for-agents-to-finish --maximum-wait 3600 --time-between-checks 10

# Keep Mac awake while waiting (macOS only)
wait-for-agents-to-finish --caffeinate

# Show help
wait-for-agents-to-finish --help
```

## Options

| Option                        | Description                                               | Default        |
| ----------------------------- | --------------------------------------------------------- | -------------- |
| `--maximum-wait SECONDS`      | Maximum time to wait before timing out                    | 7200 (2 hours) |
| `--time-between-checks SECS`  | Interval between activity checks                          | 5 seconds      |
| `--consecutive-idle-checks N` | Number of consecutive idle checks required before exiting | 3              |
| `--caffeinate`                | Keep Mac awake while waiting (macOS)                      | disabled       |
| `-h, --help`                  | Show help message                                         | -              |

## Exit Codes

| Code | Meaning                                         |
| ---- | ----------------------------------------------- |
| 0    | Idle reached (no agent actively running a turn) |
| 1    | Timeout reached (agents still working)          |
| 2    | Error (invalid arguments, etc.)                 |

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

Requires `claude-activity` package to query agent status.

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

1. Calls `claude-activity-api is-agent-active` in a loop
2. Exits when agents are idle or timeout is reached
3. Optionally uses `caffeinate -w $$` to prevent Mac sleep
4. Shows progress updates with active session count

The script relies on `claude-activity` to track agent sessions via Claude Code hooks.
