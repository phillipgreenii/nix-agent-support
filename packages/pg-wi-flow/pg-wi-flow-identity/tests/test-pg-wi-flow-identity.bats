#!/usr/bin/env bats
# bats file_tags=type:unit
# Script-level tests for pg-wi-flow-identity's decline/rewrite behavior
# (bead tc-q25wo items 1 + 4 acceptance list): declines without a session
# id, declines for a non-pg-wi-flow command, rewrites a bare `pg-wi-flow
# next` and a compound `cd x && pg-wi-flow claim y`, and the main-session
# form (empty CETA_AGENT_ID/CETA_AGENT_TYPE) yields "-main-main". Runs the
# raw .sh as a subprocess, mirroring pg-disk-reclaimer's
# test-pg-disk-reclaimer.bats.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="$SCRIPTS_DIR/pg-wi-flow-identity.sh"
  unset CETA_SESSION_ID CETA_AGENT_ID CETA_AGENT_TYPE
}

run_pg_wi_flow_identity() {
  run bash "$SCRIPT" "$@"
}

@test "--help shows usage and exits 0" {
  run_pg_wi_flow_identity --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: pg-wi-flow-identity" ]]
}

@test "declines (exit 1, no output) when CETA_SESSION_ID is empty" {
  run_pg_wi_flow_identity "pg-wi-flow next"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "declines (exit 1, no output) for a command that never invokes pg-wi-flow" {
  export CETA_SESSION_ID="sessionid12345678"
  run_pg_wi_flow_identity "ls -la"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "rewrites a bare pg-wi-flow invocation, stamping PG_WI_FLOW_IDENT" {
  export CETA_SESSION_ID="sessionid12345678"
  export CETA_AGENT_ID="agent-1"
  export CETA_AGENT_TYPE="explore"
  run_pg_wi_flow_identity "pg-wi-flow next"
  [ "$status" -eq 0 ]
  [ "$output" = "export PG_WI_FLOW_IDENT=sessioni-agent-1-explore; pg-wi-flow next" ]
}

@test "rewrites a compound command invoking pg-wi-flow after a separator" {
  export CETA_SESSION_ID="sessionid12345678"
  export CETA_AGENT_ID="agent-1"
  export CETA_AGENT_TYPE="explore"
  run_pg_wi_flow_identity "cd x && pg-wi-flow claim y"
  [ "$status" -eq 0 ]
  [ "$output" = "export PG_WI_FLOW_IDENT=sessioni-agent-1-explore; cd x && pg-wi-flow claim y" ]
}

@test "main-session form (empty agent id/type) yields -main-main" {
  export CETA_SESSION_ID="sessionid12345678"
  export CETA_AGENT_ID=""
  export CETA_AGENT_TYPE=""
  run_pg_wi_flow_identity "pg-wi-flow next"
  [ "$status" -eq 0 ]
  [ "$output" = "export PG_WI_FLOW_IDENT=sessioni-main-main; pg-wi-flow next" ]
}

@test "two distinct agent ids on the same session produce distinct idents (parallel-subagent shape)" {
  export CETA_SESSION_ID="sessionid12345678"
  export CETA_AGENT_ID="agent-a"
  export CETA_AGENT_TYPE="explore"
  run_pg_wi_flow_identity "pg-wi-flow next"
  first="$output"

  export CETA_AGENT_ID="agent-b"
  run_pg_wi_flow_identity "pg-wi-flow next"
  second="$output"

  [ "$first" != "$second" ]
}
