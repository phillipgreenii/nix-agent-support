#!/usr/bin/env bats
# bats file_tags=type:unit

if [[ -n ${TEST_SUPPORT:-} ]]; then
  source "$TEST_SUPPORT/test_helper.bash"
else
  source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
fi

setup_file() {
  # Fast polling so the blocking tests finish quickly.
  export BGWAIT_POLL_INTERVAL=0.05
}

@test "help exits 0, shows usage and the exit codes" {
  create_cmd_wrapper bgwait
  run "$TEST_DIR/run_bgwait" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: bgwait"* ]]
  [[ "$output" == *"480"* ]]
  [[ "$output" == *"540"* ]]
}

@test "unknown NAME exits 3" {
  create_cmd_wrapper bgwait
  mkdir -p "$BG_DIR"
  run "$TEST_DIR/run_bgwait" ghost
  [ "$status" -eq 3 ]
  [[ "$output" == *"no job named 'ghost'"* ]]
}

@test "missing NAME is a usage error" {
  create_cmd_wrapper bgwait
  run "$TEST_DIR/run_bgwait"
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing NAME"* ]]
}

@test "invalid NAME is rejected" {
  create_cmd_wrapper bgwait
  run "$TEST_DIR/run_bgwait" "../oops"
  [ "$status" -eq 1 ]
  [[ "$output" == *"invalid name"* ]]
}

@test "non-numeric timeout and lines are rejected" {
  create_cmd_wrapper bgwait
  run "$TEST_DIR/run_bgwait" --timeout soon j
  [ "$status" -eq 1 ]
  run "$TEST_DIR/run_bgwait" --lines many j
  [ "$status" -eq 1 ]
}

@test "already DONE job returns at once with bgcheck-shaped output" {
  create_cmd_wrapper bgwait
  mkdir -p "$BG_DIR"
  echo "12345" >"$BG_DIR/j.pid"
  echo "7" >"$BG_DIR/j.exit"
  printf 'line1\nline2\n' >"$BG_DIR/j.log"
  run "$TEST_DIR/run_bgwait" j
  [ "$status" -eq 0 ]
  # Exactly what `bgcheck j` prints: status line, header, log tail.
  [ "${lines[0]}" = "DONE exit=7" ]
  [ "${lines[1]}" = "--- last 20 line(s) of $BG_DIR/j.log ---" ]
  [ "${lines[2]}" = "line1" ]
  [ "${lines[3]}" = "line2" ]
  [ "${#lines[@]}" -eq 4 ]
}

@test "--lines bounds the log tail" {
  create_cmd_wrapper bgwait
  mkdir -p "$BG_DIR"
  echo "12345" >"$BG_DIR/j.pid"
  echo "0" >"$BG_DIR/j.exit"
  printf 'a\nb\nc\n' >"$BG_DIR/j.log"
  run "$TEST_DIR/run_bgwait" --lines 1 j
  [ "$status" -eq 0 ]
  [[ "$output" != *$'\na\n'* ]]
  [[ "$output" == *"c"* ]]
}

@test "blocks while RUNNING, then reports the payload's TRUE exit code" {
  create_cmd_wrapper bgwait
  mkdir -p "$BG_DIR"
  sleep 30 &
  live=$!
  echo "$live" >"$BG_DIR/j.pid"
  echo "building" >"$BG_DIR/j.log"
  # Finish the job (exit record, then the process) shortly after bgwait starts.
  (
    sleep 0.5
    echo "3" >"$BG_DIR/j.exit"
    kill "$live"
  ) &
  finisher=$!
  start=$SECONDS
  run "$TEST_DIR/run_bgwait" --timeout 20 j
  wait "$finisher" 2>/dev/null || true
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "DONE exit=3" ]
  [[ "$output" == *"building"* ]]
  # It actually waited for the record (and did not burn the 20 s budget).
  [ $((SECONDS - start)) -lt 15 ]
}

@test "timeout while the job still runs exits 2 and reports RUNNING once" {
  create_cmd_wrapper bgwait
  mkdir -p "$BG_DIR"
  sleep 30 &
  live=$!
  echo "$live" >"$BG_DIR/j.pid"
  echo "still going" >"$BG_DIR/j.log"
  run "$TEST_DIR/run_bgwait" --timeout 1 j
  kill "$live" 2>/dev/null || true
  [ "$status" -eq 2 ]
  [[ "${lines[0]}" == RUNNING\ pid=$live\ * ]]
  [[ "$output" == *"still going"* ]]
  [[ "$output" == *"timed out after 1s"* ]]
}

@test "a dead pid without an exit record exits 4 as EXITED unknown" {
  create_cmd_wrapper bgwait
  mkdir -p "$BG_DIR"
  bash -c 'exit 0' &
  deadpid=$!
  wait "$deadpid"
  echo "$deadpid" >"$BG_DIR/crashed.pid"
  run "$TEST_DIR/run_bgwait" crashed
  [ "$status" -eq 4 ]
  [ "${lines[0]}" = "EXITED unknown" ]
}

@test "a job that dies without an exit record while waiting is noticed" {
  create_cmd_wrapper bgwait
  mkdir -p "$BG_DIR"
  sleep 30 &
  live=$!
  echo "$live" >"$BG_DIR/j.pid"
  : >"$BG_DIR/j.log"
  ( sleep 0.5; kill "$live" ) &
  killer=$!
  run "$TEST_DIR/run_bgwait" --timeout 20 j
  wait "$killer" 2>/dev/null || true
  [ "$status" -eq 4 ]
  [ "${lines[0]}" = "EXITED unknown" ]
}

@test "an over-cap timeout is clamped to 540 with a note" {
  create_cmd_wrapper bgwait
  mkdir -p "$BG_DIR"
  echo "12345" >"$BG_DIR/j.pid"
  echo "0" >"$BG_DIR/j.exit"
  : >"$BG_DIR/j.log"
  run "$TEST_DIR/run_bgwait" --timeout 9999 j
  [ "$status" -eq 0 ]
  [[ "$output" == *"timeout capped at 540s"* ]]
}
