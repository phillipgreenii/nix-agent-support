# shellcheck shell=bash

# CLI API for querying and managing Claude activity sessions

export LOG_PREFIX="claude-activity-api"

# Command: list
# Display active conversations as JSON array
cmd_list() {
  local sessions=()

  while IFS= read -r file; do
    [[ -n $file ]] || continue
    sessions+=("$(cat "$file")")
  done < <(get_active_sessions)

  if [[ ${#sessions[@]} -eq 0 ]]; then
    echo "[]"
  else
    printf '%s\n' "${sessions[@]}" | jq -s '.'
  fi
}

# Command: is-agent-active
# Exit 0 if any agents are working, 1 if idle
cmd_is_agent_active() {
  # If no Claude processes running, nothing is working
  if ! is_claude_running; then
    exit 1 # Idle - no Claude running
  fi

  # Check for non-stale active sessions
  local count
  count=$(count_active_sessions)

  if [[ $count -gt 0 ]]; then
    exit 0 # Working
  else
    exit 1 # Idle
  fi
}

# Command: clean
# Delete stale or orphaned session files
cmd_clean() {
  local removed=0

  # If no Claude running, ALL files are considered stale
  local claude_running=true
  if ! is_claude_running; then
    claude_running=false
  fi

  # Ensure directory exists before iterating
  [[ -d $ACTIVITY_DIR ]] || {
    echo "Removed 0 stale file(s)"
    return 0
  }

  # Use find to avoid glob expansion issues (e.g., no matches leaving literal *.json)
  while IFS= read -r -d '' file; do
    # Remove if Claude not running OR file is stale
    if [[ $claude_running == false ]] || is_stale "$file"; then
      rm "$file"
      removed=$((removed + 1))
    fi
  done < <(find "$ACTIVITY_DIR" -maxdepth 1 -name "*.json" -type f -print0 2>/dev/null || true)

  echo "Removed $removed stale file(s)"
}

# Command: help
show_help() {
  cat <<EOF
Usage: claude-activity-api <command>

Commands:
  list              List active conversations (JSON array)
  is-agent-active   Exit 0 if working, 1 if idle (for shell tests)
  clean             Remove stale or orphaned session files
  help              Show this help message

Environment Variables:
  XDG_STATE_HOME           Base directory for state files (default: ~/.local/state)
  CLAUDE_ACTIVITY_MAX_AGE  Maximum age in minutes before stale (default: 720)

Session files are stored at:
  \${XDG_STATE_HOME:-\$HOME/.local/state}/claude-activity/

Files are considered stale if:
  - They are older than CLAUDE_ACTIVITY_MAX_AGE minutes, OR
  - No Claude processes are running (all files are stale)

Examples:
  # Check if agents are working
  claude-activity-api is-agent-active && echo "working" || echo "idle"

  # List active sessions
  claude-activity-api list

  # Clean up stale files
  claude-activity-api clean

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps/issues>
EOF
}

main() {
  local cmd="${1:-help}"

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
  help | -h | --help)
    show_help
    ;;
  *)
    echo "Unknown command: $cmd" >&2
    echo "" >&2
    show_help
    exit 1
    ;;
  esac
}

main "$@"
