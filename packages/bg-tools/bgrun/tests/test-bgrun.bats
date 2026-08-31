#!/usr/bin/env bats
# bats file_tags=type:unit

if [[ -n ${TEST_SUPPORT:-} ]]; then
  source "$TEST_SUPPORT/test_helper.bash"
else
  source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
fi

@test "help exits 0 and shows usage" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: bgrun"* ]]
}

@test "missing NAME is a usage error" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun"
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing NAME"* ]]
}

@test "invalid NAME is rejected" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun" "../oops" -- true
  [ "$status" -eq 1 ]
  [[ "$output" == *"invalid name"* ]]
}

@test "missing -- separator is a usage error" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun" job1 true
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing -- separator"* ]]
}

@test "missing COMMAND after -- is a usage error" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun" job1 --
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing COMMAND"* ]]
}

@test "launch prints NAME PID LOGPATH and records the true exit code" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun" ok -- bash -c 'echo hi; exit 0'
  [ "$status" -eq 0 ]
  read -r name pid log <<<"$output"
  [ "$name" = "ok" ]
  [[ "$pid" =~ ^[0-9]+$ ]]
  [ "$log" = "$BG_DIR/ok.log" ]
  wait_for_file "$BG_DIR/ok.exit"
  [ "$(cat "$BG_DIR/ok.exit")" = "0" ]
  [[ "$(cat "$log")" == *"hi"* ]]
}

@test "a failing payload's exit code is recorded, not swallowed" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun" boom -- bash -c 'echo before-failure; exit 7'
  [ "$status" -eq 0 ] # the LAUNCH succeeds; only the exit file tells the truth
  wait_for_file "$BG_DIR/boom.exit"
  [ "$(cat "$BG_DIR/boom.exit")" = "7" ]
  [[ "$(cat "$BG_DIR/boom.log")" == *"before-failure"* ]]
}

@test "meta records command, cwd, and start time" {
  create_cmd_wrapper bgrun
  cd "$TEST_DIR"
  run "$TEST_DIR/run_bgrun" meta1 -- echo "a b"
  [ "$status" -eq 0 ]
  wait_for_file "$BG_DIR/meta1.exit"
  grep -q "^cmd: echo" "$BG_DIR/meta1.meta"
  grep -q "^cwd: $TEST_DIR" "$BG_DIR/meta1.meta"
  grep -q "^start: " "$BG_DIR/meta1.meta"
}

@test "refuses a NAME that is still running, exit 2" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun" long -- sleep 30
  [ "$status" -eq 0 ]
  longpid="$(cat "$BG_DIR/long.pid")"
  run "$TEST_DIR/run_bgrun" long -- true
  [ "$status" -eq 2 ]
  [[ "$output" == *"already running"* ]]
  kill "$longpid" 2>/dev/null || true
}

@test "a finished NAME can be relaunched and old exit record is replaced" {
  create_cmd_wrapper bgrun
  run "$TEST_DIR/run_bgrun" again -- bash -c 'exit 5'
  wait_for_file "$BG_DIR/again.exit"
  [ "$(cat "$BG_DIR/again.exit")" = "5" ]
  run "$TEST_DIR/run_bgrun" again -- bash -c 'exit 0'
  [ "$status" -eq 0 ]
  wait_for_file "$BG_DIR/again.exit"
  [ "$(cat "$BG_DIR/again.exit")" = "0" ]
}

@test "--dir overrides the state directory" {
  create_cmd_wrapper bgrun
  alt="$TEST_DIR/alt"
  run "$TEST_DIR/run_bgrun" --dir "$alt" d1 -- true
  [ "$status" -eq 0 ]
  wait_for_file "$alt/d1.exit"
  [ ! -e "$BG_DIR/d1.pid" ]
}
