#!/usr/bin/env bats
# bats file_tags=type:unit

if [[ -n ${TEST_SUPPORT:-} ]]; then
  source "$TEST_SUPPORT/test_helper.bash"
else
  source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
fi

@test "help exits 0 and shows usage" {
  create_cmd_wrapper bgcheck
  run "$TEST_DIR/run_bgcheck" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: bgcheck"* ]]
}

@test "unknown NAME exits 3" {
  create_cmd_wrapper bgcheck
  mkdir -p "$BG_DIR"
  run "$TEST_DIR/run_bgcheck" ghost
  [ "$status" -eq 3 ]
  [[ "$output" == *"no job named 'ghost'"* ]]
}

@test "DONE job: status line plus log tail" {
  create_cmd_wrapper bgcheck
  mkdir -p "$BG_DIR"
  echo "12345" >"$BG_DIR/j.pid"
  echo "0" >"$BG_DIR/j.exit"
  printf 'line1\nline2\n' >"$BG_DIR/j.log"
  run "$TEST_DIR/run_bgcheck" j
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "DONE exit=0" ]
  [[ "$output" == *"line1"* ]]
  [[ "$output" == *"line2"* ]]
}

@test "--lines bounds the log tail" {
  create_cmd_wrapper bgcheck
  mkdir -p "$BG_DIR"
  echo "1" >"$BG_DIR/j.exit"
  printf 'a\nb\nc\n' >"$BG_DIR/j.log"
  run "$TEST_DIR/run_bgcheck" --lines 1 j
  [ "$status" -eq 0 ]
  [[ "$output" != *$'\na\n'* ]]
  [[ "$output" == *"c"* ]]
}

@test "RUNNING job reports pid" {
  create_cmd_wrapper bgcheck
  mkdir -p "$BG_DIR"
  echo "$$" >"$BG_DIR/live.pid"
  : >"$BG_DIR/live.log"
  run "$TEST_DIR/run_bgcheck" live
  [ "$status" -eq 0 ]
  [[ "${lines[0]}" == RUNNING\ pid=$$\ * ]]
}

@test "dead pid without exit record reports EXITED unknown" {
  create_cmd_wrapper bgcheck
  mkdir -p "$BG_DIR"
  bash -c 'exit 0' &
  deadpid=$!
  wait "$deadpid"
  echo "$deadpid" >"$BG_DIR/crashed.pid"
  run "$TEST_DIR/run_bgcheck" crashed
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "EXITED unknown" ]
}

@test "list mode: one line per recorded job" {
  create_cmd_wrapper bgcheck
  mkdir -p "$BG_DIR"
  echo "11111" >"$BG_DIR/a.pid"
  echo "0" >"$BG_DIR/a.exit"
  echo "22222" >"$BG_DIR/b.pid"
  echo "3" >"$BG_DIR/b.exit"
  run "$TEST_DIR/run_bgcheck"
  [ "$status" -eq 0 ]
  [[ "${lines[0]}" == a*"DONE exit=0" ]]
  [[ "${lines[1]}" == b*"DONE exit=3" ]]
}

@test "list mode with nothing recorded notes the empty state dir" {
  create_cmd_wrapper bgcheck
  run "$TEST_DIR/run_bgcheck"
  [ "$status" -eq 0 ]
  [[ "$output" == *"no background jobs recorded"* ]]
}

@test "invalid NAME is rejected" {
  create_cmd_wrapper bgcheck
  run "$TEST_DIR/run_bgcheck" "../oops"
  [ "$status" -eq 1 ]
  [[ "$output" == *"invalid name"* ]]
}
