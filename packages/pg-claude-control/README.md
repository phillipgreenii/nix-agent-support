# pg-claude-control

The one way for a tool in any language to send `submit`, `interrupt`, `run`, `stop` and `read`
requests to a running Claude. The command resolves the target through the session registry
(`<claude dir>/sessions/<pid>.json`; the claude dir is `CLAUDE_CONFIG_DIR`, else `~/.claude`) and writes
one request line into that Claude's inbox FIFO, which the `pg-claude-control` mod (plugin in
`claude-marketplace/pg-claude-control`) listens on. The command never talks to the recorder: it reads
only the registry files and the inbox.

```text
pg-claude-control submit    <target> < text
pg-claude-control interrupt <target>
pg-claude-control run       [flags] <target> <command> [args]
pg-claude-control stop      <target> [--signal] [--escalate <duration>]
pg-claude-control read      <target> --json
pg-claude-control list      [--json]
```

Flags: `--json`, `--wait <d>` (inbox wait, default 3s), `--timeout <d>` (reply wait, default 10s),
`--reply` (ask the mod to confirm). Put flags before `run`'s `<command>`; everything after it is the
command's arguments.

## Targets

A bare number is a pid, a UUID-shaped string a session id, anything else a name; `pid:`, `session:` and
`name:` force one reading. Only running Claudes match: the registry file must exist, the pid must be
alive and its start time must equal the file's `procStart`. The same check runs again immediately before
every write and every signal, so a reused pid never receives a request or a SIGTERM.

## Exit codes (this table is the control command's own)

| Code | Name                | Meaning                                                               |
| ---- | ------------------- | --------------------------------------------------------------------- |
| 0    |                     | The line was written (not that it took effect)                        |
| 1    | `internal`          | Reserved for unexpected errors                                        |
| 2    | `usage`             | Usage error, or a client-side refusal (`/` text, non-allowlisted run) |
| 3    | `no_control_mod`    | Unavailable: no inbox after the `--wait` retry, the Claude has no mod |
| 4    | `no_such_process`   | NotFound: not a running Claude                                        |
| 6    | `no_reply`          | Timeout: no reply within `--timeout`                                  |
| 9    | `mod_not_accepting` | Busy: the FIFO has no reader, or its buffer is full                   |
| 20   | `session_stayed`    | `stop` without `--escalate`: still running `--timeout` after `exit`   |
| 21   | `refused_by_mod`    | The mod refused a request that carried a reply path                   |
| 22   | `ambiguous_target`  | A name or session id matches several Claudes (candidates listed)      |

Codes 3, 4, 6 and 9 are the shared codes of `x/exitcode`; 20 to 22 are this command's own. Before
the shared codes `no_such_process` was 3, `no_control_mod` 4, `mod_not_accepting` 5, `refused_by_mod`
6, `ambiguous_target` 7, `no_reply` 8 and `session_stayed` 9; callers (ccpool, the plugin README)
changed with it.

Without `--json` an error prints `error: <name>` then the message on standard error; with `--json` one
object `{"error","exit","message"}` goes there instead.

## Notes

- `submit`, `run` and `stop` print `sent; effect not confirmed` unless `--reply` asked the mod to confirm.
  Delivery is at most once; each request carries a random `id=` so a retry is safe.
- A `submit` text that is over the 512-byte wire cap, or has a newline, travels in a payload file under
  `<state>/inbox/payloads/` and the request carries `payload=<path>`.
- `stop` sends `interrupt`, then `run exit`; `--escalate <d>` follows with SIGTERM if the Claude is still
  alive after `d`; `--signal` sends SIGTERM at once and works without the mod. SIGKILL is never sent.
  A session with scheduled or background tasks makes Claude ask whether to exit and stop the tasks or
  stay; the mod does not answer that prompt, so plain `stop` then exits 20 (`session_stayed`) once
  `--timeout` passes with the Claude alive, and the caller escalates with `--escalate` or `--signal`.
- Settings are shared with the mod: `PG_CLAUDE_CONTROL_STATE_DIR` (default
  `$XDG_STATE_HOME/pg-claude-control`, else `~/.local/state/pg-claude-control`) and
  `PG_CLAUDE_CONTROL_RUN_ALLOW` (default `exit,clear,compact`; `exit` is always kept).

## Tests

`nix build .#checks.<system>.pg-claude-control-go-tests` runs the unit tier: real FIFOs and registry files
in temp directories, `ps` answered by `cmdruntest.Fake`.
