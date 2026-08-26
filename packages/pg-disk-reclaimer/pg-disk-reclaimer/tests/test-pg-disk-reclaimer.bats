#!/usr/bin/env bats
# Smoke tests for pg-disk-reclaimer's entry point (arg parsing + dispatch).
# list, validate, and reclaim are all implemented (beads
# pg2-txxyj.4/.5/.6) -- their real subcommand behavior is exercised at
# the function level in tests/test-pg-disk-reclaimer-lib.bats, so this
# file keeps only a CLI-level dispatch smoke test for each.

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

@test "list dispatches and validates --aggressiveness needs a value (real subcommand behavior lives in the lib bats suite)" {
  run_pg_disk_reclaimer list --aggressiveness
  [ "$status" -ne 0 ]
  [[ "$output" =~ "--aggressiveness requires a value" ]]
}

@test "validate dispatches and rejects a missing registry file (real subcommand behavior lives in the lib bats suite)" {
  run_pg_disk_reclaimer validate /nonexistent/pg-disk-reclaimer-test-registry.json
  [ "$status" -ne 0 ]
  [[ "$output" =~ "registry file not found" ]]
}

@test "reclaim dispatches and requires --aggressiveness (real subcommand behavior lives in the lib bats suite)" {
  run_pg_disk_reclaimer reclaim
  [ "$status" -ne 0 ]
  [[ "$output" =~ "--aggressiveness" ]]
}

@test "list --verbose is accepted alongside --aggressiveness (dispatches to cmd_list, not rejected as an unknown option)" {
  # Paired with a bare --aggressiveness (missing its value) rather than a
  # real registry lookup: this test has no HOME/XDG isolation of its own
  # (see setup() above), so asserting on registry-not-found would depend on
  # whatever real registry the invoking user's actual $HOME happens to
  # have. Option-parsing-stage failure is independent of that.
  run_pg_disk_reclaimer list --verbose --aggressiveness
  [ "$status" -ne 0 ]
  [[ ! "$output" =~ "unknown option" ]]
  [[ "$output" =~ "--aggressiveness requires a value" ]]
}

@test "list -v is accepted alongside --aggressiveness (dispatches to cmd_list, not rejected as an unknown option)" {
  run_pg_disk_reclaimer list -v --aggressiveness
  [ "$status" -ne 0 ]
  [[ ! "$output" =~ "unknown option" ]]
  [[ "$output" =~ "--aggressiveness requires a value" ]]
}
