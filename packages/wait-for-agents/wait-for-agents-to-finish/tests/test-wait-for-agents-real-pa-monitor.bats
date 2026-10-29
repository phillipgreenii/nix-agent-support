#!/usr/bin/env bats
# bats file_tags=type:integration
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
# sandbox, so the real binary always fails. Exit 6 is "daemon unreachable"
# (args accepted, wait loop reached); exit 2 is a usage error, which covers the
# wrapper's own arg rejections (missing value, unknown option) AND a forwarded
# flag VALUE that pa-monitor's own parsing rejects (e.g. `--maximum-wait abc` ->
# "invalid value"; pa-monitor bead pg2-3rlwm made that code path reachable, bead
# pg2-xrmfng moved the codes off exit 1/3 so 1 stays the generic error). An
# unknown flag NAME is NOT that case -- the
# wrapper's catch-all arm rejects it with "Error: Unknown option:" and exit 2,
# so pa-monitor never receives it and can never print "flag provided but not
# defined", which is precisely what assert_args_accepted below guards. Since
# one exit code spans several outcomes, the message is what confirms WHICH
# failure occurred.

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
  [ "$status" -eq 6 ]
  assert_args_accepted
}

@test "real pa-monitor accepts every forwarded option" {
  run "$SCRIPT_UNDER_TEST" --maximum-wait 1 --consecutive-idle-checks 1
  [ "$status" -eq 6 ]
  assert_args_accepted
}

@test "real pa-monitor is not handed the ignored --time-between-checks" {
  run "$SCRIPT_UNDER_TEST" --time-between-checks 2
  [ "$status" -eq 6 ]
  assert_args_accepted
}

@test "real pa-monitor is not handed --caffeinate" {
  run "$SCRIPT_UNDER_TEST" --caffeinate --maximum-wait 1
  [ "$status" -eq 6 ]
  assert_args_accepted
}

# Closes the gap pg2-3rlwm's investigation noted: a malformed flag VALUE
# reaches pa-monitor's own flag parsing (the wrapper only checks --maximum-wait
# is non-empty, not that it is numeric), so it exits 2 (usage) -- distinct from
# the exit-6 "daemon unreachable" cases above, which the "invalid value" message
# tells apart from the wrapper's own exit-2 rejections.
@test "real pa-monitor rejects a malformed --maximum-wait value with exit 2" {
  run "$SCRIPT_UNDER_TEST" --maximum-wait abc
  [ "$status" -eq 2 ]
  [[ "$output" == *"invalid value"* ]]
}
