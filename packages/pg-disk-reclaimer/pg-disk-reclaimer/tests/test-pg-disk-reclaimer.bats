#!/usr/bin/env bats
# Smoke tests for pg-disk-reclaimer's entry point (arg parsing + dispatch).
# validate remains a stub (bead pg2-txxyj.1, "scaffold" task; real
# subcommand behavior lands with task pg2-txxyj.5). list (pg2-txxyj.4) and
# reclaim (pg2-txxyj.6) are implemented -- their real subcommand behavior
# is exercised at the function level in
# tests/test-pg-disk-reclaimer-lib.bats, so this file keeps only a
# CLI-level dispatch smoke test for each.

setup() {
  # SCRIPTS_DIR: injected by nix check (raw src dir), or computed relative to
  # this test file for a local `bats tests/` run.
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="$SCRIPTS_DIR/pg-disk-reclaimer.sh"
}

run_pg_disk_reclaimer() {
  run bash "$SCRIPT" "$@"
}

@test "--help shows usage and exits 0" {
  run_pg_disk_reclaimer --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: pg-disk-reclaimer" ]]
}

@test "-h shows usage and exits 0" {
  run_pg_disk_reclaimer -h
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: pg-disk-reclaimer" ]]
}

@test "no arguments shows usage and exits non-zero" {
  run_pg_disk_reclaimer
  [ "$status" -ne 0 ]
  [[ "$output" =~ "Usage: pg-disk-reclaimer" ]]
}

@test "an unknown command is rejected" {
  run_pg_disk_reclaimer bogus-command
  [ "$status" -ne 0 ]
  [[ "$output" =~ "unknown command" ]]
}

@test "validate dispatches (even though unimplemented)" {
  run_pg_disk_reclaimer validate
  [ "$status" -ne 0 ]
  [[ "$output" =~ "not implemented yet" ]]
}

@test "list dispatches and validates --aggressiveness needs a value (real subcommand behavior lives in the lib bats suite)" {
  run_pg_disk_reclaimer list --aggressiveness
  [ "$status" -ne 0 ]
  [[ "$output" =~ "--aggressiveness requires a value" ]]
}

@test "reclaim dispatches and requires --aggressiveness (real subcommand behavior lives in the lib bats suite)" {
  run_pg_disk_reclaimer reclaim
  [ "$status" -ne 0 ]
  [[ "$output" =~ "--aggressiveness" ]]
}
