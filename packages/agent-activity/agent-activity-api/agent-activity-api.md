# agent-activity-api

> Unified API for managing and monitoring AI agent activity.

More information: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps>.

- List all active agents with formatted output:

`agent-activity-api list`

- Wait for all agents to finish:

`agent-activity-api wait`

- Wait with custom timeout and keep Mac awake:

`agent-activity-api wait --maximum-wait 3600 --caffeinate`

- Check if any agent is currently active:

`agent-activity-api is-agent-active`

- Clean stale markers from all tools:

`agent-activity-api clean`

- Show help information:

`agent-activity-api help`

- Show version information:

`agent-activity-api version`
