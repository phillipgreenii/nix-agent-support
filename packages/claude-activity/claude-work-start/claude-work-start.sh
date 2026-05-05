# shellcheck shell=bash

# Claude hook script for UserPromptSubmit event
# Creates a session file to track active agent work

show_help() {
  cat <<EOF
Usage: claude-work-start [OPTIONS]

Internal Claude hook script for UserPromptSubmit event.
Creates a session file to track active agent work.

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

export LOG_PREFIX="claude-work-start"

main() {
  ensure_activity_dir

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

  # Warn if sentinel already exists
  if [[ -f $session_path ]]; then
    log "WARNING: Session file already exists: $sess_id"
    exit 0
  fi

  # Extract prompt from payload (safely, default to empty)
  local prompt
  prompt=$(echo "$payload" | jq -r '.user_prompt // .prompt // ""')

  # Create enhanced JSON session file
  jq -n \
    --arg tool "claude" \
    --arg session_id "$sess_id" \
    --arg start_time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg working_directory "$PWD" \
    --arg prompt "$prompt" \
    '{tool: $tool, session_id: $session_id, start_time: $start_time, working_directory: $working_directory, prompt: $prompt}' >"$session_path"

  if [[ -n $prompt ]]; then
    log "Created session: $sess_id ($(echo "$prompt" | cut -c1-40)...)"
  else
    log "Created session: $sess_id"
  fi
}

main
