#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for pg-wi-flow-identity's core functions (bead tc-q25wo item
# 1): pgwfi_invokes_pg_wi_flow (word-boundary command detection) and
# pgwfi_compose_ident (session8-agent-agenttype composition).
bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  source "$SCRIPTS_DIR/pg-wi-flow-identity.bash"
}

@test "pgwfi_invokes_pg_wi_flow matches a bare invocation" {
  pgwfi_invokes_pg_wi_flow "pg-wi-flow next"
}

@test "pgwfi_invokes_pg_wi_flow matches a command with no arguments" {
  pgwfi_invokes_pg_wi_flow "pg-wi-flow"
}

@test "pgwfi_invokes_pg_wi_flow matches after a compound-command separator" {
  pgwfi_invokes_pg_wi_flow "cd x && pg-wi-flow claim y"
}

@test "pgwfi_invokes_pg_wi_flow matches after a semicolon or pipe" {
  pgwfi_invokes_pg_wi_flow "foo; pg-wi-flow bar"
  pgwfi_invokes_pg_wi_flow "pg-wi-flow list | cat"
}

@test "pgwfi_invokes_pg_wi_flow rejects a command that never invokes pg-wi-flow" {
  run ! pgwfi_invokes_pg_wi_flow "ls -la"
}

@test "pgwfi_invokes_pg_wi_flow rejects a lookalike word, not a genuine invocation" {
  run ! pgwfi_invokes_pg_wi_flow "echo pg-wi-flow-ish"
  run ! pgwfi_invokes_pg_wi_flow "echo mypg-wi-flow"
}

@test "pgwfi_compose_ident uses agent id and agent type when present" {
  result="$(pgwfi_compose_ident "abcdefgh12345" "agent-7" "explore")"
  [ "$result" = "abcdefgh-agent-7-explore" ]
}

@test "pgwfi_compose_ident falls back to main-main for empty agent id/type (main session)" {
  result="$(pgwfi_compose_ident "abcdefgh12345" "" "")"
  [ "$result" = "abcdefgh-main-main" ]
}

@test "pgwfi_compose_ident truncates the session id to its first 8 characters" {
  result="$(pgwfi_compose_ident "abcdefghijklmnop" "" "")"
  [ "$result" = "abcdefgh-main-main" ]
}
