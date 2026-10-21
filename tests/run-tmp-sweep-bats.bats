#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/run-tmp-sweep-bats.sh, the body of the
# `pg-disk-reclaimer-tmp-sweep-test` hook (bead pg2-sbvin).
#
# Deterministic logic against the REAL bats (global rule V-3): generated fixture
# suites stand in for the 46-case sweep suite, one per outcome the gate must
# distinguish. The ps failure is a PATH shim, which is the thing being tested.

bats_require_minimum_version 1.5.0

SCRIPT="$BATS_TEST_DIRNAME/../scripts/run-tmp-sweep-bats.sh"
HELPER="$BATS_TEST_DIRNAME/../packages/pg-disk-reclaimer/pg-disk-reclaimer/tests/sweep-tools"

setup() {
  SHIM="$BATS_TEST_TMPDIR/shim"
  mkdir -p "$SHIM"
  SUITE="$BATS_TEST_TMPDIR/suite.bats"
  unset PGDR_REQUIRE_SWEEP_TOOLS
}

# suite: write a fixture suite (stdin) that loads the real helper first.
suite() {
  {
    printf "load '%s'\n\n" "$HELPER"
    cat
  } >"$SUITE"
}

broken_ps() {
  printf '#!/bin/sh\nexit 1\n' >"$SHIM/ps"
  chmod +x "$SHIM/ps"
}

gate() { PATH="$SHIM:$PATH" run --separate-stderr bash "$SCRIPT" --suite "$SUITE"; }

@test "every case passes: exit 0 and the ran count is reported" {
  ps -axww -o command= >/dev/null 2>&1 || skip "ps unusable in this environment"
  suite <<'EOS'
@test "one" { true; }
@test "two" { pgdr_require_sweep_tools; }
EOS
  gate
  [ "$status" -eq 0 ]
  [[ $output == *"2 cases ran and passed"* ]]
}

@test "a failing case fails the gate" {
  suite <<'EOS'
@test "one" { false; }
EOS
  gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"the tmp-sweep suite failed"* ]]
}

@test "ps unusable: a sweep case FAILS the gate instead of skipping (the pg2-sbvin defect)" {
  suite <<'EOS'
@test "sweeps" { pgdr_require_sweep_tools; }
EOS
  broken_ps
  gate
  [ "$status" -eq 1 ]
  [[ $output == *"not ok 1 sweeps"* ]]
  [[ $output != *"# skip"* ]]
}

@test "the same suite with ps unusable and the gate's flag absent merely skips (what the nix sandbox did)" {
  suite <<'EOS'
@test "sweeps" { pgdr_require_sweep_tools; }
EOS
  broken_ps
  PATH="$SHIM:$PATH" run bats --tap "$SUITE"
  [ "$status" -eq 0 ]
  [[ $output == *"# skip"* ]]
}

@test "an unexpected skip fails the gate even though bats exits 0" {
  suite <<'EOS'
@test "lazy" { skip "not today"; }
EOS
  gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"unexpected skipped cases"* ]]
  [[ $stderr == *"not today"* ]]
}

@test "the two environmental skips (no BSD userland, root) are allowed" {
  suite <<'EOS'
@test "bsd" { skip "no BSD userland here"; }
@test "mode" { skip "root ignores file modes"; }
@test "real" { true; }
EOS
  gate
  [ "$status" -eq 0 ]
  [[ $output == *"ran and passed"* ]]
}

@test "a missing suite is a usage error, not a pass" {
  PATH="$SHIM:$PATH" run --separate-stderr bash "$SCRIPT" --suite "$BATS_TEST_TMPDIR/nope.bats"
  [ "$status" -eq 2 ]
}

@test "the default suite is the package's tmp-sweep suite and it exists" {
  [ -f "$BATS_TEST_DIRNAME/../packages/pg-disk-reclaimer/pg-disk-reclaimer/tests/test-pg-disk-reclaimer-tmp-sweep.bats" ]
  grep -q 'packages/pg-disk-reclaimer/pg-disk-reclaimer/tests/test-pg-disk-reclaimer-tmp-sweep.bats' "$SCRIPT"
}
