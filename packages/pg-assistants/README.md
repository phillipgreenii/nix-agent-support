# pg-assistants

A minimal per-user supervisor for "permanent" Claude Code assistants. It keeps each configured
assistant running in its own tmux session (on a dedicated server, `tmux -L pg-assistants`) and
recycles a session through a nudge, interrupt, kill ladder driven by its context fill and the
account's usage blocks.

This build carries only `config check`. The daemon and the `status`, `attach`, `exit` and `trust`
subcommands are added in later changes.

## Commands

| Command                      | What it does                                                      |
| ---------------------------- | ----------------------------------------------------------------- |
| `config check --config PATH` | Validate a configuration file strictly, without changing a thing. |

`config check` exits 0 printing `ok: <n> assistants`, 7 printing each problem on its own line
(`<field>: <message>`), and 2 on a usage error (including a missing `--config`).

## Configuration

TOML, rendered by the home-manager module. Durations are Go duration strings (`"30s"`, `"5m"`)
and MUST be positive; points are fractions.

```toml
tick = "1m"                   # default 1m; an assistant MAY override it

[grace]                       # time-based ladder steps, for every ladder
interrupt = "5m"              # default 5m: nudge -> interrupt
kill = "2m"                   # default 2m: interrupt -> kill

[blocks]                      # fractions of the account's 5-hour or 7-day block
nudge = 0.95                  # defaults 0.95 / 0.98 / 1.00
interrupt = 0.98
kill = 1.00
unknown_reset_hold = "1h"     # default 1h

[restart]
min_uptime = "1m"             # default 1m
backoff_initial = "1m"        # default 1m
backoff_max = "30m"           # default 30m
start_timeout = "5m"          # default 5m

[paths]                       # every entry is required and absolute
tmux = "/nix/store/.../bin/tmux"
claude = "/nix/store/.../bin/claude"
pg_claude_control = "/nix/store/.../bin/pg-claude-control"
shell = "/bin/zsh"
state_dir = "/Users/<user>/.local/state/pg-assistants"
claude_json = "/Users/<user>/.claude.json"

[[assistant]]
id = "example-a"              # unique; [a-z0-9-]+; the tmux session name
name = "Example A"            # claude --name
enabled = true                # default true
model = "claude-sonnet-5-5"   # claude --model
effort = "medium"             # low, medium, high, xhigh or max
cwd = "/abs/path"             # absolute; one assistant per directory
prompt = "Resume your duties per AGENTS.md."   # first message; MUST NOT start with "-"
tick = "30s"                  # optional override
env = { EXAMPLE = "value" }   # optional extra session environment

[assistant.context]
limit = 200000                # tokens, > 0
window = 1000000              # optional; required only for a model the table does not know
nudge = 0.80                  # 0 < nudge < interrupt <= kill
interrupt = 0.95
kill = 1.00                   # default 1.00; MAY exceed 1.0
```

Rules beyond the comments above:

- `limit * kill` MUST be strictly below the model's context window: with auto-compact off, a kill
  point at or past the window never fires. The window comes from the table of exact model ids in
  `internal/config/windows.go` (no family fallback; a `[1m]` suffix on a known id selects 1M). An
  unknown model needs `context.window`, which is accepted for any model.
- An unknown key is a problem, so a misspelt key cannot silently run on a default.
- A value that reaches tmux as an argument (`name`, `model`, `cwd`, `prompt`, an `env` value, and
  `paths.shell`, `paths.claude`, `paths.state_dir`) MUST NOT end in `\;`. tmux reads an argument
  ending in `;` as the end of a command, so the daemon sends a trailing `;` as `\;`; a value that
  already ends in `\;` cannot be passed on intact. A `;` anywhere else, or a trailing `;`, is fine.
- `cwd` MUST NOT contain `#`: tmux expands a format in the start directory (`##`, `#{...}`), and a
  directory it then cannot find silently starts the session in `$HOME`.
- Whether `cwd` exists is NOT checked here (the build sandbox cannot see it).

Where the rules apply:

- `config check` (run at build time) is strict: any problem fails it.
- The daemon isolates problems per assistant: an assistant with a problem is not started and the
  others run. A failing global field falls back to its default and is reported. Only a file that
  cannot be read or parsed, or an invalid `[paths]` entry, stops the daemon.

## Development

```bash
cd packages/pg-assistants
nice -n 10 go test -p 4 ./...      # the unit tier
./update-deps.sh                   # after a dependency change: go mod tidy + gomod2nix.toml
```

The whole-module test gate is `nix build .#checks.<system>.pg-assistants-go-tests`; `nix build
.#pg-assistants` only proves the binary compiles. `TestCallsBounded` guards that every
`cmdrun.Call` in the module sets a timeout.

The real-tool tests (a real tmux server; the real launch chain, through `/bin/sh` and a `zsh`
login shell to a stand-in `claude`; the real `pg-claude-control` against a temporary session
registry and an inbox the test answers) are `integration`-tagged and run on demand in
`nix build .#checks.<system>.pg-assistants-integration-tests`, never in the unit tier. To run them
by hand, point `PG_ASSISTANTS_TEST_PG_CLAUDE_CONTROL` at a `pg-claude-control` built from this
repo (`go build -o <dir>/pg-claude-control ./cmd/pg-claude-control` in `packages/pg-claude-control`)
and set `PG_ASSISTANTS_REQUIRE_TOOLS=1` so a missing tool fails instead of skipping:

```bash
PG_ASSISTANTS_REQUIRE_TOOLS=1 PG_ASSISTANTS_TEST_PG_CLAUDE_CONTROL=<dir>/pg-claude-control \
  nice -n 10 go test -p 4 -tags integration ./...
```
