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
  pg-claude-code-recorder find pg2-abc               a bead id is a text search
  pg-claude-code-recorder status --client swiftbar  count this call under client "swiftbar"
  pg-claude-code-recorder snapshot --all --where '{"field":"type","eq":"assistant"}'   the state, then tail it:
  pg-claude-code-recorder feed --after <cursor> --wait 30s --where '{"field":"type","eq":"assistant"}'
  pg-claude-code-recorder raw --record f_5:1:0:-1   the bytes of one record

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
A cursor is <epoch>:<sequence>. feed exits 6 when its wait ended with nothing (the answer's next_cursor
still advances), and 7 (cursor_expired) when the cursor is from an old epoch, below the retention floor or
beyond the head: take a snapshot and tail from its cursor. Delivery is at least once; de-duplicate by record id.

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
  7  cursor expired
  8  refused: a precondition failed (removal disabled, the extract target exists or a live Process holds it)
  9  doctor found a mismatch
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

This build copies, reports lag and reports live and quiet; settle, hold and removal arrive in a later phase.
`

func (a *app) help() {
	_, _ = fmt.Fprint(a.out, strings.TrimLeft(helpText, "\n")+exitTable+stageTable)
}
