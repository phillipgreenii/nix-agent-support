# pw-reset-agents

Clear stale AI agent activity markers, from anywhere — including a stripped environment.

## Overview

`pw-reset-agents` is a thin wrapper that `exec`s `agent-activity-api clean`. Sessions that die
without cleaning up leave activity markers behind; those markers make waiters such as
`pw-agent-activity` count finished sessions as still busy. This command clears them and reports how
many it removed.

It exists so the operation has a short, stable command name that does not require the caller to know
which subcommand of which tool implements it.

## Usage

```bash
# Clear every stale activity marker
pw-reset-agents

# Show help / version
pw-reset-agents --help
pw-reset-agents --version
```

## Passthrough contract

Every argument is forwarded **unchanged** to `agent-activity-api clean`, so that tool owns the
option surface. `clean` takes no options today, and this wrapper deliberately keeps no local option
table — a copy would drift silently the moment that changes. Run `agent-activity-api help` for the
authoritative command list.

Two arguments never reach the delegate:

| Argument          | Handled by                          | Why                                                                  |
| ----------------- | ----------------------------------- | -------------------------------------------------------------------- |
| `-h`, `--help`    | this wrapper's `show_help()`        | `agent-activity-api clean` defines no help option                    |
| `-v`, `--version` | the `mkBashScript`-injected handler | the builder reserves both spellings (see the `bash-scripting` skill) |

Neither did anything useful before this wrapper handled them, so nothing that used to work changed.

## Dependencies

`agent-activity-api` (from this repo's `agent-activity` aggregate) and `coreutils` are declared as
`runtimeDeps`, not assumed to be on the caller's `PATH`. That is the point: the command must work
from launchd, from `env -i`, and from a ccpool-spawned session, not only from an interactive login
shell.

## Tests

- `pw-reset-agents/tests/test-pw-reset-agents.bats` — passthrough contract against a stubbed
  `agent-activity-api`.
- `pw-reset-agents/tests/test-pw-reset-agents-real-agent-activity-api.bats` — the same contract
  against the REAL binary via `SCRIPT_UNDER_TEST`, so a subcommand rename or a missing
  `runtimeDeps` entry fails the check.
