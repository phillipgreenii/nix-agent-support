# pg-assistants

A minimal per-user supervisor for "permanent" Claude Code assistants. It keeps each configured
assistant running in its own tmux session (on a dedicated server, `tmux -L pg-assistants`) and
recycles a session through a nudge, interrupt, kill ladder driven by its context fill and the
account's usage blocks.

A permanent assistant is a chain of ordinary interactive Claude Code sessions in one working
directory: each session works, keeps its notes in that directory and exits before its context
fills, and the next session picks up from the notes. The assistant's identity and duties live in
that directory's `AGENTS.md` / `CLAUDE.md`; pg-assistants knows nothing about them. The decision
record is ADR 0128 (`docs/adr/0128-pg-assistants-minimal-supervisor-for-permanent-assistants.md`).

## Usage

The home-manager module `phillipgreenii.programs.pg-assistants` (`home/programs/pg-assistants`)
renders the configuration, checks it at build time, installs the command, marks every assistant's
directory trusted at activation and, on darwin, registers the daemon as the launchd agent
`com.phillipg.pg-assistants-daemon`. It needs `phillipgreenii.programs.pg-claude-control` enabled
with its plugin on (both asserted), and phillipgreenii-nix-personal's home module for the launchd
registry. Nothing in this repo enables it; a consuming flake does:

```nix
phillipgreenii.programs.pg-assistants = {
  enable = true;
  assistants.inbox-keeper = {
    name = "Inbox Keeper";
    model = "claude-sonnet-5-5";
    cwd = "/Users/example/assistants/inbox-keeper";
    prompt = "Resume your duties per AGENTS.md.";
    context.limit = 200000;
  };
};
```

Then put the `AGENTS.md` block below in the assistant's directory, deploy, and check it with
`pg-assistants status`.

### Status

```bash
pg-assistants status           # header (daemon pid and age, block readings) and one row per assistant
pg-assistants status --json    # the status file as it is
pg-assistants status --check   # the health probe: exit 3 down, 5 degraded, 0 healthy
```

The table is `ID ENABLED RUNNING CONTEXT LADDER WAIT NOTE`. `WAIT` says why an enabled assistant
is not running (`backoff`, `block_hold` or `missing_cwd`, with the time it waits until), and `NOTE`
shows the first of: a config error, `stalled` (the loop has not ticked for `tick + grace.kill +
45s`), a hint (for example "control mod not answering (shell startup waiting? trust dialog? plugin
disabled?)"), the last error, the last exit. A session on the assistants' tmux server that no valid
config entry supervises is listed with the note `not in config` and is stopped. When the status
file is missing or over a minute old, `status` warns naming
`launchctl print gui/<uid>/com.phillipg.pg-assistants-daemon` and the launchd stderr log,
`$XDG_STATE_HOME/pg-assistants-launchd/launchd-stderr.log`. The last output of a session that
ended is in `<state dir>/last-exit/<id>.txt`.

### Attaching

`pg-assistants attach <id>` attaches to the assistant's session on `tmux -L pg-assistants`; it
works from inside the operator's own tmux too. Detach with `C-b d`: the assistant keeps running.
Typing into the session is allowed (the session is an ordinary interactive claude), but the daemon
never does it.

### Removing an assistant

Set its `enabled = false`, deploy, wait until `pg-assistants status` shows it stopped, then delete
the entry and deploy again. Deleting the entry at once also works: the orphan sweep stops the
session that no entry supervises. Renaming an assistant's id is a removal plus an addition.

### Uninstalling

Turn the module off and deploy, then stop the sessions by hand:

```bash
tmux -L pg-assistants kill-server
```

`AbandonProcessGroup` keeps the sessions alive when the daemon stops, by design (a deploy or a
daemon restart MUST NOT kill the assistants), so nothing else stops them. The state directory
(`$XDG_STATE_HOME/pg-assistants`) and the launchd log directory
(`$XDG_STATE_HOME/pg-assistants-launchd`) may then be deleted.

### The assistant's `AGENTS.md`

