# shellcheck shell=bash

# pg-wi-flow-identity is a ceta input processor (see
# programs.claude-extended-tool-approver.inputProcessors and bead tc-q25wo
# item 1): ceta calls it as `pg-wi-flow-identity "<bash-command>"`, with
# CETA_SESSION_ID / CETA_AGENT_ID / CETA_AGENT_TYPE already in its
# environment (bead tc-7m85u). It stamps PG_WI_FLOW_IDENT -- the identity
# pg-wi-flow's own CLI actor composition (../lib/actor.bash) reads -- onto
# any command that invokes `pg-wi-flow`, so no agent (and no pg-wi-flow
# caller) ever has to type an actor by hand.

# pgwfi_invokes_pg_wi_flow COMMAND -- true if COMMAND invokes `pg-wi-flow`
# as a command WORD: matched on a boundary of neither an identifier
# character nor `-` (so a shell separator -- `;`, `&&`, `||`, `|`, `(`,
# whitespace -- or the start/end of the string counts as a boundary, but
# "pg-wi-flow-ish" or "mypg-wi-flow" do not match). This is a regex
# heuristic, not a full shell parse, deliberately: rewriting only genuine
# pg-wi-flow invocations keeps the change minimal on ceta's Approve path --
# a rewrite is only safe there, since ceta's allowlist is matched against
# the REWRITTEN command (bead tc-q25wo description).
pgwfi_invokes_pg_wi_flow() {
  local cmd="$1"
  [[ $cmd =~ (^|[^A-Za-z0-9_-])pg-wi-flow($|[^A-Za-z0-9_-]) ]]
}

# pgwfi_compose_ident SESSION_ID AGENT_ID AGENT_TYPE -- prints
# "<session8>-<agent-or-main>-<agenttype-or-main>" on stdout (no trailing
# newline). session8 is the first 8 characters of SESSION_ID.
# AGENT_ID/AGENT_TYPE follow ceta's own "present but empty in the main
# session" convention (bead tc-7m85u): an empty value here means "main",
# never "unknown agent".
pgwfi_compose_ident() {
  local session_id="$1" agent_id="$2" agent_type="$3"
  printf '%s-%s-%s' "${session_id:0:8}" "${agent_id:-main}" "${agent_type:-main}"
}
