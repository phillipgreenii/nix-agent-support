#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for pg-wi-flow's CLI actor composition (bead tc-q25wo item 2):
# env only (PG_WI_FLOW_IDENT set, no stage -- refuses), env + stage
# (composes), --actor override (wins verbatim), and refusal (neither
# available). `run --separate-stderr` (bats >= 1.5.0) is used throughout so
# a refusal's one-line stderr reason is never mistaken for a printed actor
# on stdout.
bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -d $LIB_PATH ]]; then
    # shellcheck disable=SC1091 # composed lib path is runtime-resolved
    source "$LIB_PATH/actor.bash"
  else
    # shellcheck disable=SC1090 # composed lib path is runtime-resolved (nix check)
    source "$LIB_PATH"
  fi

  unset PG_WI_FLOW_IDENT
}

@test "env only: PG_WI_FLOW_IDENT set but no stage refuses without printing an actor" {
  export PG_WI_FLOW_IDENT="abcdefgh-agent-1-explore"
  run --separate-stderr pgwf_compose_actor "" ""
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [ -n "$stderr" ]
}

@test "env + stage composes PG_WI_FLOW_IDENT-STAGE" {
  export PG_WI_FLOW_IDENT="abcdefgh-agent-1-explore"
  run --separate-stderr pgwf_compose_actor "" "claim"
  [ "$status" -eq 0 ]
  [ "$output" = "abcdefgh-agent-1-explore-claim" ]
}

@test "--actor override wins verbatim, even with a usable env+stage present" {
  export PG_WI_FLOW_IDENT="abcdefgh-agent-1-explore"
  run --separate-stderr pgwf_compose_actor "explicit-actor" "claim"
  [ "$status" -eq 0 ]
  [ "$output" = "explicit-actor" ]
}

@test "refusal: neither --actor nor PG_WI_FLOW_IDENT is available" {
  unset PG_WI_FLOW_IDENT
  run --separate-stderr pgwf_compose_actor "" "claim"
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [ -n "$stderr" ]
}
