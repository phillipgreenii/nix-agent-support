#!/usr/bin/env bats

# Test suite for agent-activity-lib

setup() {
  if [[ -z ${LIB_PATH:-} ]]; then
    # Local dev: source from source directory
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi

  if [[ -d "${LIB_PATH}" ]]; then
    # LIB_PATH is a directory (local dev) — source the .bash file by name
    source "${LIB_PATH}/agent-activity-lib.bash"
  else
    # LIB_PATH is a file (nix composed library) — source directly
    source "${LIB_PATH}"
  fi

  # Create temporary directory for test state
  TEST_DIR="$(mktemp -d)"

  # Create mock claude-activity-api
  cat >"$TEST_DIR/claude-activity-api" <<MOCK
#!/usr/bin/env bash
case "\$1" in
  list)
    if [[ -f "$TEST_DIR/mock-claude-list-response" ]]; then
      cat "$TEST_DIR/mock-claude-list-response"
    else
      echo "[]"
    fi
    ;;
esac
MOCK
  chmod +x "$TEST_DIR/claude-activity-api"
  export CLAUDE_ACTIVITY_CMD="$TEST_DIR/claude-activity-api"
  CLAUDE_CMD="$TEST_DIR/claude-activity-api"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "log writes to stderr with prefix" {
  run log "test message"
  [ "$status" -eq 0 ]
  [[ "$output" == "[agent-activity] test message" ]]
}

@test "calculate_age returns dash for empty input" {
  run calculate_age ""
  [ "$status" -eq 0 ]
  [ "$output" = "-" ]
}

@test "truncate_prompt returns short prompt unchanged" {
  run truncate_prompt "short" 60
  [ "$status" -eq 0 ]
  [ "$output" = "short" ]
}

@test "truncate_prompt truncates long prompt with ellipsis" {
  run truncate_prompt "this is a very long prompt that should be truncated" 10
  [ "$status" -eq 0 ]
  [ "$output" = "this is a ..." ]
}

@test "relative_path returns dash for empty input" {
  run relative_path ""
  [ "$status" -eq 0 ]
  [ "$output" = "-" ]
}

@test "relative_path converts HOME prefix to tilde" {
  run relative_path "$HOME/projects/test"
  [ "$status" -eq 0 ]
  [ "$output" = "~/projects/test" ]
}

@test "relative_path returns non-HOME paths unchanged" {
  run relative_path "/tmp/test"
  [ "$status" -eq 0 ]
  [ "$output" = "/tmp/test" ]
}

@test "get_all_session_ids returns empty for no sessions" {
  run get_all_session_ids
  [ "$status" -eq 0 ]
}

@test "get_all_session_ids returns claude sessions" {
  echo '[{"session_id":"abc123","name":"test"}]' > "$TEST_DIR/mock-claude-list-response"

  run get_all_session_ids
  [ "$status" -eq 0 ]
  [[ "$output" == *"claude:abc123"* ]]
}

@test "format_agent_list shows 'No active agents' for empty array" {
  run bash -c 'source "'"${LIB_PATH}/agent-activity-lib.bash"'" 2>/dev/null || source "'"${LIB_PATH}"'"; echo "[]" | format_agent_list'
  [ "$status" -eq 0 ]
  [[ "$output" == *"No active agents"* ]]
}

@test "diff_session_ids counts removed sessions" {
  local before=$'claude:abc\nclaude:def'
  local after=$'claude:abc'

  run diff_session_ids "$before" "$after"
  [ "$status" -eq 0 ]
  [[ "$output" == "0 1" ]]
}

@test "diff_session_ids returns zeros when nothing changed" {
  local before=$'claude:abc'
  local after=$'claude:abc'

  run diff_session_ids "$before" "$after"
  [ "$status" -eq 0 ]
  [[ "$output" == "0 0" ]]
}
