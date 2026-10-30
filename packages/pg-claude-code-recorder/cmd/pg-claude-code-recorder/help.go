package main

import (
	"fmt"
	"strings"
)

const helpText = `pg-claude-code-recorder records Claude Code state and answers queries about it.

Usage:
  pg-claude-code-recorder serve    [--config FILE] [--metrics-addr HOST:PORT] [--pprof]
  pg-claude-code-recorder status   [--json|--short] [--watch] [--allow-partial]
  pg-claude-code-recorder coverage [--view summary|files] [--json] [--wait-caught-up] [--allow-partial]
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
  pg-claude-code-recorder status --client swiftbar  count this call under client "swiftbar"

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

This build copies and reports lag; settle, hold and removal arrive in a later phase.
`

func (a *app) help() {
	_, _ = fmt.Fprint(a.out, strings.TrimLeft(helpText, "\n")+exitTable+stageTable)
}
