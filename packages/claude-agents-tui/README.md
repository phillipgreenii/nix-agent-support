# claude-agents-tui

TUI and headless monitor for active Claude Code sessions.

## What it does

Renders per-session context usage, directory rollups, 5h billing block usage, burn rate, subagents, and shells in a live TUI. Also exposes a headless `--wait-until-idle` mode that replaces the previous `wait-for-agents-to-finish` blocking CLI, for use in shutdown scripts and automation that must wait for Claude to finish work. Includes reactive `caffeinate` on macOS to keep the machine awake only while agents are active.

## Quick start

```bash
# Interactive TUI
claude-agents-tui

# Headless: wait until all sessions are idle (replaces wait-for-agents-to-finish)
claude-agents-tui --wait-until-idle
```

## Keybindings

| Key          | Action                              |
| ------------ | ----------------------------------- |
| `up`/`down`  | Move cursor                         |
| `j`/`k`      | Move cursor (vim-style)             |
| `enter`      | Open session details                |
| `esc`        | Close session details               |
| `t`          | Toggle tokens vs. cost display      |
| `a`          | Toggle active-only vs. all sessions |
| `n`          | Toggle name vs. id display          |
| `C`          | Toggle caffeinate                   |
| `q`/`ctrl+c` | Quit                                |

## Status symbols

| Symbol | Meaning  |
| ------ | -------- |
| `●`    | Working  |
| `○`    | Idle     |
| `✕`    | Dormant  |
| `🤖`   | Subagent |
| `🐚`   | Shell    |

## Configuration

Config path: `~/.config/claude-agents-tui/config.toml` (respects `XDG_CONFIG_HOME`). If the file is missing, defaults apply.

| Key                       | Default  | Notes                                          |
| ------------------------- | -------- | ---------------------------------------------- |
| `plan_tier`               | `max_5x` | Options: `pro`, `max_5x`, `max_20x`            |
| `topup_pool_usd`          | `0`      | Top-up pool balance in USD                     |
| `topup_purchase_date`     | `""`     | ISO date of top-up purchase                    |
| `burn_window_short_s`     | `60`     | Short burn-rate window (seconds)               |
| `burn_window_long_s`      | `300`    | Long burn-rate window (seconds)                |
| `refresh_interval_ms`     | `1000`   | TUI refresh interval (milliseconds)            |
| `headless_interval_s`     | `5`      | Headless poll interval (seconds)               |
| `caffeinate_grace_s`      | `60`     | Grace period before releasing caffeinate       |
| `working_threshold_s`     | `30`     | Max age to consider a session "working"        |
| `idle_threshold_s`        | `600`    | Max age before a session is considered dormant |
| `consecutive_idle_checks` | `3`      | Consecutive idle polls required before exit    |
| `maximum_wait_s`          | `7200`   | Upper bound on headless wait (seconds)         |

## Headless mode

`--wait-until-idle` runs a polling loop that exits once all sessions have been idle for `consecutive_idle_checks` consecutive polls, or when `maximum_wait_s` elapses.

### Flags

| Flag                            | Purpose                                             |
| ------------------------------- | --------------------------------------------------- |
| `--wait-until-idle`             | Enable headless mode                                |
| `--maximum-wait SECONDS`        | Override `maximum_wait_s`                           |
| `--time-between-checks SECONDS` | Override `headless_interval_s`                      |
| `--consecutive-idle-checks N`   | Override `consecutive_idle_checks`                  |
| `--caffeinate`                  | Keep Mac awake for the duration of the wait (macOS) |
| `--version`                     | Print version and exit                              |
| `--help`                        | Print flag list and exit                            |

### Exit codes

| Code | Meaning                                         |
| ---- | ----------------------------------------------- |
| `0`  | All sessions idle                               |
| `1`  | Timed out (hit `maximum_wait_s`)                |
| `2`  | Error (config load failure, unexpected failure) |

## ccusage dependency

`claude-agents-tui` invokes `ccusage` as a subprocess (`ccusage blocks --active --json --offline`) to fetch 5h billing-block cost and burn rate.

The Nix package in this repo wraps `ccusage` (built from `packages/ccusage`) onto the binary's `PATH` at install time, so no manual install is required — `nix build .#claude-agents-tui` or `programs.claude-agents-tui.enable = true` gives you a working TUI out of the box.

If you are running a raw `go run` / `go build` binary outside Nix, either install `ccusage` on your `PATH` (`npm i -g ccusage`) or accept the "5h Block (unavailable)" fallback in the header.

`ccusage` can take ~5s to parse a busy `~/.claude/projects/` tree; poll timeouts in the TUI and headless modes are set to 10s to accommodate this.

## caffeinate (macOS)

Caffeinate behavior is reactive: the TUI spawns `caffeinate -i -w <pid>` only while at least one session is actively working, and releases it after `caffeinate_grace_s` of idle. Pressing `C` toggles caffeinate on/off manually. In headless mode, `--caffeinate` spawns caffeinate for the entire wait window.

## Design

See `docs/plans/2026-04-23-claude-agents-tui-design.md` for the full design document.

## Dependencies

Go deps are not vendored. The Nix build uses `vendorHash` to fetch modules reproducibly. After changing Go dependencies, refresh the hash:

```bash
./update-deps.sh
```
