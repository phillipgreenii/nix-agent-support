#!/usr/bin/env bats

# Test suite for claude-activity-lib

setup() {
  if [[ -z ${LIB_PATH:-} ]]; then
    # Local dev: source from source directory
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi

  if [[ -d "${LIB_PATH}" ]]; then
    # LIB_PATH is a directory (local dev) — source the .bash file by name
    source "${LIB_PATH}/claude-activity-lib.bash"
  else
    # LIB_PATH is a file (nix composed library) — source directly
    source "${LIB_PATH}"
  fi

  # Create temporary directory for test state
  TEST_DIR="$(mktemp -d)"
  export CLAUDE_TRACKING_ACTIVITY_DIR="$TEST_DIR/claude-activity"
  export CLAUDE_ACTIVITY_MAX_AGE=1440
  # Re-evaluate ACTIVITY_DIR after setting the env var
  ACTIVITY_DIR="${CLAUDE_TRACKING_ACTIVITY_DIR}"
  MAX_AGE_MINUTES="${CLAUDE_ACTIVITY_MAX_AGE}"
  mkdir -p "$CLAUDE_TRACKING_ACTIVITY_DIR"

  # Create mock pgrep that returns false (Claude not running)
  cat > "$TEST_DIR/pgrep" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "-if" ]] && [[ "$2" == "claude" ]]; then
  if [[ "${MOCK_PGREP_CLAUDE_RUNNING:-0}" == "1" ]]; then
    exit 0
  else
    exit 1
  fi
fi
exit 1
EOF
  chmod +x "$TEST_DIR/pgrep"
  export PATH="$TEST_DIR:$PATH"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "ensure_activity_dir creates directory" {
  local new_dir="$TEST_DIR/new-activity-dir"
  ACTIVITY_DIR="$new_dir"
  [ ! -d "$new_dir" ]
  ensure_activity_dir
  [ -d "$new_dir" ]
}

@test "get_session_path returns correct path" {
  run get_session_path "test-session"
  [ "$status" -eq 0 ]
  [ "$output" = "$CLAUDE_TRACKING_ACTIVITY_DIR/test-session.json" ]
}

@test "get_session_id extracts session_id from JSON" {
  run get_session_id '{"session_id":"abc-123"}'
  [ "$status" -eq 0 ]
  [ "$output" = "abc-123" ]
}

@test "get_session_id returns error for missing session_id" {
  run get_session_id '{}'
  [ "$status" -eq 1 ]
}

@test "get_session_id hashes unsafe characters" {
  run get_session_id '{"session_id":"unsafe/path/../id"}'
  [ "$status" -eq 0 ]
  # Output should be a hex hash (64 chars for sha256)
  [[ ${#output} -eq 64 ]]
  [[ "$output" =~ ^[a-f0-9]+$ ]]
}

@test "get_session_id passes safe characters through" {
  run get_session_id '{"session_id":"safe-id-123"}'
  [ "$status" -eq 0 ]
  [ "$output" = "safe-id-123" ]
}

@test "is_stale returns false for fresh files" {
  local file="$TEST_DIR/fresh.json"
  echo '{}' > "$file"
  run is_stale "$file"
  [ "$status" -ne 0 ]  # not stale
}

@test "is_stale returns true for old files" {
  local file="$TEST_DIR/old.json"
  echo '{}' > "$file"
  # Make file old (2000 minutes)
  local age_seconds=$((2000 * 60))
  local timestamp=$(($(date +%s) - age_seconds))
  perl -e "utime $timestamp, $timestamp, '$file'"
  run is_stale "$file"
  [ "$status" -eq 0 ]  # stale
}

@test "get_active_sessions returns non-stale files" {
  echo '{}' > "$CLAUDE_TRACKING_ACTIVITY_DIR/fresh.json"
  echo '{}' > "$CLAUDE_TRACKING_ACTIVITY_DIR/stale.json"
  # Make stale file old
  local age_seconds=$((2000 * 60))
  local timestamp=$(($(date +%s) - age_seconds))
  perl -e "utime $timestamp, $timestamp, '$CLAUDE_TRACKING_ACTIVITY_DIR/stale.json'"

  run get_active_sessions
  [ "$status" -eq 0 ]
  [[ "$output" == *"fresh.json"* ]]
  [[ "$output" != *"stale.json"* ]]
}

@test "get_active_sessions returns nothing for empty dir" {
  run get_active_sessions
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "count_active_sessions returns correct count" {
  echo '{}' > "$CLAUDE_TRACKING_ACTIVITY_DIR/one.json"
  echo '{}' > "$CLAUDE_TRACKING_ACTIVITY_DIR/two.json"
  run count_active_sessions
  [ "$status" -eq 0 ]
  [ "$output" = "2" ]
}

@test "count_active_sessions returns 0 for empty dir" {
  run count_active_sessions
  [ "$status" -eq 0 ]
  [ "$output" = "0" ]
}

@test "is_claude_running returns false when mock says not running" {
  run is_claude_running
  [ "$status" -eq 1 ]
}

@test "is_claude_running returns true when mock says running" {
  export MOCK_PGREP_CLAUDE_RUNNING=1
  run is_claude_running
  [ "$status" -eq 0 ]
}

@test "log writes to stderr with prefix" {
  export LOG_PREFIX="test-prefix"
  run log "test message"
  [ "$status" -eq 0 ]
  [[ "$output" == "[test-prefix] test message" ]]
}
