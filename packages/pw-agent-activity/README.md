# pw-agent-activity

Wait for all AI agents to finish, from anywhere — including a stripped environment.

## Overview

`pw-agent-activity` is a thin wrapper that `exec`s `agent-activity-api wait`. It exists so the
"wait for the agents" operation has a short, stable command name that does not require the caller
to know which subcommand of which tool implements it.

## Usage

```bash
# Wait with defaults (2 hour max)
pw-agent-activity

# Wait with a custom timeout
pw-agent-activity --maximum-wait 3600

# Keep the Mac awake while waiting (macOS only)
pw-agent-activity --maximum-wait 3600 --caffeinate

# Show help / version
pw-agent-activity --help
pw-agent-activity --version
```

## Passthrough contract

Every argument is forwarded **unchanged** to `agent-activity-api wait`, so that tool owns the
option surface. This wrapper deliberately keeps no local option table — a copy would drift
silently the next time `agent-activity-api` gains, renames, or drops a `wait` option. Run
`agent-activity-api help` for the authoritative list.

Two arguments never reach the delegate:

| Argument          | Handled by                          | Why                                                                  |
| ----------------- | ----------------------------------- | -------------------------------------------------------------------- |
| `-h`, `--help`    | this wrapper's `show_help()`        | `agent-activity-api wait` defines no help option and exits 2         |
| `-v`, `--version` | the `mkBashScript`-injected handler | the builder reserves both spellings (see the `bash-scripting` skill) |

Both were errors before this wrapper handled them, so nothing that used to work changed.

## Exit codes

Passed through from `agent-activity-api wait`:

| Code | Meaning                            |
| ---- | ---------------------------------- |
| 0    | all agents finished                |
| 1    | timeout (`--maximum-wait` elapsed) |
| 2    | error (invalid arguments)          |

## Dependencies

`agent-activity-api` (from this repo's `agent-activity` aggregate) and `coreutils` are declared as
`runtimeDeps`, not assumed to be on the caller's `PATH`. That is the point: the command must work
from launchd, from `env -i`, and from a ccpool-spawned session, not only from an interactive login
shell.

## Tests

- `pw-agent-activity/tests/test-pw-agent-activity.bats` — passthrough contract against a stubbed
  `agent-activity-api`.
- `pw-agent-activity/tests/test-pw-agent-activity-real-agent-activity-api.bats` — the same contract
  against the REAL binary via `SCRIPT_UNDER_TEST`, so a subcommand rename or a missing
  `runtimeDeps` entry fails the check.
