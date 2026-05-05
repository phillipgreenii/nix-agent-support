# shellcheck shell=bash
# Shared test helpers for agent-activity script tests

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

  # Create mock claude-activity-api
  create_mock_claude_activity_api

  # Create a wrapper script for running commands that need the library
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

create_mock_claude_activity_api() {
  cat >"$TEST_DIR/claude-activity-api" <<'MOCK'
#!/usr/bin/env bash
# Mock claude-activity-api for testing agent-activity
ACTIVITY_DIR="${CLAUDE_TRACKING_ACTIVITY_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/claude-activity}"
MAX_AGE="${CLAUDE_ACTIVITY_MAX_AGE:-720}"

case "$1" in
  list)
    sessions=()
    if [[ -d "$ACTIVITY_DIR" ]]; then
      for file in "$ACTIVITY_DIR"/*.json; do
        [[ -f "$file" ]] || continue
        # Check staleness
        now=$(date +%s)
        if file_time=$(stat -f %m "$file" 2>/dev/null) || file_time=$(stat -c %Y "$file" 2>/dev/null); then
          age_seconds=$((now - file_time))
          max_age_seconds=$((MAX_AGE * 60))
          [[ $age_seconds -gt $max_age_seconds ]] && continue
        fi
        sessions+=("$(cat "$file")")
      done
    fi
    if [[ ${#sessions[@]} -eq 0 ]]; then
      echo "[]"
    else
      printf '%s\n' "${sessions[@]}" | jq -s '.'
    fi
    ;;
  is-agent-active)
    # Check if Claude is running
    if ! pgrep -if "claude" >/dev/null 2>&1; then
      exit 1
    fi
    # Check for non-stale sessions
    count=0
    if [[ -d "$ACTIVITY_DIR" ]]; then
      now=$(date +%s)
      for file in "$ACTIVITY_DIR"/*.json; do
        [[ -f "$file" ]] || continue
        if file_time=$(stat -f %m "$file" 2>/dev/null) || file_time=$(stat -c %Y "$file" 2>/dev/null); then
          age_seconds=$((now - file_time))
          max_age_seconds=$((MAX_AGE * 60))
          [[ $age_seconds -gt $max_age_seconds ]] && continue
        fi
        ((count++))
      done
    fi
    [[ $count -gt 0 ]] && exit 0 || exit 1
    ;;
  clean)
    claude_running=true
    pgrep -if "claude" >/dev/null 2>&1 || claude_running=false
    removed=0
    if [[ -d "$ACTIVITY_DIR" ]]; then
      now=$(date +%s)
      for file in "$ACTIVITY_DIR"/*.json; do
        [[ -f "$file" ]] || continue
        if [[ "$claude_running" == false ]]; then
          rm "$file"
          ((removed++))
        else
          if file_time=$(stat -f %m "$file" 2>/dev/null) || file_time=$(stat -c %Y "$file" 2>/dev/null); then
            age_seconds=$((now - file_time))
            max_age_seconds=$((MAX_AGE * 60))
            if [[ $age_seconds -gt $max_age_seconds ]]; then
              rm "$file"
              ((removed++))
            fi
          fi
        fi
      done
    fi
    echo "Removed $removed stale file(s)"
    ;;
esac
MOCK
  chmod +x "$TEST_DIR/claude-activity-api"
  export CLAUDE_ACTIVITY_CMD="$TEST_DIR/claude-activity-api"
}

# Create a wrapper script that sources the library then runs a command script
# Usage: run run_cmd <script-name> [args...]
create_cmd_wrapper() {
  # Resolve LIB_PATH: if it's a directory, point to the .bash file
  local resolved_lib
  if [[ -d ${LIB_PATH} ]]; then
    resolved_lib="${LIB_PATH}/agent-activity-lib.bash"
  else
    resolved_lib="${LIB_PATH}"
  fi

  cat >"$TEST_DIR/run_cmd" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
script_name="\$1"
shift
export CLAUDE_ACTIVITY_CMD="${TEST_DIR}/claude-activity-api"
source "${resolved_lib}"
source "${SCRIPTS_DIR}/\${script_name}.sh"
WRAPPER
  chmod +x "$TEST_DIR/run_cmd"
}
