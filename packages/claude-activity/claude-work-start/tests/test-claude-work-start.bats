#!/usr/bin/env bats

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../lib" && pwd)"
  fi

  # Load shared test helpers
  if [[ -n ${BATS_SUPPORT_PATH:-} ]]; then
    source "${BATS_SUPPORT_PATH}/test_helper.bash"
  else
    source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
  fi
  setup_test_env
}

teardown() {
  teardown_test_env
}

@test "claude-work-start creates session file" {
  echo '{"session_id":"test-session-123"}' | run_cmd claude-work-start
  [ -f "$CLAUDE_TRACKING_ACTIVITY_DIR/test-session-123.json" ]
}

@test "claude-work-start creates valid JSON" {
  echo '{"session_id":"test-json"}' | run_cmd claude-work-start

  local session_file="$CLAUDE_TRACKING_ACTIVITY_DIR/test-json.json"
  run jq -e '.session_id' "$session_file"
  [ "$status" -eq 0 ]
  [ "$output" = '"test-json"' ]
  run jq -e '.tool' "$session_file"
  [ "$status" -eq 0 ]
  [ "$output" = '"claude"' ]
  run jq -e '.start_time' "$session_file"
  [ "$status" -eq 0 ]
  run jq -e '.working_directory' "$session_file"
  [ "$status" -eq 0 ]
  run jq -e 'has("prompt")' "$session_file"
  [ "$status" -eq 0 ]
  [ "$output" = "true" ]
}

@test "claude-work-start handles missing session_id gracefully" {
  run bash -c 'echo "{}" | '"$TEST_DIR"'/run_cmd claude-work-start 2>&1'
  [ "$status" -eq 0 ]
  [[ "$output" =~ "ERROR: No session_id" ]]
}

@test "claude-work-start warns on duplicate session" {
  echo '{"session_id":"duplicate"}' | run_cmd claude-work-start 2>/dev/null
  run bash -c 'echo "{\"session_id\":\"duplicate\"}" | '"$TEST_DIR"'/run_cmd claude-work-start 2>&1'
  [ "$status" -eq 0 ]
  [[ "$output" =~ "WARNING: Session file already exists" ]]
}

@test "claude-work-start hashes unsafe session IDs" {
  echo '{"session_id":"unsafe/path/../id"}' | run_cmd claude-work-start 2>/dev/null

  # Should create exactly one file (the hashed version)
  local count
  count=$(find "$CLAUDE_TRACKING_ACTIVITY_DIR" -type f | wc -l | tr -d ' ')
  [ "$count" = "1" ]
}

@test "claude-work-start --help shows usage" {
  run run_cmd claude-work-start --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: claude-work-start"* ]]
}
