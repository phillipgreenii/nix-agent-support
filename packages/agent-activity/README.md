# agent-activity

Unified API for managing and monitoring AI agent activity.

## Overview

Orchestrates claude-activity API to provide:

- Unified agent listing with formatted output
- Smart wait logic showing details when agent set changes
- Aggregated status checking
- Coordinated cleanup of stale markers

## Commands

### list

List all active agents with formatted output:

```bash
agent-activity-api list
```

Output:

```
TOOL      SESSION   STARTED         AGE     DIRECTORY          PROMPT
claude    cc40d87d  10:34:00 UTC    11h     ~/projects         Fix bug in login...
```

### wait

Wait for all agents to finish, showing full list when agent set changes:

```bash
agent-activity-api wait [OPTIONS]
```

Options:

- `--maximum-wait SECONDS` - Max wait time (default: 7200 = 2h)
- `--time-between-checks SECS` - Check interval (default: 5s)
- `--caffeinate` - Keep Mac awake (macOS only)

### is-agent-active

Check if any agent is active:

```bash
agent-activity-api is-agent-active && echo "busy" || echo "idle"
```

Exit codes:

- 0: At least one agent active
- 1: All agents idle

### clean

Clean stale markers from all tools:

```bash
agent-activity-api clean
```

Output:

```
Cleaned 4 sessions (4 claude)
```

## Dependencies

Requires:

- claude-activity package
- jq for JSON processing
- coreutils for date calculations
