# shellcheck shell=bash
# Shared test helpers for claude-activity script tests

# Set up test environment
# Expects: SCRIPTS_DIR, LIB_PATH to be set by the calling test file's setup()
setup_test_env() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export CLAUDE_TRACKING_ACTIVITY_DIR="$TEST_DIR/claude-activity"
  export CLAUDE_ACTIVITY_MAX_AGE=1440
  mkdir -p "$CLAUDE_TRACKING_ACTIVITY_DIR"

  # Create mock pgrep that returns false (Claude not running)
  create_mock_pgrep

  # Create a wrapper script for running commands that need the library
  # This handles stdin piping correctly
  create_cmd_wrapper
}

teardown_test_env() {
  rm -rf "$TEST_DIR"
}

create_mock_pgrep() {
  cat >"$TEST_DIR/pgrep" <<'EOF'
#!/usr/bin/env bash
# Mock pgrep for testing
# Behavior controlled by MOCK_PGREP_CLAUDE_RUNNING environment variable
if [[ "$1" == "-if" ]] && [[ "$2" == "claude" ]]; then
  if [[ "${MOCK_PGREP_CLAUDE_RUNNING:-0}" == "1" ]]; then
    exit 0  # Claude is running
  else
    exit 1  # Claude is not running
  fi
fi
# For other pgrep calls, return false
exit 1
EOF
  chmod +x "$TEST_DIR/pgrep"
  export PATH="$TEST_DIR:$PATH"
}

# Create a wrapper script that sources the library then runs a command script
# Usage: run run_cmd <script-name> [args...]
# Piping: echo "data" | run run_cmd <script-name>
create_cmd_wrapper() {
  # Resolve LIB_PATH: if it's a directory, point to the .bash file
  local resolved_lib
  if [[ -d ${LIB_PATH} ]]; then
    resolved_lib="${LIB_PATH}/claude-activity-lib.bash"
  else
    resolved_lib="${LIB_PATH}"
  fi

  cat >"$TEST_DIR/run_cmd" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
script_name="\$1"
shift
source "${resolved_lib}"
source "${SCRIPTS_DIR}/\${script_name}.sh"
WRAPPER
  chmod +x "$TEST_DIR/run_cmd"
}

# Helper: Create a test session file
create_test_session() {
  local session_id="$1"
  local age_minutes="${2:-0}"

  local session_dir="${CLAUDE_TRACKING_ACTIVITY_DIR}"
  mkdir -p "$session_dir"

  local session_file="$session_dir/$session_id.json"
  echo '{"name":"'"$session_id"'","start_time":"2026-01-05T10:00:00Z"}' >"$session_file"

  # If age specified, touch file to make it older
  if [[ $age_minutes -gt 0 ]]; then
    # Use perl for portability (handles both GNU/BSD date differences)
    local age_seconds=$((age_minutes * 60))
    local timestamp=$(($(date +%s) - age_seconds))
    perl -e "utime $timestamp, $timestamp, '$session_file'"
  fi
}
