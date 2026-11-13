package main

import (
	"fmt"
	"strings"
)

const helpText = `pg-claude-code-recorder records Claude Code state and answers queries about it.

Usage:
  pg-claude-code-recorder serve    [--config FILE] [--metrics-addr HOST:PORT] [--pprof] [--rebuild]
  pg-claude-code-recorder rebuild  [--config FILE]   rebuild index.db from the journal and re-parse (daemon stopped)
  pg-claude-code-recorder status   [--json|--short] [--watch] [--allow-partial]
  pg-claude-code-recorder coverage [--view summary|files] [--json] [--wait-caught-up] [--allow-partial]
  pg-claude-code-recorder query    [--request FILE] [--limit N] [--jsonl] [--json]   request JSON on standard input
  pg-claude-code-recorder search   [--request FILE] [--limit N] [--jsonl] [--json]   word or substring search
  pg-claude-code-recorder find     <session-id|text> [--limit N]
  pg-claude-code-recorder feed     [--after CURSOR] [--wait 30s] [--where JSON] [--project a,b] [--limit N] [--jsonl]
  pg-claude-code-recorder snapshot [--where JSON] [--project a,b] [--limit N] [--all] [--jsonl]
  pg-claude-code-recorder raw      --record FILE:GEN:OFFSET:ITEM | --file FILE --offset N --length N
  pg-claude-code-recorder conversations [--ended LABEL | --crashed | --suspected-crash] [--since 7d|TIME] [--cwd PATH] [--limit N] [--json]
  pg-claude-code-recorder processes [--live] [--session ID] [--pid N] [--limit N] [--json]
  pg-claude-code-recorder held     [--retry] [--json]   held Conversations: reason code, since, files and a remedy line each
  pg-claude-code-recorder where    <session> [--json]   its stage, blocked_by, next eligible time and the command that clears it
  pg-claude-code-recorder remove   [<session>...] [--dry-run] [--yes] [--all-eligible] [--json]
  pg-claude-code-recorder extract  <session> [--to DIR] [--json]   restore a removed Conversation (claude --resume works)
  pg-claude-code-recorder doctor   [--json]   compare every copy with its Claude file by full hash (rolling slices of doctor.bytes_per_slice; a run during a cycle completes it); exit 11 on a mismatch (notices such as out_of_scope_after_cleanup, copy_unrecoverable and copy_restored_from_source are non-critical and do not set it)
  pg-claude-code-recorder sweep    --force [--wait] [--json]   empty the skip cache and the seen table and ingest every file now (the full verification; it never runs on a timer)
  pg-claude-code-recorder version

Common flags:
  --client NAME   names the caller for the recorder's per-client metrics
  --socket PATH   the daemon's socket (default: $PG_CLAUDE_CODE_RECORDER_SOCKET, else the
                  config's socket, else <stateDir>/recorder.sock)
  --config FILE   the TOML config (default: $XDG_CONFIG_HOME/pg-claude-code-recorder/config.toml)
  --json          stable JSON on stdout; an error is {"error","exit","message"} on stderr

Examples:
  pg-claude-code-recorder status                    one-sentence health verdict
  pg-claude-code-recorder status --json             the stable status shape
  pg-claude-code-recorder status --short            one word: ok, behind, held or down
  pg-claude-code-recorder status --watch            follow the first run until it is caught up
  pg-claude-code-recorder coverage --wait-caught-up block until the first backfill is done
  pg-claude-code-recorder coverage --view files     per-file copy and parse offsets and lag
  echo '{"where":{"field":"type","eq":"assistant"},"group":["model"],"aggregate":[{"fn":"count"}]}' | pg-claude-code-recorder query
  echo '{"mode":"word","q":"registry file"}' | pg-claude-code-recorder search --limit 5
  echo '{"mode":"word","q":"\"no verify\"","fields":["tool_use.input"]}' | pg-claude-code-recorder search
                                                     word search reaches tool bodies when the index has no trigram table: case-insensitive
                                                     tokens, AND, a quoted phrase; punctuation splits tokens (--no-verify is "no verify"),
                                                     so use mode=substring for exact punctuation or a partial token
  echo '{"mode":"substring","q":"--no-verify","where":{"field":"session_id","eq":"<session>"}}' | pg-claude-code-recorder search
                                                     substring scans the copies (tool input and bodies), literal and case sensitive,
                                                     3 characters minimum, newest first, at most 256 MiB read per request; a request
                                                     that stops early answers partial: true with scanned_bytes and scanned_to, so add a
                                                     session_id, time or cwd filter (or search below scanned_to) to cover the rest
  pg-claude-code-recorder find pg2-abc               a bead id is a text search
  pg-claude-code-recorder status --client swiftbar  count this call under client "swiftbar"
  pg-claude-code-recorder snapshot --all --where '{"field":"type","eq":"assistant"}'   the state, then tail it:
  pg-claude-code-recorder feed --after <cursor> --wait 30s --where '{"field":"type","eq":"assistant"}'
  pg-claude-code-recorder raw --record f_5:1:0:-1   the bytes of one record

  pg-claude-code-recorder remove --dry-run                    what would go and how many GB (works while removal is disabled)
  pg-claude-code-recorder remove --all-eligible --yes        remove every eligible Conversation
  pg-claude-code-recorder where 0a1b2c3d-0000-4000-8000-000000000001   why it is not removable yet, and what clears that
  pg-claude-code-recorder extract 0a1b2c3d-0000-4000-8000-000000000001 --to /example/out
  pg-claude-code-recorder conversations --crashed --since 1d   Conversations whose Process died with its registry file left behind
  pg-claude-code-recorder conversations --suspected-crash      crashed plus unknown with a stale registry file (a power loss)
  pg-claude-code-recorder conversations --ended exited --cwd /example/work
  pg-claude-code-recorder processes --live                     live Processes: pid, start, session, host, control inbox
A Conversation ended how its evidence says (the recorder stores evidence, the command derives the label):
  cleared  a /clear entry, and the registry now names a new session
  exited   a typed /exit entry after the Process started
  unknown  the recorder did not watch the Process across its end
  crashed  the registry file was still present when the Process exited
  closed   otherwise (Ctrl-D, a closed terminal, SIGTERM, SIGHUP; also "closed normally")
conversations rows are newest first (default --since 7d, --limit 50); each carries session id, cwd, end time,
label, the evidence used, the stage, whether the originals exist and the line to resume it. An empty
--crashed result exits 0 and says how many unknown rows have a stale registry file. --ended cannot be
combined with --crashed or --suspected-crash (exit 2).
Removal is off by default (remove.enable = false): the operator runs remove (--all-eligible, --yes to apply) on a
roughly fixed period and the daemon removes nothing on its own. There is no undo except extract. Before turning on
automatic removal later: the daemon-down alert is registered, doctor has run clean (it also runs from a timer inside the
daemon, doctor.interval), pa-monitor sessions and ccpool resume have moved to the recorder, the state directory
is backed up, and remove --dry-run prints what would go and how many GB. remove acts on the named Conversations
or, with --all-eligible, on every settled one older than remove.after; it never bypasses the age. Without --yes
it asks when run interactively and exits 2 when not. extract refuses when the session id exists under any
project directory (exit 11) or a live Process holds it (exit 9); --to DIR never touches the Claude directory.
A cursor is <epoch>:<sequence>. feed exits 6 when its wait ended with nothing (the answer's next_cursor
still advances), and 11 (cursor_expired) when the cursor is from an old epoch, below the retention floor or
beyond the head: take a snapshot and tail from its cursor. Delivery is at least once; de-duplicate by record id.

Config keys for the doctor, WAL and read pool: doctor.bytes_per_slice (256 MiB of the copy per slice; the budget counts
bytes of one side, the copy; 0 selects the old synchronous doctor), doctor.slice_every (1m), doctor.allow_slow_cycle (start
even when the projected cycle is longer than doctor.interval), wal.truncate_above (512 MiB; the WAL size above which the
checkpoint loop may run one TRUNCATE checkpoint an hour; 0 disables it) and query.max_connections (8; at least 2, 0 is
unbounded).

First run: enable the module, run status, and read coverage until caught_up is true.
status and coverage exit 5 with the partial answer on stdout while the recorder is behind;
--allow-partial turns that into 0 during the first backfill. status always answers, offline
included (exit 3 with the socket path, launch agent state, log path and state size).

Exit codes:
`

