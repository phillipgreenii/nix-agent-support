# shellcheck shell=bash

# pgwf_compose_actor EXPLICIT_ACTOR STAGE — prints, on stdout, the actor
# pg-wi-flow's CLI verbs should pass to `bd` (bead tc-q25wo item 2). Every
# write verb (claim, and later recompositions) MUST route its actor through
# this function -- never call bd without an actor.
#
# Precedence:
#   1. EXPLICIT_ACTOR (a caller-supplied --actor) wins verbatim when
#      non-empty -- it is never combined with PG_WI_FLOW_IDENT/STAGE.
#   2. Otherwise "$PG_WI_FLOW_IDENT-$STAGE", composed from the env var the
#      ceta input processor (pg-wi-flow-identity, see
#      ../pg-wi-flow-identity/pg-wi-flow-identity.bash) stamps into every
#      pg-wi-flow invocation, and STAGE -- the item's `stage:` label read at
#      claim time; later write verbs recompose the same way from the item's
#      current stage.
#   3. If PG_WI_FLOW_IDENT is unset/empty, or STAGE is empty, composition is
#      impossible: print a one-line reason to stderr and return non-zero
#      WITHOUT printing anything to stdout, so a caller that forgets to
#      check the exit code cannot mistake stderr prose for an actor.
#
# Outside a Claude Code session (no ceta, so no PG_WI_FLOW_IDENT) this MUST
# fail rather than fall back to any bd-side default identity -- pg-wi-flow
# has no fallback of its own to offer; --actor is the only other source.
pgwf_compose_actor() {
  local explicit_actor="${1:-}" stage="${2:-}"

  if [[ -n $explicit_actor ]]; then
    printf '%s\n' "$explicit_actor"
    return 0
  fi

  if [[ -z ${PG_WI_FLOW_IDENT:-} ]]; then
    echo "pg-wi-flow: no actor available (PG_WI_FLOW_IDENT is unset/empty and no --actor was given); refusing to act without an actor. $(pgwf_identity_hint)" >&2
    return 1
  fi

  if [[ -z $stage ]]; then
    echo "pg-wi-flow: no actor available (PG_WI_FLOW_IDENT=$PG_WI_FLOW_IDENT but no stage was given); refusing to act without an actor" >&2
    return 1
  fi

  printf '%s-%s\n' "$PG_WI_FLOW_IDENT" "$stage"
}

# pgwf_identity_hint -- the one-line remedy appended to every "no identity"
# refusal and to the fail-fast preflight below. pg-wi-flow's agents (dispatcher,
# worker, resolver) can only run with an identity AND the Agent tool when the
# /pg-wi-flow:drain loop is the TOP-LEVEL interactive Claude Code session:
# one more level of subagent nesting (e.g. /drain launched from inside a
# dispatched subagent) leaves the worker with no Agent tool to dispatch
# reviewer leaves, and PG_WI_FLOW_IDENT unset (tc-9ddu3.2.11 dry run). The
# other cause (tc-h3mrb): ceta runs input processors only on an Approve/Ask
# verdict (docs/adr/0070), and it abstains on a heredoc fed to pg-wi-flow or a
# function definition, so such a compound command is never stamped.
pgwf_identity_hint() {
  printf '%s' "PG_WI_FLOW_IDENT is stamped by the ceta hook only onto a Bash call it approves, so a command with a heredoc or a shell function definition (ceta abstains on those) arrives unstamped even in a top-level session: run pg-wi-flow as its own simple command (pass JSON with --json FILE or a pipe, not a heredoc). Otherwise pg-wi-flow must run from a TOP-LEVEL interactive Claude Code session (the /pg-wi-flow:drain loop is that session); a dispatcher/worker started from inside another dispatched subagent has no PG_WI_FLOW_IDENT and no Agent tool. Re-run /pg-wi-flow:drain from a top-level session (outside Claude Code, pass --actor NAME instead)."
}

# pgwf_require_identity -- fail-fast precondition for the verbs that reserve
# or hand off work (next, claim, release): succeeds when an explicit actor was
# given (PGWF_EXPLICIT_ACTOR) or PG_WI_FLOW_IDENT is set; otherwise prints an
# actionable one-line reason to stderr and returns non-zero BEFORE any bd
# call, so an empty queue (`none`) can no longer mask a missing identity.
pgwf_require_identity() {
  if [[ -n ${PGWF_EXPLICIT_ACTOR:-} || -n ${PG_WI_FLOW_IDENT:-} ]]; then
    return 0
  fi
  echo "pg-wi-flow: PG_WI_FLOW_IDENT is unset/empty and no --actor was given; refusing to act. $(pgwf_identity_hint)" >&2
  return 1
}
