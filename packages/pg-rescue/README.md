# pg-rescue

`pg-rescue` wraps a command. When the command fails, it runs a named chain of failure handlers. A
handler is an independent executable behind one contract: it reads the failure report from
`$PG_RESCUE_REPORT`, logs to stderr, and answers with an exit code (`0` resolved, `2` declined, `3`
deferred, anything else failed) plus an optional result JSON object on stdout.

This README covers the reference handler `pg-rescue-claude`. The wrapper's own behaviour is
specified in the design of record under `docs/superpowers/specs/` at the repo root.

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
