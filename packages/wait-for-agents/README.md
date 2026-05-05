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

| Code | Meaning                                |
| ---- | -------------------------------------- |
| 0    | All agents finished (or none running)  |
| 1    | Timeout reached (agents still working) |
| 2    | Error (invalid arguments, etc.)        |

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
