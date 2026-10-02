# pg-rescue

`pg-rescue` wraps a command. When the command fails, it runs a named chain of failure handlers. A
handler is an independent executable behind one contract: it reads the failure report from
`$PG_RESCUE_REPORT`, logs to stderr, and answers with an exit code (`0` resolved, `2` declined, `3`
deferred, anything else failed) plus an optional result JSON object on stdout.

The design of record is under `docs/superpowers/specs/` at the repo root. This README documents
what is built: the wrapper's contract, display, config, run directory and run log, the template
data model the three reference handlers share, how to write a handler, and `pg-rescue-claude`.

- [The wrapper](#the-wrapper)
- [Handler contract](#handler-contract)
- [Display](#display)
- [Config](#config)
- [Run directory](#run-directory)
- [Run log](#run-log)
- [Template data model](#template-data-model)
- [Writing a handler](#writing-a-handler)
- [pg-rescue-claude](#pg-rescue-claude)

## The wrapper

```text
pg-rescue (--handlers a,b,c | --chain NAME) [-C DIR] [--context TEXT] [--verify CMD]
          [--result-file F] [-q | -v | -vv] [--config PATH] -- cmd args...
pg-rescue --stdin (--handlers ... | --chain ...) --verify CMD [-C DIR] [--context TEXT]
          [--result-file F] [-q | -v | -vv] [--config PATH]
pg-rescue result <resolved|deferred|declined> [SUMMARY] [--details-file F] [--meta-file F]
pg-rescue check [--chain NAME] [--config PATH]
```

- Exactly one of `--handlers` and `--chain` is required. There is no default chain.
- Everything after `--` is the command's argv. It runs directly, with no shell; write
  `-- sh -c '...'` to use shell syntax.
- `-C DIR` runs the command, the handlers and verify in `DIR`. Use it instead of the command's own
  directory flag (`git -C`), or the report and the handlers point at the wrong tree.
- `--verify CMD` runs through `sh -c` in the command's cwd after a handler claims `resolved`. Without
  it the original argv is re-run. There is no way to skip verification; `--verify true` trusts the
  handler.
- `--stdin` treats stdin as the failure output instead of running a command. `--verify` is required.
- `--result-file F` writes the run's [run log](#run-log) line to `F` as one JSON object. It tells a
  reserved exit code (`70`, `75`) apart from a command that exited with the same number. If `F`
  cannot be written, a warning is printed (not under `-q`) and the exit code is unchanged.
- `-q`, `-v`, `-vv` set the [display](#display) level.

### Exit codes

| Situation                                                        | Exit                                                                                                          |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| Command succeeded                                                | `0`                                                                                                           |
| A handler returned `resolved` and verify passed                  | `0`                                                                                                           |
| A handler returned `deferred`                                    | `75`                                                                                                          |
| Every handler declined or failed                                 | the command's own code; `128+signo` if a signal it did not get from the wrapper killed it; `1` with `--stdin` |
| Wrapper error (CLI, config, run directory, spawning the command) | `70`, before the command runs                                                                                 |
| The wrapper received `SIGINT`, `SIGTERM` or `SIGHUP`             | `130`, `143` or `129`; the chain stops                                                                        |

Writing the display, the run log, `display.log` or `--result-file` never changes the exit code.

## Handler contract

A handler is any executable named in the config. The wrapper runs it in the command's cwd with stdin
on `/dev/null`, in its own process group, and kills what is left of that group when it exits.

| Handler exit                                      | Outcome    | Chain             |
| ------------------------------------------------- | ---------- | ----------------- |
| `0`                                               | `resolved` | stop, then verify |
| `2`                                               | `declined` | continue          |
| `3`                                               | `deferred` | stop              |
| anything else (timeout, signal, spawn failure...) | `failed`   | continue          |

The handler reads the failure report at `$PG_RESCUE_REPORT` and may print one JSON object on stdout:
`{"outcome", "summary", "details", "meta"}`, all optional. `outcome`, if present, must agree with the
exit code. Anything else on stdout (two objects, trailing text, duplicate keys, a wrong field type,
more than 1 MiB) makes the attempt `failed`. Handlers log to stderr.

The wrapper sets these variables for every handler and for verify:

| Variable                                                                                                    | Value                                                 |
| ----------------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| `PG_RESCUE_REPORT`, `PG_RESCUE_OUTPUT_FILE`, `PG_RESCUE_RUN_DIR`, `PG_RESCUE_RUN_LOG`                       | absolute paths                                        |
| `PG_RESCUE_CMD`, `PG_RESCUE_EXIT`                                                                           | the quoted argv and its exit; empty in `--stdin` mode |
| `PG_RESCUE_CONTEXT`, `PG_RESCUE_FINGERPRINT`, `PG_RESCUE_RUN_ID`, `PG_RESCUE_HANDLER`, `PG_RESCUE_POSITION` | as named                                              |
| `PG_RESCUE_DEPTH`, `PG_RESCUE_PARENT_RUN_ID`                                                                | nesting; nothing limits it                            |

## Display

The display tells the operator what the chain did. It is written to stderr only, and only when a
handler ran: the happy path prints nothing, so the command looks the same with or without the
wrapper.

- Tool text appears only on delimiter lines: `== ... ==` for sections and `-- ... --` for
  sub-sections. Text a handler, the command or verify produced is printed flush-left on lines of its
  own, so it can be copied. The display first ends a line the command left open, then leaves a blank
  line, so its header is never glued to command output. A blank line comes before every handler
  section.
- `[i/n]` is the handler's position in a chain of length `n`.
- There is no colour.
- Text a handler reported (`summary`, `details`, `meta`, stderr) and verify output are sanitized
  before they are shown: whole ANSI escape sequences are removed, every other C0 control character
  (but newline and tab), DEL and C1 control character is stripped, and a line that would read as a
  delimiter line is defanged with a leading space. A handler can neither emit terminal escapes nor
  forge a section boundary. The command's own passthrough is left untouched.
- Only the first line of a `summary` is shown.
- The config's `redact` patterns are applied to everything shown, after sanitizing (so a control
  character cannot split a secret and slip past them). The command's own passthrough is not redacted.

| Level   | Command output | When a handler ran                                                                                                     |
| ------- | -------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `-q`    | passed through | nothing from pg-rescue                                                                                                 |
| default | passed through | header; per attempted handler a delimiter `[i/n] name: outcome (reason)` and its summary; a passed-verify line; footer |
| `-v`    | passed through | the default, plus `details`, verify output (and failed verifies), skipped handlers, and the run directory              |
| `-vv`   | passed through | the `-v` output, plus each handler's argv, exit code, duration, timeout, `description`, `meta` and stderr              |

`-q` silences pg-rescue's own text, warnings included. It does not silence a wrapper error (exit
`70`), which is printed before anything runs.

Whatever the level, whenever a handler ran, the display rendered at `-vv` is also written to
`<run-dir>/display.log` (without the leading blank line), so "what happened in run X" can always be
answered, even under `-q` or pg-router.

A run with the design's example chain looks like this at the default level:

```text
<command output, unchanged>

== pg-rescue: `git pull --rebase` exited 1 · chain sync ==

== [1/5] flake-lock-conflict: declined (exit 2) ==
Conflict spans source files, not just flake.lock

== [2/5] fix-small: failed (verify failed: exit 128) ==
Resolved the conflict markers and continued the rebase

== [3/5] fix-large: resolved ==
Finished the half-continued rebase; kept both sides of the README conflict
-- verify: passed (exit 0) --

== resolved by fix-large · run 20261002T140311Z-7f3a9c2e ==
```

The footer names the deciding handler and the run id, and the exit code when it is not `0`:
`deferred by H · exit 75 · working copy left as-is`, `unhandled · exit N`, and
`interrupted during H · working copy may be mid-change · exit 130`. With `--handlers` the header
reads `handlers a,b,c` where a chain reads `chain NAME`; with `--stdin` it reads `stdin input`. The
golden files under `internal/runner/testdata/golden/` show every result at every level.

## Config

Lookup order: `--config PATH`, then `$PG_RESCUE_CONFIG`, then
`${XDG_CONFIG_HOME:-~/.config}/pg-rescue/config.toml`. The whole file is validated at every start; a
config error exits `70` before the command runs and names the file, the table and the key.

```toml
redact = ['ghp_[A-Za-z0-9]{36}', 'Authorization: \S+']

[handler.fix-small]
command = ["pg-rescue-claude", "--model", "haiku", "--time-limit", "2m"]
timeout = "3m"
description = "haiku, 2m"
tags = ["agent"]

[chain.sync]
handlers = ["flake-lock-conflict", "fix-small", "p1-later", "notify"]
```

| Key                        | Meaning                                                                                                |
| -------------------------- | ------------------------------------------------------------------------------------------------------ |
| `redact`                   | Go RE2 regexes; matches become `[REDACTED]` in every copy of captured text (report, display, run log)  |
| `handler.NAME.command`     | argv, required; `command[0]` is looked up on `PATH` when the handler is spawned                        |
| `handler.NAME.timeout`     | Go duration, default `5m`; SIGTERM to the handler's process group, SIGKILL 5s later; the attempt fails |
| `handler.NAME.env`         | non-secret variables for the handler; never displayed or logged                                        |
| `handler.NAME.env_file`    | dotenv file for secrets; a group- or world-readable file is a config error                             |
| `handler.NAME.description` | shown at `-vv`                                                                                         |
| `handler.NAME.tags`        | free-form; copied into each run-log attempt for measurement                                            |
| `chain.NAME.handlers`      | non-empty list of instance names                                                                       |

`pg-rescue check` validates the config the same way and lists every handler with its resolved
`command[0]` or `NOT FOUND`; it exits `1` if a binary is missing and `70` on a config error.

## Run directory

`${XDG_STATE_HOME:-~/.local/state}/pg-rescue/runs/<run-id>/` holds `report.json`, `output.log`,
`display.log` and the per-attempt stderr, verify and details files.

- The wrapper sets umask `077`: the state root and every run directory are `0700`, every file
  `0600`, whatever the user's umask.
- The run id is a UTC timestamp and 32 random bits, `20261002T140311Z-7f3a9c2e`. The directory is
  made with `mkdir` and a new id is drawn on collision. If it cannot be created the wrapper exits
  `70` before the command runs.
- The running wrapper holds an `flock` on `<run-dir>/.lock`.
- The directory is removed on the happy path (and after a wrapper error) and kept when a handler ran
  or the run was interrupted.
- At the start of each run the wrapper prunes, best effort. A directory is deleted only if `Lstat`
  shows a real directory directly under `runs/` (nothing is followed), its name matches the run-id
  pattern, the timestamp in its id is more than 7 days old, and its lock is not held.
- A deferring handler must copy what its hand-off needs into the hand-off itself, because the run
  directory expires.

## Run log

Every run, the happy path included, appends one JSON line to
`${XDG_STATE_HOME:-~/.local/state}/pg-rescue/runs.jsonl` (mode `0600`):

```json
{
  "run_id": "20261002T140311Z-7f3a9c2e",
  "parent_run_id": null,
  "depth": 1,
  "ts": "2026-10-02T14:03:11Z",
  "pg_rescue_version": "26.10.02.12345+ab12cd34",
  "host": "phillipg-mbp-02",
  "mode": "argv",
  "chain": "sync",
  "handlers": ["flake-lock-conflict", "fix-small", "fix-large"],
  "cmd": "git pull --rebase",
  "cwd": "/abs/repo",
  "context": "sync-projects: rebase repo onto origin",
  "fingerprint": "sha256:...",
  "exit": 1,
  "result": "resolved",
  "resolved_by": "fix-large",
  "deferred_by": null,
  "interrupted_during": null,
  "signal": null,
  "final_exit": 0,
  "duration_ms": 254000,
  "attempts": [
    {
      "handler": "fix-small",
      "position": 2,
      "tags": ["agent"],
      "outcome": "failed",
      "reason": "resolved → verify failed (exit 128)",
      "exit": 0,
      "duration_ms": 41200,
      "verify_ms": 900,
      "binary": "/nix/store/.../bin/pg-rescue-claude",
      "meta": { "session_id": "...", "total_cost_usd": 0.04 }
    }
  ]
}
```

- `result` is one of `success`, `resolved`, `deferred`, `unhandled`, `error`, `interrupted`.
  `resolved_by` / `deferred_by` name the deciding handler; an `interrupted` line names the handler
  that was running in `interrupted_during` (an interrupted verify counts as its handler; `null` when
  the command itself was running) and the signal in `signal`.
- `exit` is the command's own exit code, `null` in `--stdin` mode or when the command never started;
  `final_exit` is the wrapper's. `cmd` is `null` in `--stdin` mode.
- `cmd` and `context` are redacted. `binary` is the resolved absolute path of `command[0]`; under
  nix it is a store path and so doubles as the handler's version. `meta` is the handler's own `meta`,
  verbatim.
- `pg_rescue_version` is the package's per-source-digest version string.
- Each line is one `O_APPEND` write, so concurrent runs never interleave within a line. When the file
  exceeds 10 MiB it is renamed to `runs.jsonl.1`, replacing the previous one. A failed write never
  changes the exit code; it prints a warning at `-v`.

### Measuring from the run log

Every rate below is a `jq` expression over the run log alone, per chain (a `--handlers` run, which
has no chain, is grouped as `(handlers)`). Each recipe is run against a fixture log by
`internal/runlog/readme_test.go`, verbatim.

Common-case rate: `success`, plus `resolved` by an attempt tagged `deterministic`, over all runs.

<!-- recipe: common-case-rate -->

```bash
jq -s -c 'group_by(.chain // "(handlers)") | map({chain: (.[0].chain // "(handlers)"), runs: length, common_case_rate: ((map(select(.result == "success" or (.result == "resolved" and any(.attempts[]; .outcome == "resolved" and any(.tags[]; . == "deterministic"))))) | length) / length)})' "${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue/runs.jsonl"
```

Agent rate: `resolved` by an attempt tagged `agent`.

<!-- recipe: agent-rate -->

```bash
jq -s -c 'group_by(.chain // "(handlers)") | map({chain: (.[0].chain // "(handlers)"), runs: length, agent_rate: ((map(select(.result == "resolved" and any(.attempts[]; .outcome == "resolved" and any(.tags[]; . == "agent")))) | length) / length)})' "${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue/runs.jsonl"
```

Agent cost: the sum of `meta.total_cost_usd` over attempts tagged `agent`.

<!-- recipe: agent-cost -->

```bash
jq -s -c 'group_by(.chain // "(handlers)") | map({chain: (.[0].chain // "(handlers)"), agent_cost_usd: ([.[] | .attempts[] | select(any(.tags[]; . == "agent")) | .meta.total_cost_usd // 0] | add // 0)})' "${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue/runs.jsonl"
```

Escape rate: `deferred` plus `unhandled`, over all runs.

<!-- recipe: escape-rate -->

```bash
jq -s -c 'group_by(.chain // "(handlers)") | map({chain: (.[0].chain // "(handlers)"), runs: length, escape_rate: ((map(select(.result == "deferred" or .result == "unhandled")) | length) / length)})' "${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue/runs.jsonl"
```

Failing handlers: attempts with outcome `failed`, grouped by `handler` and `reason`, most frequent first.

<!-- recipe: failing-handlers -->

```bash
jq -s -c '[.[] | .attempts[] | select(.outcome == "failed")] | group_by([.handler, .reason]) | map({handler: .[0].handler, reason: .[0].reason, count: length}) | sort_by(-.count)' "${XDG_STATE_HOME:-$HOME/.local/state}/pg-rescue/runs.jsonl"
```

Tracing: from a run to its bead, `pg-rescue-bead` records `meta.item_id`; from a bead to its run, the
bead body carries the run id; from a run to its claude session, `pg-rescue-claude` records
`meta.session_id`.

## Template data model

The three reference handlers render their prompt, title or body with Go `text/template` over one
data model, built from the failure report. `--print-template-vars` prints it and
`--print-default-template` prints the built-in template.

| Field                                                                 | Type     | Value                                                                            |
| --------------------------------------------------------------------- | -------- | -------------------------------------------------------------------------------- |
| `.RunID`, `.Fingerprint`, `.Host`, `.StartedAt`, `.Context`, `.Chain` | string   | from the report (`.Chain` is empty under `--handlers`)                           |
| `.Mode`                                                               | string   | `argv` or `stdin`                                                                |
| `.Cmd`                                                                | string   | the quoted argv, or `stdin input` in `--stdin` mode                              |
| `.Argv`                                                               | []string | empty in `--stdin` mode                                                          |
| `.Cwd`                                                                | string   | absolute path                                                                    |
| `.Repo`                                                               | string   | basename of the git toplevel containing `.Cwd`, or the basename of `.Cwd`        |
| `.Exit`                                                               | int      | the command's exit code; `-1` in `--stdin` mode                                  |
| `.OutputTail`                                                         | string   | redacted                                                                         |
| `.Attempts`                                                           | list     | each with `.Handler`, `.Position`, `.Outcome`, `.Reason`, `.Summary`, `.Details` |
| `.LastSummary`                                                        | string   | the summary of the most recent attempt that has one, otherwise empty             |
| `.Fence`                                                              | string   | per-run random nonce                                                             |

Untrusted text (`OutputTail`, each attempt's `Summary` and `Details`, `Context`) is wrapped in a
fence by the built-in templates: write `{{.Quote .OutputTail}}`. The fence is a code fence longer
than any backtick run in the text, labelled `quoted-data` with the per-run nonce, so neither the
command's output nor an earlier handler can close it or pose as instructions. It frames the
information and restricts nothing. In an attempt, `Handler`, `Position`, `Outcome` and `Reason` are
facts the wrapper recorded; `Summary` and `Details` are the handler's claims.

Template flags are named the same everywhere: `--X-template TEXT`, `--X-template-file F`,
`--append-instructions TEXT`, `--append-instructions-file F`.

## Writing a handler

A bash handler keeps stdout for the result and sends everything else to stderr:

```bash
#!/usr/bin/env bash
exec 3>&1 1>&2   # all ordinary output goes to stderr
# ... work ...
pg-rescue result resolved "relocked flake.lock" >&3
```

`pg-rescue result <resolved|deferred|declined> [SUMMARY] [--details-file F] [--meta-file F]` prints
the JSON and exits with the matching code, so it must be the last command. Treat the live state as
authoritative and earlier attempts in the report as hints: an earlier handler may have changed the
working copy before it failed. A handler that can outlive its wrapper should exit when it is orphaned
(`getppid() == 1`). A deferring handler must copy what its hand-off needs out of the run directory.

To test a handler on its own, feed it a saved failure:

```bash
pg-rescue --stdin --handlers my-handler --verify true -vv < saved-output.log
```

## pg-rescue-claude

Runs `claude -p --output-format json` synchronously, in the failed command's working directory, with
a prompt rendered from the report, and returns the agent's verdict.

It adds no policy. There is no built-in tool denylist, no nesting guard and no rules in the prompt.
Everything the agent may do comes from the instance arguments and the agent's own rules and skills.

### Arguments

| Argument                                                     | Default       | Purpose                                                                               |
| ------------------------------------------------------------ | ------------- | ------------------------------------------------------------------------------------- |
| `--model M`                                                  | `sonnet`      | Passed through as `claude --model`                                                    |
| `--time-limit D`                                             | `5m`          | Wall-clock limit for the agent. When it is exceeded, the handler exits `1`            |
| `--append-instructions TEXT`, `--append-instructions-file F` | none          | Appended to the prompt                                                                |
| `--prompt-template TEXT`, `--prompt-template-file F`         | built-in      | Replaces the whole prompt                                                             |
| `--print-default-template`, `--print-template-vars`          | n/a           | Print, then exit                                                                      |
| `--output-tail-lines N`                                      | `200`         | How many lines of the report's output tail go into the prompt                         |
| `--permission-mode M`                                        | `acceptEdits` | Passed through                                                                        |
| `--allowed-tools LIST`, `--disallowed-tools LIST`            | CLI default   | Passed through when given                                                             |
| `--mcp-config F` (repeatable), `--strict-mcp-config`         | none, off     | Passed through. With none given, no MCP flag is passed at all                         |
| `--claude-arg ARG` (repeatable)                              | none          | Passed through verbatim, for example `--claude-arg --max-budget-usd --claude-arg 0.5` |

The current CLI has no `--max-turns` in its help, and this handler does not pass one. Bound the run
with `--time-limit` and, if wanted, `--claude-arg --max-budget-usd --claude-arg N`.

### How claude is run

- The prompt goes to claude on **stdin**. `--mcp-config`, `--allowed-tools` and `--disallowed-tools`
  take a variable number of values, so a trailing positional prompt would be swallowed by them.
- `--json-schema` is always passed, so the CLI holds the agent's final answer to
  `{outcome, summary, details}` (see finding 3 below).
- claude runs in its own process group. The handler kills that whole group when the time limit
  expires, when the handler is signalled (it exits `128+signo`), and when the wrapper that started
  the handler is gone (the orphan watch, `internal/orphan`).
- The nested-session markers `CLAUDE_CODE_CHILD_SESSION`, `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`,
  `CLAUDE_CODE_SESSION_ID` and `CLAUDE_CODE_EXECPATH` are set to empty for the child, the same set
  ccpool blanks when it launches a session. Without that, an agent caller's markers make the child
  skip persisting its transcript. This is plumbing and restricts nothing.

### Result handling

1. The final message is `structured_output` when the CLI reports one, otherwise `.result`.
2. It is parsed as the result object: exactly one JSON object naming an `outcome` of `resolved`,
   `declined` or `deferred`. The handler exits `0`, `2` or `3` accordingly and prints
   `{outcome, summary, details, meta}`. A message wrapped whole in one code fence is accepted.
3. `meta` is taken from the CLI's JSON: `session_id`, `total_cost_usd`, `num_turns`, `usage` and
   `model`. Only fields the CLI reported appear. The CLI JSON has no top-level `model`; the handler
   reports the `modelUsage` entry that cost the most. Denied tools are listed in
   `meta.permission_denials` (names only).
4. Anything else exits `1` and still prints `{summary, meta}`, with `meta.error_kind` one of
   `time_limit`, `spawn`, `cli_error`, `bad_cli_output` or `unparseable_result`. For an
   unparseable final message the raw text goes to stderr.

### Verified CLI behaviour

Recorded against Claude Code 2.1.284. **No real `claude -p` was run to produce these notes.** They
come from `claude --help` and `claude -p --help`, from a static read of the installed binary's
strings, and from an existing in-repo precedent. Each finding says what is established and what is
still UNVERIFIED, with the exact command a person should run to settle it.

#### 1. Does `-p` load project `.mcp.json` servers without interactive approval?

Established from local sources, not from a live run: **yes**.

- `claude -p --help` says the workspace trust dialog is skipped in non-interactive mode.
- The installed binary resolves a project server's approval as `rejected` if the server is listed in
  `disabledMcpjsonServers`, then `approved` if it is in `enabledMcpjsonServers` or
  `enableAllProjectMcpServers` is set, and otherwise `pending`. A `pending` server is promoted to
  `approved` when the session is non-interactive and the `projectSettings` source is loaded.
- So in `-p`, an unlisted `.mcp.json` server is approved, and a server a user has rejected stays
  rejected. A run with `--setting-sources` that leaves out `project` does not auto-approve.

UNVERIFIED, needs a live check: that a server really connects and its tools are usable. In a scratch
directory whose `.mcp.json` declares one working stdio server `NAME`:

```bash
printf 'List the names of the MCP tools you can see. Answer in one line.' \
  | claude -p --output-format json --allowed-tools 'mcp__NAME' | jq -r .result
```

Expect tool names starting `mcp__NAME__`. The tool must be in `--allowed-tools` (finding 2), because
approval of a server is separate from permission to call its tools.

#### 2. How does `-p` report a permission denial, and is `--permission-prompts none` needed?

Established from local sources, not from a live run:

- A denial is reported in the result envelope as `permission_denials`, an array of
  `{tool_name, tool_use_id, tool_input}`. The handler copies the tool names into
  `meta.permission_denials`.
- `--permission-prompts <host|none>` (default `host`): `host` means the SDK host or
  `--permission-prompt-tool` answers prompts; `none` means "nobody: anything that would prompt is
  denied automatically; the permission mode still decides everything else".
- A tool that is not allowed is denied rather than prompted for, so an instance that should use MCP
  tools lists them in `--allowed-tools`.

The handler does **not** pass `--permission-prompts none` by default. It adds no policy, the flag
may not exist on an older CLI, and the time limit bounds any wait. An instance that wants it passes
`--claude-arg --permission-prompts --claude-arg none`.

UNVERIFIED, needs a live check: what a plain `claude -p` (no SDK host, no `--permission-prompt-tool`)
does with the default `host` when a tool needs permission (deny, or wait), and whether a denial sets
`is_error` or changes claude's exit status:

```bash
printf 'Run the shell command: echo hi' \
  | claude -p --output-format json --permission-mode default --allowed-tools Read \
  | jq '{is_error, subtype, permission_denials, result}'; echo "claude exit: ${PIPESTATUS[1]}"
```

If it hangs, that answers the first question: `--permission-prompts none` is then needed for
unattended runs. If it returns promptly with a populated `permission_denials`, it is not.

#### 3. Does `--json-schema` apply to `-p` output?

Established from local sources, not from a live run: **yes**, and the handler uses it.

- `claude -p --help` lists `--json-schema` as "JSON Schema for structured output validation".
- The in-repo `pg-connector` thread-slack backend passes it with `-p --output-format json`, and its
  runner documents a live reproduction in which the CLI forced a schema-conformant answer (see
  `packages/pg-connector/cmd/pg-connector-thread-slack/internal/runner.go`).
- The installed binary implements it as a `StructuredOutput` tool the model must call once at the
  end, validated against the schema, with a retry limit (terminal reason
  `structured_output_retry_exhausted`). The validated object is reported as `structured_output` in
  the result envelope, and `result` may then be empty.

That last point is why the handler reads `structured_output` first and falls back to `.result`.

UNVERIFIED, needs a live check: exactly which of `structured_output` and `result` carry the object
on 2.1.284:

```bash
printf 'Say hello.' \
  | claude -p --output-format json \
      --json-schema '{"type":"object","properties":{"outcome":{"type":"string"}},"required":["outcome"]}' \
  | jq '{result, structured_output, is_error}'
```

Either way the handler works. If a CLI version leaves both empty, the run exits `1` with
`error_kind: unparseable_result`.

### Tests

`go test ./...` in this directory. The handler's tests run the real binary (re-executed from the test
binary) against `cmd/pg-rescue-claude/testdata/fake-claude`, a bash stub copied onto `PATH` as
`claude`. It records its argv, stdin, working directory and nested-session variables, then prints a
canned envelope. Every canned report in `testdata/reports/` is run through `contracttest.Run`.

Tests that start processes (the time limit, SIGTERM and orphan tests) wait with generous windows and
kill what they started by exact pid.
