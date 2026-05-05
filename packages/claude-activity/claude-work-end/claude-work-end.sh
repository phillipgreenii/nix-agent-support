# shellcheck shell=bash

# Claude hook script for Stop event
# Removes the session file when agent work completes

show_help() {
  cat <<EOF
Usage: claude-work-end [OPTIONS]

Internal Claude hook script for Stop event.
Removes the session file when agent work completes.

This script is called automatically by Claude and should not be
invoked manually. It reads JSON payload from stdin.

Options:
  -h, --help     Show this help message and exit
  -v, --version  Show version information

Session files are stored at:
  \${XDG_STATE_HOME:-\$HOME/.local/state}/claude-activity/

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps/issues>
EOF
}

# Handle flags
case "${1:-}" in
-h | --help)
  show_help
  exit 0
  ;;
esac

export LOG_PREFIX="claude-work-end"

main() {
  # Read JSON payload from stdin
  local payload
  payload=$(cat)

  # Extract and normalize session ID
  local sess_id
  if ! sess_id=$(get_session_id "$payload"); then
    log "ERROR: No session_id in payload"
    exit 0 # Don't fail the hook
  fi

  local session_path
  session_path=$(get_session_path "$sess_id")

  # Warn if session file doesn't exist
  if [[ ! -f $session_path ]]; then
    log "WARNING: No session file found: $sess_id"
    exit 0
  fi

  # Remove the session file
  rm "$session_path"
  log "Removed session: $sess_id"
}

main
