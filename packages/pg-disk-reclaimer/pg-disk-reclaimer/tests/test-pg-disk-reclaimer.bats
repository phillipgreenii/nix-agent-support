#!/usr/bin/env bats
# Smoke tests for pg-disk-reclaimer's entry point (arg parsing + dispatch).
# Trivial at this stage (bead pg2-txxyj.1, "scaffold" task) -- real
# subcommand behavior lands with tasks pg2-txxyj.4/.5/.6.

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

@test "each known subcommand dispatches (even though unimplemented)" {
  for cmd in list validate reclaim; do
    run_pg_disk_reclaimer "$cmd"
    [ "$status" -ne 0 ]
    [[ "$output" =~ "not implemented yet" ]]
  done
}
