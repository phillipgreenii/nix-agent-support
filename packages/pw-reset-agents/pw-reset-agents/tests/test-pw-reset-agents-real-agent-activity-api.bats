#!/usr/bin/env bats
# bats file_tags=type:integration
# Argument-contract tests for pw-reset-agents against the REAL
# agent-activity-api binary -- NO PATH stub.
#
# Why this file exists: test-pw-reset-agents.bats stubs `agent-activity-api` on
# PATH, so it asserts only what the wrapper ASSEMBLES and can never notice that
# agent-activity-api no longer DEFINES the `clean` subcommand -- the same blind
# spot that let wait-for-agents-to-finish ship a removed pa-monitor flag for
# months (bead pg2-3fv9l).
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
  # claude-activity-api resolves its markers under HOME/XDG_STATE_HOME; point
  # both at a fresh temp dir so the outcome cannot depend on host state.
  export HOME="$TEST_DIR"
  export XDG_STATE_HOME="$TEST_DIR/state"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "real agent-activity-api resolves through runtimeDeps" {
  # Not merely "runtimeDeps is declared": the wrapper must actually find the
  # tool. A lookup failure surfaces as bash's 127 / "command not found".
  run "$SCRIPT_UNDER_TEST"
  [ "$status" -ne 127 ]
  [[ "$output" != *"command not found"* ]]
}

@test "real agent-activity-api still defines the clean subcommand" {
  run "$SCRIPT_UNDER_TEST"
  # A renamed/removed subcommand makes agent-activity-api print its own
  # "Unknown command" help banner instead of cleaning.
  [[ "$output" != *"Unknown command"* ]]
  [[ "$output" == *"Cleaned"* ]]
  [ "$status" -eq 0 ]
}