const exitTable = `  0  success
  1  unexpected error
  2  usage error
  3  daemon offline (unreachable)
  4  not found
  5  degraded or behind (the answer is partial)
  6  a wait timed out
  7  config error: the config file could not be loaded or is invalid
  8  denied: the operation is switched off (removal disabled)
  9  busy: a live Process holds the session (extract), or the daemon answered busy
  10 serve hard-exited because shutdown outlasted its 8 s deadline (shutdown_overrun in the log; the launch agent restarts it)
  11 conflict: the cursor expired (feed), the extract target exists, or doctor found a mismatch
  20 serve refused to start because index.db has an older schema and needs the rebuild command (schema_rebuild_required; the launch agent does not restart it)
`

const stageTable = `
Conversation stages (a Conversation is a transcript lineage; the stage is how the recorder handles its copies):
  live             open: a live Process holds the Conversation
  quiet            closed and not yet settled
  held             a settle check failed; never settled or removed until it passes
  settled          closed, quiet for settle.after, check passed, copies settled
  removal_pending  removal is committed and its deletes are not all done
  removed          the Claude files are gone; an extract or a reappearing path returns it to quiet

State machine:
  live -> quiet -> settled -> removal_pending -> removed
  quiet -> held -> (check passes) -> settled
  removed -> (extract, or the path reappears) -> quiet

settled -> removal_pending needs remove.enable, remove.after of quiet, no registry file naming the session and no
live Process; a held Conversation shows its reason code in held and where.
`

func (a *app) help() {
	_, _ = fmt.Fprint(a.out, strings.TrimLeft(helpText, "\n")+exitTable+stageTable)
}
