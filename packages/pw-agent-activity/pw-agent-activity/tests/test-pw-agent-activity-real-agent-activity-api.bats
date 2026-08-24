#!/usr/bin/env bats
# Argument-contract tests for pw-agent-activity against the REAL
# agent-activity-api binary -- NO PATH stub.
#
# Why this file exists: test-pw-agent-activity.bats stubs `agent-activity-api`
# on PATH, so it asserts only what the wrapper ASSEMBLES and can never notice
# that agent-activity-api no longer DEFINES the `wait` subcommand -- the same
# blind spot that let wait-for-agents-to-finish ship a removed pa-monitor flag
# for months (bead pg2-3fv9l).
#
# These cases drive the ASSEMBLED artifact via SCRIPT_UNDER_TEST (exported by
# mkBashScript's `check`), which carries the real agent-activity-api on its PATH
# through runtimeDeps. Nothing is stubbed, so both a subcommand rename and a
# missing runtimeDeps entry fail here.

setup() {
  if [[ -z ${SCRIPT_UNDER_TEST:-} ]]; then
    skip "SCRIPT_UNDER_TEST unset; the assembled artifact only exists in the nix check"
  fi

  TEST_DIR="$(mktemp -d)"
  export HOME="$TEST_DIR"
  export XDG_STATE_HOME="$TEST_DIR/state"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "real agent-activity-api resolves through runtimeDeps" {
  # Not merely "runtimeDeps is declared": the wrapper must actually find the
  # tool. A lookup failure surfaces as bash's 127 / "command not found".
  run "$SCRIPT_UNDER_TEST" --maximum-wait 0
  [ "$status" -ne 127 ]
  [[ "$output" != *"command not found"* ]]
}

@test "real agent-activity-api still defines the wait subcommand" {
  run "$SCRIPT_UNDER_TEST" --maximum-wait 0
  # A renamed/removed subcommand makes agent-activity-api print its own
  # "Unknown command" help banner instead of entering the wait.
  [[ "$output" != *"Unknown command"* ]]
  [[ "$output" == *"Waiting for agents"* ]]
}

@test "real agent-activity-api accepts the forwarded wait options" {
  run "$SCRIPT_UNDER_TEST" --maximum-wait 0 --time-between-checks 1
  [[ "$output" != *"Unknown option"* ]]
  [[ "$output" == *"Waiting for agents"* ]]
}

@test "an unrecognised option is judged by agent-activity-api, not the wrapper" {
  run "$SCRIPT_UNDER_TEST" --definitely-not-an-option
  [ "$status" -eq 2 ]
  [[ "$output" == *"Unknown option: --definitely-not-an-option"* ]]
}
