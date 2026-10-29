# claude-tool-tracker

Appends one event line per call to the tracker's event log, and records Claude Code command-hook
input. Every later part of the tracker (ingest, the mod, CETA's own events) writes and reads the
line format documented here. Per-kind `data` fields: [docs/event-kinds.md](docs/event-kinds.md).

Standard library only. Design: `docs/superpowers/specs/2026-10-14-claude-tool-tracker-design.md`.

## Event format

### Where events go

- Directory: `$XDG_STATE_HOME/claude-tool-tracker/events/` (default `~/.local/state/...`), mode
  `0700`. The command creates it (and a missing parent) and sets the mode explicitly, whatever the
  umask.
- File: `tool-events.<session_id>.<source>.jsonl`, mode `0600` (set explicitly after open).
  `source` MUST match `[a-z0-9-]+`; `session_id` MUST match `[A-Za-z0-9_-]+`. A producer writes
  only its own files. A reader MUST read only files matching the whole pattern.

### One line per event

Each line is one JSON object ending in a newline, written with a single append-mode `write` call.

| Field         | Required              | Meaning                                                        |
| ------------- | --------------------- | -------------------------------------------------------------- |
| `schema`      | yes                   | `tool-events/1`                                                |
| `event_id`    | yes                   | unique per event (default: 12 hex of the ms clock + 16 random) |
| `ts`          | yes                   | milliseconds since the Unix epoch, the producer's clock        |
| `source`      | yes                   | the producer; equals the file name's `source`                  |
| `kind`        | yes                   | `<source>.<name>`                                              |
| `session_id`  | yes                   | the Claude Code session id                                     |
| `tool_use_id` | yes, except as below  | the call's id                                                  |
| `agent_id`    | for a subagent's call | the subagent id                                                |
| `data`        | yes (may be `{}`)     | the producer's own fields                                      |

`tool_use_id` may be omitted only by `tracker.session_end` and `tracker.permission_offer` (the
`PermissionRequest` hook input declares no `tool_use_id`; ingest correlates it by session, agent,
tool name and time, and by `data.tool_input`). `append` rejects any other kind without it.

### `append`

```
claude-tool-tracker append --session-id ID --source SRC --kind SRC.NAME
    [--tool-use-id ID] [--agent-id ID] [--ts MS] [--event-id ID]   < data.json
```

Stdin is the `data` object (empty stdin means `{}`); anything but exactly one JSON object is an
error. Numbers keep their exact text. Exit `0` after one line was written; `1` for invalid input
or a write failure (nothing is written); `2` for a usage error. A producer MUST NOT let a nonzero
exit change a tool call's verdict: the mod ignores the exit, and CETA writes through the Go
package, not this command.

### Hook mode

```
claude-tool-tracker hook        < Claude Code hook JSON
```

Reads one hook input from stdin (at most 8 MiB) and appends a `source` `tracker` event for
`PreToolUse` (`tracker.pre_tool`), `PostToolUse` and `PostToolUseFailure` (`tracker.post_tool`),
`PermissionRequest` (`tracker.permission_offer`) and `PermissionDenied` (`tracker.denied`). Any
other event, empty or malformed input, and any write failure do nothing. It ALWAYS exits `0`
and prints NOTHING to stdout or stderr, so Claude Code never reads a decision or `updatedInput`
from it (it is safe next to CETA's `PreToolUse` hook). It gives up after 2 seconds. Register it
with a short timeout (for example 5 seconds).

### Maximum line size and shortening

A line (newline included) is at most **65536 bytes**, so one `write` stays a single atomic append.
When a line would exceed it, the producer shortens `data` and still writes one line:

1. Find the longest string anywhere in `data` (nested objects and arrays included), cut it to
   half its length on a UTF-8 rune boundary and end it with `…[truncated N bytes]` (N = bytes
   removed). Repeat until the line fits.
2. Set `data._truncated` to the original line size in bytes.
3. If the longest string is 64 bytes or less and the line is still too big, replace `data` with
   `{"_truncated": <original bytes>, "_dropped": true}`.

Envelope fields are never shortened. A reader that needs a full input (CETA's `evaluate` replay)
must treat an event with `data._truncated` as incomplete.

## Layout

| Path                       | Holds                                                        |
| -------------------------- | ------------------------------------------------------------ |
| `cmd/claude-tool-tracker/` | the binary; its tests build and run it                       |
| `internal/events/`         | envelope, validation, file naming, modes, shortening, append |
| `internal/hook/`           | hook JSON to tracker events                                  |
| `testdata/`                | real-shaped hook input fixtures                              |
