#!/usr/bin/env bats
# bats file_tags=type:unit

# `run !` (used for the rejection asserts) needs bats >= 1.5.0.
bats_require_minimum_version 1.5.0

# These tests drive the REAL bgrun, bgcheck-style state files and the REAL bgkill
# against real processes in a throwaway state dir (V-3): nothing here mocks bgrun.

if [[ -n ${TEST_SUPPORT:-} ]]; then
  source "$TEST_SUPPORT/test_helper.bash"
else
  source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
fi

# bgkill's own SCRIPTS_DIR is the bgkill source dir; bgrun lives beside it.
create_bgrun_wrapper() {
  local saved="$SCRIPTS_DIR"
  SCRIPTS_DIR="$(cd "$SCRIPTS_DIR/../bgrun" && pwd)"
  create_cmd_wrapper bgrun
  SCRIPTS_DIR="$saved"
}

# Bounded wait until PID is gone.
wait_gone() {
  local pid="$1" tries="${2:-100}"
  while kill -0 "$pid" 2>/dev/null && [[ $tries -gt 0 ]]; do
    sleep 0.05
    tries=$((tries - 1))
  done
  ! kill -0 "$pid" 2>/dev/null
}

# Bounded wait until FILE holds a non-empty pid.
wait_for_pidfile() {
  local file="$1" tries=100
  while [[ ! -s $file && $tries -gt 0 ]]; do
    sleep 0.05
    tries=$((tries - 1))
  done
  [[ -s $file ]]
}

@test "help exits 0 and shows usage" {
  create_cmd_wrapper bgkill
  run "$TEST_DIR/run_bgkill" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: bgkill"* ]]
}

@test "missing NAME is a usage error" {
  create_cmd_wrapper bgkill
  run "$TEST_DIR/run_bgkill"
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing NAME"* ]]
}

@test "invalid NAME and unknown options are usage errors" {
  create_cmd_wrapper bgkill
  run "$TEST_DIR/run_bgkill" "../oops"
  [ "$status" -eq 1 ]
  [[ "$output" == *"invalid name"* ]]
  run "$TEST_DIR/run_bgkill" --signal KILL job
  [ "$status" -eq 1 ]
  [[ "$output" == *"unknown option"* ]]
}

@test "an unrecorded NAME exits 3" {
  create_cmd_wrapper bgkill
  mkdir -p "$BG_DIR"
  run "$TEST_DIR/run_bgkill" ghost
  [ "$status" -eq 3 ]
  [[ "$output" == *"no job named 'ghost'"* ]]
}

@test "a finished job exits 4 and is left alone" {
  create_bgrun_wrapper
  create_cmd_wrapper bgkill
  run "$TEST_DIR/run_bgrun" done1 -- bash -c 'exit 0'
  wait_for_file "$BG_DIR/done1.exit"
  run "$TEST_DIR/run_bgkill" done1
  [ "$status" -eq 4 ]
  [[ "$output" == *"not running"* ]]
  [ "$(cat "$BG_DIR/done1.exit")" = "0" ]
}

@test "stops a running job: payload and grandchild die, exit record is the true 143" {
  create_bgrun_wrapper
  create_cmd_wrapper bgkill
  run "$TEST_DIR/run_bgrun" victim -- bash -c 'sleep 60 & echo $! >"$0"; wait' "$TEST_DIR/grandchild.pid"
  [ "$status" -eq 0 ]
  launcher="$(cat "$BG_DIR/victim.pid")"
  wait_for_pidfile "$TEST_DIR/grandchild.pid"
  grandchild="$(cat "$TEST_DIR/grandchild.pid")"
  kill -0 "$grandchild"

  run "$TEST_DIR/run_bgkill" victim
  [ "$status" -eq 0 ]
  [[ "$output" == "STOPPED victim pid=$launcher "* ]]
  wait_gone "$launcher"
  wait_gone "$grandchild"
  wait_for_file "$BG_DIR/victim.exit"
  [ "$(cat "$BG_DIR/victim.exit")" = "143" ]
}

@test "a job that ignores SIGTERM keeps running: exit 6, never escalated" {
  create_bgrun_wrapper
  create_cmd_wrapper bgkill
  run "$TEST_DIR/run_bgrun" stubborn -- bash -c 'trap "" TERM; echo ready >"$0"; while :; do sleep 0.2; done' "$TEST_DIR/ready"
  [ "$status" -eq 0 ]
  launcher="$(cat "$BG_DIR/stubborn.pid")"
  wait_for_file "$TEST_DIR/ready"
  run "$TEST_DIR/run_bgkill" --grace 1 stubborn
  [ "$status" -eq 6 ]
  [[ "$output" == *"still running"* ]]
  kill -0 "$launcher" # the launcher and its payload are still there
  [ ! -e "$BG_DIR/stubborn.exit" ]
  # Cleanup of the process this test started (SIGKILL is the test's, not bgkill's).
  mapfile -t tree < <(ps -A -o pid= -o ppid= | awk -v r="$launcher" '$2 == r { print $1 }')
  kill -9 "${tree[@]}" "$launcher" 2>/dev/null || true
}

@test "refuses a pid that is not a bgrun launcher, and signals nothing" {
  create_cmd_wrapper bgkill
  mkdir -p "$BG_DIR"
  sleep 60 &
  victim=$!
  echo "$victim" >"$BG_DIR/forged.pid"
  printf 'session: anything\ncmd: sleep 60\n' >"$BG_DIR/forged.meta"
  run "$TEST_DIR/run_bgkill" forged
  [ "$status" -eq 5 ]
  [[ "$output" == *"not a bgrun launcher"* ]]
  kill -0 "$victim"
  kill "$victim" 2>/dev/null || true
}

@test "refuses a recycled pid whose start time differs from the recorded one" {
  create_bgrun_wrapper
  create_cmd_wrapper bgkill
  run "$TEST_DIR/run_bgrun" recycled -- sleep 60
  [ "$status" -eq 0 ]
  launcher="$(cat "$BG_DIR/recycled.pid")"
  sed -i.bak 's/^pidstart: .*/pidstart: Mon Jan  1 00:00:00 2001/' "$BG_DIR/recycled.meta"
  run "$TEST_DIR/run_bgkill" recycled
  [ "$status" -eq 5 ]
  [[ "$output" == *"pid reused"* ]]
  kill -0 "$launcher"
  # Cleanup: restore the real record, then stop it properly.
  mv "$BG_DIR/recycled.meta.bak" "$BG_DIR/recycled.meta"
  run "$TEST_DIR/run_bgkill" recycled
  [ "$status" -eq 0 ]
}

@test "--dir selects another state directory" {
  create_bgrun_wrapper
  create_cmd_wrapper bgkill
  alt="$TEST_DIR/alt"
  run "$TEST_DIR/run_bgrun" --dir "$alt" altjob -- sleep 60
  [ "$status" -eq 0 ]
  launcher="$(cat "$alt/altjob.pid")"
  run "$TEST_DIR/run_bgkill" --dir "$alt" altjob
  [ "$status" -eq 0 ]
  wait_gone "$launcher"
}
