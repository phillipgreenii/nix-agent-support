#!/usr/bin/env bats
# Argument-contract tests for wait-for-agents-to-finish against the REAL
# pa-monitor binary -- NO PATH stub.
#
# Why this file exists (bead pg2-3fv9l): test-wait-for-agents.bats stubs
# `pa-monitor` on PATH, so it asserts only what the wrapper ASSEMBLES and can
# never notice that pa-monitor does not DEFINE those arguments. The wrapper
# shipped for months passing the `--wait-until-idle` flag that ADR 0011 had
# replaced with the `wait-until-agents-finished` subcommand, and the stubbed
# suite stayed green throughout.
#
# These cases drive the ASSEMBLED artifact via SCRIPT_UNDER_TEST (exported by
# mkBashScript's `check`), which carries the real pa-monitor on its PATH through
# runtimeDeps. Nothing is stubbed, so a future argument drift fails here.
#
# READ THE MESSAGE, NOT JUST THE EXIT CODE. No daemon runs in the check
# sandbox, so the real binary always fails -- "daemon unreachable" (args
# accepted, wait loop reached) exits 2; "flag provided but not defined" (args
# rejected during flag parsing) exits 3, now that pa-monitor bead pg2-3rlwm
# made that code path reachable and split it out of the daemon-unavailable
# exit 2. The message still confirms WHICH failure occurred, independent of
# the exit code.

setup() {
  if [[ -z ${SCRIPT_UNDER_TEST:-} ]]; then
    skip "SCRIPT_UNDER_TEST unset; the assembled artifact only exists in the nix check"
  fi

  TEST_DIR="$(mktemp -d)"
  # pa-monitor resolves its socket under XDG_STATE_HOME (else HOME/.local/state).
  # Point both at a fresh temp dir so the socket provably does not exist and the
  # outcome cannot depend on a daemon running on the host.
  export HOME="$TEST_DIR"
  export XDG_STATE_HOME="$TEST_DIR/state"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Assert the run reached pa-monitor's wait loop rather than dying in its
# argument handling.
assert_args_accepted() {
  [[ "$output" != *"flag provided but not defined"* ]]
  [[ "$output" != *"unknown subcommand"* ]]
  [[ "$output" == *"daemon unreachable"* ]]
}

@test "real pa-monitor accepts the default invocation" {
  run "$SCRIPT_UNDER_TEST"
  [ "$status" -eq 2 ]
  assert_args_accepted
}

@test "real pa-monitor accepts every forwarded option" {
  run "$SCRIPT_UNDER_TEST" --maximum-wait 1 --consecutive-idle-checks 1
  [ "$status" -eq 2 ]
  assert_args_accepted
}

@test "real pa-monitor is not handed the ignored --time-between-checks" {
  run "$SCRIPT_UNDER_TEST" --time-between-checks 2
  [ "$status" -eq 2 ]
  assert_args_accepted
}

@test "real pa-monitor is not handed --caffeinate" {
  run "$SCRIPT_UNDER_TEST" --caffeinate --maximum-wait 1
  [ "$status" -eq 2 ]
  assert_args_accepted
}

# Closes the gap pg2-3rlwm's investigation noted: a malformed flag VALUE
# reaches pa-monitor's own flag parsing (the wrapper only checks --maximum-wait
# is non-empty, not that it is numeric), so it exits 3 -- distinct from the
# exit-2 "daemon unreachable" cases above.
@test "real pa-monitor rejects a malformed --maximum-wait value with exit 3" {
  run "$SCRIPT_UNDER_TEST" --maximum-wait abc
  [ "$status" -eq 3 ]
  [[ "$output" == *"invalid value"* ]]
}