Copy this block into each assistant's `AGENTS.md` (or `CLAUDE.md`), beside its own duties:

```markdown
## Running as a permanent assistant

This directory is a permanent assistant supervised by pg-assistants. Each session is one link in
a chain: it ends, and the next session starts from the notes left in this directory.

- Keep your notes in this directory: what you are doing, what is done, and what comes next. Read
  them first in every session.
- Nobody is attached to this session. Never wait for input and never end a turn on a question:
  decide, write any open question into your notes, and carry on.
- When pg-assistants nudges you to stop, or your work for now is done: finish the current step,
  write your notes, then run `pg-assistants exit` as a command on its own (never chained with
  `&&`, `;` or a `cd`) as your last action, and end your turn.
- If there is nothing to do, write your notes, run `pg-assistants exit` the same way, and end your
  turn: the next session starts on the next tick.
```

An assistant that should poll less often (it finds nothing to do on most starts) gets a longer
`tick` in its config entry, rather than a wait inside the session.

## Commands

| Command                                         | What it does                                                                         |
| ----------------------------------------------- | ------------------------------------------------------------------------------------ |
| `daemon --config PATH [--shutdown-deadline 8s]` | Run the supervisor (launchd keeps it alive).                                         |
| `status [--json] [--check] [--config PATH]`     | Show each assistant's state; `--check` is the health probe.                          |
| `attach <id> [--config PATH]`                   | Attach to an assistant's session on `tmux -L pg-assistants` (detach with `C-b d`).   |
| `exit`                                          | Inside a session: ask the daemon to stop it (run it on its own, then end the turn).  |
| `trust --claude-json PATH <cwd>...`             | Mark directories trusted (by resolved path) in Claude's `.claude.json` (merge-only). |
| `config check --config PATH`                    | Validate a configuration file strictly, without changing a thing.                    |

`status` and `attach` read `--config`, by default `$XDG_CONFIG_HOME/pg-assistants/config.toml`.
Every command answers `--help`.

Exit codes (`x/exitcode`):

- `config check` exits 0 printing `ok: <n> assistants`, 7 printing each problem on its own line
  (`<field>: <message>`), and 2 on a usage error (including a missing `--config`).
