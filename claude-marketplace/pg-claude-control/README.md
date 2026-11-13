# pg-claude-control

A Claude Code mod (plugin with one hooks module and one bash listener). On `session.start` it
spawns `scripts/listen.sh`, a child of the claude process, which holds a per-process FIFO inbox.
A tool writes one request line to the FIFO and the mod acts on its own Claude. On `session.end`
the mod writes the end reason. Claude Code does not depend on the mod: if it fails to load or is
disabled, only control through it is lost.

## Shared settings mechanism (the command reads the same)

| Setting         | Carried by                                                                        | Default                                                                      |
| --------------- | --------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| State directory | environment variable `PG_CLAUDE_CONTROL_STATE_DIR`                                | `$XDG_STATE_HOME/pg-claude-control`, else `~/.local/state/pg-claude-control` |
| `run.allow`     | environment variable `PG_CLAUDE_CONTROL_RUN_ALLOW`, comma-separated command names | `exit,clear,compact` (`exit` is always kept: `stop` runs it)                 |

## State directory layout

```text
<state>/inbox/              user-owned, mode 0700 (a looser one is tightened; a foreign-owned one or
                            a symbolic link is refused and logged as inbox_dir_refused)
<state>/inbox/<pid>.fifo    mode 0600, opened read-write by the listener
<state>/inbox/payloads/     mode 0700; payload files mode 0600
<state>/ends/<session-id>.<unix-ms>.json   one per session.end (schema/session-end.schema.json)
<state>/requests.jsonl      last 1,000 requests: {ts, id, verb, outcome}
<state>/events.jsonl        append-only: listener.gave_up, inbox_dir_refused, request_rejected,
                            reply_failed, listener_error
```

## Request wire

One line per request, at most 512 bytes including the newline (counted as bytes; 512 is macOS's
PIPE_BUF, so one write is atomic):

```text
[id=<token>] <verb> [argument] [payload=<path>] [reply=<path>]
```

Verbs are a fixed table: `submit`, `interrupt`, `run`, `read`, `ping`. An unknown verb is ignored
and logged as `request_rejected`. A line over the cap is discarded up to its newline and logged.

- `submit <text>`: `$.prompt.submit`; text starting with `/` is refused. A long text travels in a
  payload file under `<state>/inbox/payloads/` and the request carries `payload=<path>`; the mod
  reads and deletes it.
- `interrupt`: `$.turn.abort` with the turn id tracked from `turn.start`; reply reason `no turn`
  when none runs.
- `run <command> [args]`: `$.command.run`, only for a command in `run.allow`.
- `read`: `$.prompt.read`; the result is `{text, cursor}`.
- `ping`: reply `{pong: true, ts}`.

An `id=` repeating one of the last 256 ids is dropped (logged as `duplicate`). Delivery is at most
once; there is no acknowledgement.

## Reply JSON (`reply=<path>`)

The mod writes `<path>.tmp` and moves it into place with `mv -f`; the caller re-reads until the JSON
is valid.

```json
{ "ok": true, "result": { "text": "draft", "cursor": 5 } }
{ "ok": false, "reason": "command not in run.allow: foo" }
```

`ok: false` is a refusal by the mod (the command's exit code 21 carries `reason`).

## End file

`<state>/ends/<session-id>.<unix-ms>.json` is `{sessionId, reason, timeMs}`, `reason` one of
`prompt_input_exit`, `clear`, `other`, `resume`, `logout` (the last two unmeasured). It is written to
`.<name>.tmp` and moved into place, inside the hook's 1.5 s bound.

## Listener recovery

A deleted FIFO, or one replaced by another file, is recreated within one to three seconds. A killed
listener is respawned up to 5 consecutive failures with a growing pause, then `listener.gave_up` is
logged; a listener that ran 60 s resets the count. When the parent Claude dies the listener removes
its FIFO and exits within 3 seconds. Where `test -ef /dev/fd/3` cannot identify the FIFO (macOS's
fdescfs reports another device number), the health check uses `test -p` alone.

## Tests

- `nix build .#checks.<system>.pg-claude-control-mod-node-tests` (core logic and the live listener,
  plain node, real file system)
- `nix build .#checks.<system>.pg-claude-control-listener-bats` (the listener under the real bash)
- by hand: `claude plugin validate claude-marketplace/pg-claude-control`
