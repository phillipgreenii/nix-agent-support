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

@test "agent-activity-api list with no agents shows 'No active agents'" {
  run run_cmd agent-activity-api list
  [ "$status" -eq 0 ]
  [[ "$output" =~ "No active agents" ]]
}

@test "agent-activity-api list shows claude agents" {
  echo '{"tool":"claude","session_id":"test456","start_time":"2026-03-02T11:00:00Z","working_directory":"/test2","prompt":"another test"}' \
    > "$CLAUDE_TRACKING_ACTIVITY_DIR/test456.json"

  run run_cmd agent-activity-api list
  [ "$status" -eq 0 ]
  [[ "$output" =~ "claude" ]]
  [[ "$output" =~ "test456" ]]
}

@test "agent-activity-api is-agent-active returns 1 when no agents" {
  run run_cmd agent-activity-api is-agent-active
  [ "$status" -eq 1 ]
}

@test "agent-activity-api is-agent-active returns 0 when claude agent active" {
  # Create fresh marker
  echo '{"tool":"claude","session_id":"active","start_time":"2026-03-02T22:00:00Z"}' \
    > "$CLAUDE_TRACKING_ACTIVITY_DIR/active.json"

  # Mock claude process as running
  export MOCK_PGREP_CLAUDE_RUNNING=1
  # Recreate wrapper so it picks up the new env
  create_cmd_wrapper

  run run_cmd agent-activity-api is-agent-active
  [ "$status" -eq 0 ]
}

@test "agent-activity-api clean removes stale markers" {
  # Simulate Claude running so only age-based staleness applies
  export MOCK_PGREP_CLAUDE_RUNNING=1
  # Recreate wrapper and mock so they pick up the new env
  create_mock_claude_activity_api
  create_cmd_wrapper

  # Create old claude marker (use perl for portable timestamp)
  echo '{"tool":"claude","session_id":"old","start_time":"2020-01-01T00:00:00Z"}' \
    > "$CLAUDE_TRACKING_ACTIVITY_DIR/old.json"
  perl -e "utime 1577836800, 1577836800, '$CLAUDE_TRACKING_ACTIVITY_DIR/old.json'"

  # Create recent marker
  echo '{"tool":"claude","session_id":"recent","start_time":"2026-03-02T22:00:00Z"}' \
    > "$CLAUDE_TRACKING_ACTIVITY_DIR/recent.json"

  run run_cmd agent-activity-api clean
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Cleaned" ]]

  # Old file should be removed, recent should remain
  [ ! -f "$CLAUDE_TRACKING_ACTIVITY_DIR/old.json" ]
  [ -f "$CLAUDE_TRACKING_ACTIVITY_DIR/recent.json" ]
}

@test "agent-activity-api list handles missing fields gracefully" {
  # Create marker with only required fields
  echo '{"session_id":"minimal"}' \
    > "$CLAUDE_TRACKING_ACTIVITY_DIR/minimal.json"

  run run_cmd agent-activity-api list
  [ "$status" -eq 0 ]
  [[ "$output" =~ "minimal" ]]
  # Should show defaults for missing fields
  [[ "$output" =~ "-" ]] || [[ "$output" =~ "unknown" ]]
}

@test "agent-activity-api help shows usage" {
  run run_cmd agent-activity-api help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: agent-activity-api"* ]]
}
