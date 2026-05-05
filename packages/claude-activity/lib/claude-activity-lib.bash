# shellcheck shell=bash
# claude-activity-lib.bash
# Shared library for claude-activity tracking

# Directory for session state
# Can be overridden via CLAUDE_TRACKING_ACTIVITY_DIR environment variable
# Otherwise defaults to XDG-compliant location
# Note: Not using 'readonly' to allow tests to reset this between test runs
if [[ -n ${CLAUDE_TRACKING_ACTIVITY_DIR:-} ]]; then
  ACTIVITY_DIR="${CLAUDE_TRACKING_ACTIVITY_DIR}"
else
  ACTIVITY_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/claude-activity"
fi

# Maximum age in minutes before files are considered stale
# Note: Not using 'readonly' to allow tests to reset this between test runs
MAX_AGE_MINUTES="${CLAUDE_ACTIVITY_MAX_AGE:-720}"

# Logging function with customizable prefix
log() {
  echo "[${LOG_PREFIX:-claude-activity}] $*" >&2
}

# Ensure the activity directory exists
ensure_activity_dir() {
  mkdir -p "$ACTIVITY_DIR"
}

# Get the path for a session file
get_session_path() {
  local session_id="$1"
  echo "$ACTIVITY_DIR/$session_id.json"
}

# Check if Claude is currently running
is_claude_running() {
  pgrep -if "claude" >/dev/null 2>&1
}

# Check if a file is stale (older than MAX_AGE_MINUTES)
is_stale() {
  local file="$1"
  local now
  now=$(date +%s)

  # Get file modification time (macOS vs GNU stat)
  local file_time
  if file_time=$(stat -f %m "$file" 2>/dev/null); then
    # macOS stat
    :
  elif file_time=$(stat -c %Y "$file" 2>/dev/null); then
    # GNU stat
    :
  else
    # Can't determine file time, consider it stale
    return 0
  fi

  local age_seconds=$((now - file_time))
  local max_age_seconds=$((MAX_AGE_MINUTES * 60))

  [[ $age_seconds -gt $max_age_seconds ]]
}

# Get list of active (non-stale) session files
get_active_sessions() {
  [[ -d $ACTIVITY_DIR ]] || return

  for file in "$ACTIVITY_DIR"/*.json; do
    # Skip if no files match the glob
    [[ -f $file ]] || continue

    # Skip stale files
    is_stale "$file" && continue

    echo "$file"
  done
}

# Count active sessions
count_active_sessions() {
  local count=0
  while IFS= read -r file; do
    [[ -n $file ]] && ((count++))
  done < <(get_active_sessions)
  echo "$count"
}

# Parse session_id from JSON payload and normalize for filename safety
get_session_id() {
  local payload="$1"
  local id

  # Extract session_id from JSON
  id=$(echo "$payload" | jq -r '.session_id // empty')

  # Return error if no ID found
  [[ -z $id ]] && return 1

  # If ID contains unsafe characters, hash it
  if [[ ! $id =~ ^[a-zA-Z0-9-]+$ ]]; then
    # Use sha256sum (coreutils) with shasum fallback (macOS built-in)
    if command -v sha256sum >/dev/null 2>&1; then
      echo -n "$id" | sha256sum | cut -d' ' -f1
    else
      echo -n "$id" | shasum -a 256 | cut -d' ' -f1
    fi
  else
    echo "$id"
  fi
}
