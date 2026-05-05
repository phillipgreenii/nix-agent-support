# shellcheck shell=bash
# shellcheck disable=SC2317  # False positive: exit inside while loop makes later lines appear "unreachable"

# Unified API for AI agent activity

DEFAULT_MAX_WAIT=7200    # 2 hours in seconds
DEFAULT_CHECK_INTERVAL=5 # 5 seconds

# Command: list
# Display all active agents in formatted table
cmd_list() {
  local claude_list
  claude_list=$("$CLAUDE_CMD" list 2>/dev/null) || claude_list="[]"
  local merged
  merged=$(echo "$claude_list" | jq -c '[.[] | . + {tool: "claude"}]')
  echo "$merged" | format_agent_list
}

# Command: is-agent-active
# Exit 0 if any agent active, 1 if all idle
cmd_is_agent_active() {
  if "$CLAUDE_CMD" is-agent-active 2>/dev/null; then
    exit 0
  fi
  exit 1
}

# Command: clean
# Clean stale markers from all tools
cmd_clean() {
  local before_ids
  before_ids=$(get_all_session_ids)
  "$CLAUDE_CMD" clean >/dev/null 2>&1 || true
  local after_ids
  after_ids=$(get_all_session_ids)
  local counts
  counts=$(diff_session_ids "$before_ids" "$after_ids")
  local _cursor_count claude_count total_count
  read -r _cursor_count claude_count <<<"$counts"
  total_count=$((claude_count))
  echo "Cleaned $total_count sessions ($claude_count claude)"
}

# Command: wait
# Wait for all agents to finish
# shellcheck disable=SC2317  # False positive: exit in loop makes later lines "unreachable"
cmd_wait() {
  local max_wait=$DEFAULT_MAX_WAIT
  local check_interval=$DEFAULT_CHECK_INTERVAL
  local use_caffeinate=false

  # Parse arguments (cmd_wait receives only the options after "wait")
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --maximum-wait)
      [[ -z ${2:-} ]] && {
        echo "Error: --maximum-wait requires a value" >&2
        exit 2
      }
      max_wait="$2"
      shift 2
      ;;
    --time-between-checks)
      [[ -z ${2:-} ]] && {
        echo "Error: --time-between-checks requires a value" >&2
        exit 2
      }
      check_interval="$2"
      shift 2
      ;;
    --caffeinate)
      use_caffeinate=true
      shift
      ;;
    *)
      echo "Error: Unknown option: $1" >&2
      exit 2
      ;;
    esac
  done

  # Validate numeric arguments
  [[ ! $max_wait =~ ^[0-9]+$ ]] && {
    echo "Error: --maximum-wait must be a positive integer" >&2
    exit 2
  }
  [[ ! $check_interval =~ ^[0-9]+$ ]] && {
    echo "Error: --time-between-checks must be a positive integer" >&2
    exit 2
  }

  # Start caffeinate if requested
  if [[ $use_caffeinate == true ]] && command -v caffeinate &>/dev/null; then
    caffeinate -w $$ &
    echo "Started caffeinate to keep Mac awake"
  fi

  local start_time
  start_time=$(date +%s)
  local elapsed=0

  echo "Waiting for agents (max ${max_wait}s, check every ${check_interval}s)..."
  echo ""

  local previous_session_ids=""
  local first_check=true

  # Main wait loop
  while [[ $elapsed -lt $max_wait ]]; do
    # Get current session IDs
    local current_session_ids
    current_session_ids=$(get_all_session_ids | sort)

    # Check if any agent is active
    if ! cmd_is_agent_active 2>/dev/null; then
      echo ""
      echo "All agents finished."
      exit 0
    fi

    # Show full list if first check or set changed
    local claude_list
    claude_list=$("$CLAUDE_CMD" list 2>/dev/null) || claude_list="[]"

    if [[ $first_check == true ]] || [[ $current_session_ids != "$previous_session_ids" ]]; then
      # Get merged list and format
      local merged
      merged=$(echo "$claude_list" | jq -c '[.[] | . + {tool: "claude"}]')

      echo "$merged" | format_agent_list
      echo ""

      first_check=false
    else
      # Show count line
      local claude_count
      claude_count=$(echo "$claude_list" | jq 'length')
      echo "Active sessions: Claude=$claude_count (${elapsed}s elapsed)"
    fi

    previous_session_ids="$current_session_ids"

    # Sleep before next check
    sleep "$check_interval"

    # Update elapsed time
    elapsed=$(($(date +%s) - start_time))
  done

  # Timeout reached
  echo ""
  echo "Timeout: agents still working after ${max_wait}s"
  exit 1
}

# Command: help
cmd_help() {
  cat <<EOF
Usage: agent-activity-api <command> [options]

Commands:
  list              List all active agents with formatted output
  wait [OPTIONS]    Wait for all agents to finish
  is-agent-active   Check if any agent is active (exit 0=active, 1=idle)
  clean             Clean stale markers from all tools
  help              Show this help message
  version           Show version information

Wait Options:
  --maximum-wait SECONDS      Maximum wait time (default: 7200 = 2h)
  --time-between-checks SECS  Check interval (default: 5)
  --caffeinate                Keep Mac awake (macOS only)

Examples:
  # List all active agents
  agent-activity-api list

  # Wait for all agents to finish
  agent-activity-api wait

  # Wait with custom timeout
  agent-activity-api wait --maximum-wait 3600 --caffeinate

  # Check if any agent is active
  agent-activity-api is-agent-active && echo "busy" || echo "idle"

  # Clean stale markers
  agent-activity-api clean

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps/issues>
EOF
}

# Command: version
cmd_version() {
  echo "agent-activity-api $SCRIPT_VERSION"
}

# Main entry point
main() {
  local cmd="${1:-help}"
  [[ $# -gt 0 ]] && shift

  case "$cmd" in
  list)
    cmd_list
    ;;
  is-agent-active)
    cmd_is_agent_active
    ;;
  clean)
    cmd_clean
    ;;
  wait)
    cmd_wait "$@"
    ;;
  help | --help | -h)
    cmd_help
    ;;
  version | --version | -v)
    cmd_version
    ;;
  *)
    echo "Unknown command: $cmd" >&2
    echo "" >&2
    cmd_help
    exit 1
    ;;
  esac
}

main "$@"
