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

@test "claude-work-end removes session file" {
  create_test_session "remove-me"
  echo '{"session_id":"remove-me"}' | run_cmd claude-work-end 2>/dev/null
  [ ! -f "$CLAUDE_TRACKING_ACTIVITY_DIR/remove-me.json" ]
}

@test "claude-work-end warns on missing session" {
  run bash -c 'echo "{\"session_id\":\"nonexistent\"}" | '"$TEST_DIR"'/run_cmd claude-work-end 2>&1'
  [ "$status" -eq 0 ]
  [[ "$output" =~ "WARNING: No session file found" ]]
}

@test "claude-work-end --help shows usage" {
  run run_cmd claude-work-end --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: claude-work-end"* ]]
}
