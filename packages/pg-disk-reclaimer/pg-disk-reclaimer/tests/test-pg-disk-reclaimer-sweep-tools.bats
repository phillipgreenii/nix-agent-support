#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for tests/sweep-tools.bash (bead pg2-sbvin): the tmp-sweep suite
# MUST fail, not skip, when ps or lsof is unusable and PGDR_REQUIRE_SWEEP_TOOLS=1.
#
# Global rule V-3: this drives the REAL bats against a generated one-case suite
# that loads the REAL helper, with a PATH shim making ps (or lsof) fail, which is
# the thing being tested. Nothing about bats' own skip/fail semantics is mocked.
bats_require_minimum_version 1.5.0

setup() {
  TESTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")" && pwd)"
  SHIM="$BATS_TEST_TMPDIR/shim"
  mkdir -p "$SHIM"
  CASE="$BATS_TEST_TMPDIR/case.bats"
  cat >"$CASE" <<EOS
load '$TESTS_DIR/sweep-tools'

@test "a case that needs ps and lsof" {
  pgdr_require_sweep_tools
  echo reached-the-body
}
EOS
}

# broken TOOL: a shim that makes TOOL print nothing and fail, as a sandbox does.
broken() {
  printf '#!/bin/sh\nexit 1\n' >"$SHIM/$1"
  chmod +x "$SHIM/$1"
}

run_case() { PATH="$SHIM:$PATH" run --separate-stderr bats --tap "$CASE"; }

@test "working ps and lsof: the case body runs under the require flag" {
  command -v ps >/dev/null && command -v lsof >/dev/null || skip "no ps or lsof on this host"
  # Under a sandbox that denies them the probe is itself the thing under test.
  ps -axww -o command= >/dev/null 2>&1 || skip "ps unusable in this environment"
  PGDR_REQUIRE_SWEEP_TOOLS=1 run bats --tap "$CASE"
  [ "$status" -eq 0 ]
  [[ $output == *"ok 1 a case that needs ps and lsof"* ]]
  [[ $output != *"# skip"* ]]
}

@test "unusable ps and no require flag: the case is skipped, which is the sandbox's honest answer" {
  broken ps
  unset PGDR_REQUIRE_SWEEP_TOOLS
  run_case
  [ "$status" -eq 0 ]
  [[ $output == *"# skip ps is not usable here"* ]]
}

@test "unusable ps with PGDR_REQUIRE_SWEEP_TOOLS=1: the case FAILS and names ps" {
  broken ps
  PGDR_REQUIRE_SWEEP_TOOLS=1 run_case
  [ "$status" -ne 0 ]
  [[ $output == *"not ok 1 a case that needs ps and lsof"* ]]
  [[ $output != *"# skip"* ]]
  [[ $stderr == *"ps is not usable here"* || $output == *"ps is not usable here"* ]]
}

@test "unusable lsof with PGDR_REQUIRE_SWEEP_TOOLS=1: the case FAILS and names lsof" {
  ps -axww -o command= >/dev/null 2>&1 || skip "ps unusable in this environment"
  broken lsof
  PGDR_REQUIRE_SWEEP_TOOLS=1 run_case
  [ "$status" -ne 0 ]
  [[ $output == *"not ok 1 a case that needs ps and lsof"* ]]
  [[ $stderr == *"lsof is not usable here"* || $output == *"lsof is not usable here"* ]]
}

@test "the require flag fires only for the value 1" {
  broken ps
  PGDR_REQUIRE_SWEEP_TOOLS=0 run_case
  [ "$status" -eq 0 ]
  [[ $output == *"# skip"* ]]
}
