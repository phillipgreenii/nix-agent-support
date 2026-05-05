# claude-activity

Claude Code activity tracking via hooks with CLI API.

## Overview

Track Claude Code agent activity using hooks and provide a CLI API for querying session state. Stores session data in XDG-compliant directories and handles stale file cleanup.

## Components

### Scripts

- **claude-work-start**: Hook script for `UserPromptSubmit` event - creates session file
- **claude-work-end**: Hook script for `Stop` event - removes session file
- **claude-activity-api**: CLI API for querying and managing sessions

### API Commands

```bash
# Check if agents are working
claude-activity-api is-agent-active && echo "working" || echo "idle"

# List active sessions
claude-activity-api list

# Clean up stale files
claude-activity-api clean
```

## Session Storage

Session files are stored at:

```
${XDG_STATE_HOME:-$HOME/.local/state}/claude-activity/
```

Files are considered stale if:

- They are older than `CLAUDE_ACTIVITY_MAX_AGE` minutes (default: 720), OR
- No Claude processes are running

## Environment Variables

- `XDG_STATE_HOME`: Base directory for state files (default: `~/.local/state`)
- `CLAUDE_ACTIVITY_MAX_AGE`: Maximum age in minutes before stale (default: 720)

## Integration

This package is designed to work with the `claude.hooks` home-manager module to automatically track Claude agent activity.