- `daemon` exits 7 only when the file cannot be read or parsed or a `[paths]` entry is invalid
  (an assistant's problem or a bad global field is logged and the rest runs), 1 when the state
  dir, its `tmux.conf` or the events log cannot be created, 10 when a shutdown overran its
  deadline, and 2 on a usage error. It writes `<state dir>/status.json` and
  `<state dir>/events.jsonl` (10 MiB, one `.1` backup); its own events are `daemon_start` (pid,
  version, config path and hash, the effective `PG_CLAUDE_CONTROL_STATE_DIR` and
  `CLAUDE_CONFIG_DIR`), `config_problem` for each global problem, and `status_write_failed` when
  the final status write fails. `PG_ASSISTANTS_PPROF_ADDR` opens the opt-in pprof endpoint.
- `status` exits 0, warning on stderr (naming `launchctl print gui/<uid>/com.phillipg.pg-assistants-daemon`
  and `$XDG_STATE_HOME/pg-assistants-launchd/launchd-stderr.log`) when the file is missing or over
  a minute old. `status --check` prints one line and exits 3 when the file is missing, unreadable
  or stale, 5 when degraded (a config error or problem, a hint, a stalled loop, a crash backoff,
  or an orphan session), 0 otherwise.
- `attach` exits 4 when the server has no such session, 3 when no assistants' tmux server runs.
- `exit` exits 2 when `PG_ASSISTANT_ID` or `PG_ASSISTANTS_STATE_DIR` is missing or invalid.
- `trust` exits 1 printing the error when the file cannot be read or written.

## The launchd agent

On darwin the module registers `com.phillipg.pg-assistants-daemon` (keep-alive, run at load):

```text
exec <login shell> -l -c 'exec "$@"' <login shell> <pkg>/bin/pg-assistants daemon --config <rendered file> --shutdown-deadline 8s
```

The daemon runs through the operator's login shell so that it, and every `pg-claude-control`
call it makes, sees the operator's session variables. `AbandonProcessGroup` is set, so stopping or
restarting the daemon leaves the tmux server and the assistants running, and the next daemon adopts
them. `ExitTimeOut` is the 8 s shutdown deadline plus a 7 s margin. Its environment carries `HOME`,
`PG_ASSISTANTS_CONFIG_HASH` (a hash of the rendered file, so a config change restarts the daemon),
`PG_ASSISTANTS_SESSION_VARS_HASH` (a hash of `home.sessionVariables`, so a change of a session
variable restarts it too) and, with `pprof.enable`, `PG_ASSISTANTS_PPROF_ADDR`. The tmux server
keeps this environment and hands it to every session, so these names are in `blank_env`. launchd's own stdout and stderr go to
`$XDG_STATE_HOME/pg-assistants-launchd/`, outside the daemon's 0700 state directory. Linux has no
supervisor: the module registers nothing and warns.

## Events

The daemon writes one JSON line per event to `<state dir>/events.jsonl`, shaped
`{ts, level, event, service, assistant, session_id, pid, reason, error, uptime_s, ...}`
(`service` is `pg-assistants`):

| Event                                                                                                        | When                                                                                               |
| ------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------- |
| `daemon_start`                                                                                               | the daemon started (pid, version, config path and hash, the effective control and Claude dirs)     |
| `start`, `start_failed`, `start_stalled`                                                                     | a session was started, could not be (`step` names why), or never answered `usage`                  |
| `adopt`                                                                                                      | a session found running at the daemon's first tick was taken over                                  |
| `exited`                                                                                                     | a session ended (status, signal, uptime, whether it asked to exit; output in `last-exit/<id>.txt`) |
| `nudge`, `interrupt`, `kill`, `force_kill`, `graceful_stop`                                                  | a ladder action worked (`reason` names the meter or the ladder)                                    |
| `action_failed`                                                                                              | an action failed; `action` names it (a ladder action, or `list_sessions` for the orphan sweep)     |
| `usage_failed`, `window_mismatch`                                                                            | `usage` failed; the engine's context window differs from the configured one                        |
| `config_problem`                                                                                             | a global field failed validation and fell back to its default                                      |
| `orphan`                                                                                                     | a session no valid config entry supervises was found                                               |
| `loop_panic`                                                                                                 | a loop panicked (with the stack); it restarts after the crash backoff                              |
| `status_write_failed`                                                                                        | the status file could not be written                                                               |
| `shutdown_signal`, `shutdown_phase`, `shutdown_complete`, `shutdown_overrun`, `loop_started`, `loop_stopped` | `x/lifecycle`'s loop and shutdown events                                                           |

## Configuration

TOML, rendered by the home-manager module. Durations are Go duration strings (`"30s"`, `"5m"`)
and MUST be positive; points are fractions.

```toml
tick = "1m"                   # default 1m; an assistant MAY override it
blank_env = ["__HM_SESS_VARS_SOURCED"]   # default none: set empty in every session

[grace]                       # time-based ladder steps, for every ladder
interrupt = "5m"              # default 5m: nudge -> interrupt
kill = "2m"                   # default 2m: interrupt -> kill

[blocks]                      # fractions of the account's 5-hour or 7-day block
nudge = 0.95                  # defaults 0.95 / 0.98 / 1.00
interrupt = 0.98
kill = 1.00
unknown_reset_hold = "1h"     # default 1h; also for a reset past the block's length + 1h

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
- Whether `cwd` exists is NOT checked here (the build sandbox cannot see it). Two `cwd` values
  that resolve to the same existing directory through a symlink count as shared; folder trust is
  read and written under the resolved directory, the key Claude looks up.
- `blank_env` lists environment variable names (`[A-Za-z_][A-Za-z0-9_]*`) that every session
  starts with set and empty. The tmux server keeps the environment of the daemon that started it,
  and the daemon runs under the operator's login shell, which has already run the startup files and
  exported their run-once guards; every session starts from the server's environment, so without
  this its own login shell would skip those files and keep the `PATH` and session variables of the
  server's start. An empty guard runs the file again. It also blanks variables meant for the
  daemon alone. An assistant's own `env` wins over a name listed here, and the daemon's fixed
  entries (`DISABLE_AUTO_COMPACT`, `PG_ASSISTANT_ID`, `PG_ASSISTANTS_STATE_DIR`, the `CLAUDE*`
  markers) cannot be blanked. A variable removed from the startup files stays in the server's
  environment until the server restarts (`tmux -L pg-assistants kill-server`).

### Module options

Every key is rendered from an option under `phillipgreenii.programs.pg-assistants`; a null option
is omitted. The module renders only the keys above (an unknown key fails the build).

| Option                                                            | Key                                          | Default                                                                                       |
| ----------------------------------------------------------------- | -------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `tick`, `grace.{interrupt,kill}`                                  | `tick`, `[grace]`                            | `1m`, `5m`, `2m`                                                                              |
| `sessionBlankEnv`                                                 | `blank_env`, then the daemon-only names      | `__HM_SESS_VARS_SOURCED`, `__HM_ZSH_SESS_VARS_SOURCED`, `__NIX_DARWIN_SET_ENVIRONMENT_DONE`   |
| `blocks.{nudge,interrupt,kill,unknownResetHold}`                  | `[blocks]`                                   | 0.95, 0.98, 1.00, `1h`                                                                        |
| `restart.{minUptime,backoffInitial,backoffMax,startTimeout}`      | `[restart]`                                  | `1m`, `1m`, `30m`, `5m`                                                                       |
| (none)                                                            | `paths.tmux`, `paths.pg_claude_control`      | the `tmux` package; the `pg-claude-control` module's package                                  |
| `claudePath`                                                      | `paths.claude`                               | the `claude` of `pkgs.llm-agentsPkgs.claude-code` (what `home/programs/claude-code` installs) |
| `loginShell`                                                      | `paths.shell`                                | `/bin/zsh` (MUST be absolute; asserted)                                                       |
| (none)                                                            | `paths.state_dir`                            | `$XDG_STATE_HOME/pg-assistants`, expanded at evaluation                                       |
| `claudeJson`                                                      | `paths.claude_json`                          | `$CLAUDE_CONFIG_DIR/.claude.json` when that session variable is set, else `~/.claude.json`    |
| `assistants.<id>.{name,enabled,model,effort,cwd,prompt,tick,env}` | `[[assistant]]` (`id` is the attribute name) | `name` the id, `enabled` true, `effort` `medium`, `tick` and `env` omitted                    |
| `assistants.<id>.context.{limit,window,nudge,interrupt,kill}`     | `[assistant.context]`                        | `limit` required, `window` omitted, 0.80, 0.95, 1.00                                          |
| `pprof.enable`                                                    | (the agent's `PG_ASSISTANTS_PPROF_ADDR`)     | true: `<state dir>/pprof.sock`                                                                |

`blank_env` is `sessionBlankEnv` followed by the variables the launchd agent sets for the daemon
alone (`PG_ASSISTANTS_CONFIG_HASH`, `PG_ASSISTANTS_PPROF_ADDR`, `PG_ASSISTANTS_SESSION_VARS_HASH`)
and launchd's `XPC_SERVICE_NAME`; replacing `sessionBlankEnv` keeps those. The default guards are
home-manager's (`hm-session-vars.sh` returns when `__HM_SESS_VARS_SOURCED` is non-empty, and
`~/.zshenv` skips its zsh session variables when `__HM_ZSH_SESS_VARS_SOURCED` is non-empty) and
nix-darwin's (`/etc/zshenv` sources `set-environment` only when
`__NIX_DARWIN_SET_ENVIRONMENT_DONE` is empty). `context.nudge` and `context.interrupt` defaults are
the module's: the file itself has none, so a hand-written file MUST set them. Every `[paths]` entry is a literal absolute path: a `$HOME` in
`CLAUDE_CONFIG_DIR` is expanded at evaluation, because the daemon reads the file where no shell
expands anything. `model`, `cwd` and `prompt` have no default.

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
