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

@test "claude-activity-api list returns empty array when no sessions" {
  run run_cmd claude-activity-api list
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

@test "claude-activity-api list returns sessions as JSON array" {
  create_test_session "session1"
  create_test_session "session2"

  run run_cmd claude-activity-api list
  [ "$status" -eq 0 ]

  # Should be valid JSON array with 2 elements
  local length
  length=$(echo "$output" | jq 'length')
  [ "$length" = "2" ]
}

@test "claude-activity-api is-agent-active exits 1 when no Claude running" {
  run run_cmd claude-activity-api is-agent-active
  [ "$status" -eq 1 ]
}

@test "claude-activity-api is-agent-active ignores stale files" {
  # Create a stale session (older than MAX_AGE)
  create_test_session "stale-session" 2000

  # Should still report as idle since file is stale
  run run_cmd claude-activity-api is-agent-active
  [ "$status" -eq 1 ]
}

@test "claude-activity-api clean removes old files" {
  create_test_session "fresh" 0
  create_test_session "stale" 2000

  # Mock Claude as running so only stale files are removed (not all files)
  export MOCK_PGREP_CLAUDE_RUNNING=1
  # Recreate wrapper so it picks up the new env
  create_cmd_wrapper

  run run_cmd claude-activity-api clean
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Removed 1 stale file(s)" ]]

  # Fresh file should still exist
  [ -f "$CLAUDE_TRACKING_ACTIVITY_DIR/fresh.json" ]
  # Stale file should be removed
  [ ! -f "$CLAUDE_TRACKING_ACTIVITY_DIR/stale.json" ]
}

@test "claude-activity-api clean handles empty directory" {
  run run_cmd claude-activity-api clean
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Removed 0 stale file(s)" ]]
}

@test "claude-activity-api help shows usage" {
  run run_cmd claude-activity-api help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: claude-activity-api"* ]]
}
