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
    echo "pg-wi-flow: no actor available (PG_WI_FLOW_IDENT is unset/empty and no --actor was given); refusing to act without an actor" >&2
    return 1
  fi

  if [[ -z $stage ]]; then
    echo "pg-wi-flow: no actor available (PG_WI_FLOW_IDENT=$PG_WI_FLOW_IDENT but no stage was given); refusing to act without an actor" >&2
    return 1
  fi

  printf '%s-%s\n' "$PG_WI_FLOW_IDENT" "$stage"
}
