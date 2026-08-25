#!/usr/bin/env bats
# Unit tests for pg-disk-reclaimer's core subcommand functions
# (pg-disk-reclaimer.bash). Trivial at this stage (bead pg2-txxyj.1,
# "scaffold" task) -- real bodies land with tasks pg2-txxyj.4/.5/.6.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
}

@test "cmd_list is defined and fails (not implemented yet)" {
  run cmd_list
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not implemented yet" ]]
}

@test "cmd_validate is defined and fails (not implemented yet)" {
  run cmd_validate
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not implemented yet" ]]
}

@test "cmd_reclaim is defined and fails (not implemented yet)" {
  run cmd_reclaim
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not implemented yet" ]]
}
