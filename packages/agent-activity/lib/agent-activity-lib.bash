# shellcheck shell=bash
# agent-activity-lib.bash
# Shared library for agent-activity orchestration

# Commands (can be overridden for testing)
CLAUDE_CMD="${CLAUDE_ACTIVITY_CMD:-claude-activity-api}"

# Logging function
log() {
  echo "[agent-activity] $*" >&2
}

# Get all session IDs from all APIs
# Returns: newline-separated list of "tool:session_id"
get_all_session_ids() {
  local ids=()

  # Get claude sessions
  if claude_list=$("$CLAUDE_CMD" list 2>/dev/null); then
    while IFS= read -r id; do
      [[ -n $id ]] && ids+=("claude:$id")
    done < <(echo "$claude_list" | jq -r '.[] | .session_id // .name // empty')
  fi

  printf '%s\n' "${ids[@]}"
}

# Calculate age from ISO8601 timestamp to human string
# Args: $1 = ISO8601 timestamp
# Returns: Human duration like "5m", "2h", "3d"
calculate_age() {
  local start_time="$1"

  # Return "-" if empty
  [[ -z $start_time ]] && echo "-" && return

  # Parse ISO8601 to epoch (macOS and Linux compatible)
  local start_epoch
  if start_epoch=$(date -j -f "%Y-%m-%dT%H:%M:%SZ" "$start_time" +%s 2>/dev/null); then
    # macOS date
    :
  elif start_epoch=$(date -d "$start_time" +%s 2>/dev/null); then
    # GNU date
    :
  else
    echo "-"
    return
  fi

  local now_epoch
  now_epoch=$(date +%s)

  local age_seconds=$((now_epoch - start_epoch))

  # Convert to human format
  if [[ $age_seconds -lt 60 ]]; then
    echo "${age_seconds}s"
  elif [[ $age_seconds -lt 3600 ]]; then
    echo "$((age_seconds / 60))m"
  elif [[ $age_seconds -lt 86400 ]]; then
    echo "$((age_seconds / 3600))h"
  else
    echo "$((age_seconds / 86400))d"
  fi
}

# Truncate prompt to specified length with ellipsis
# Args: $1 = prompt, $2 = max_length (default 60)
truncate_prompt() {
  local prompt="$1"
  local max_length="${2:-60}"

  if [[ ${#prompt} -le $max_length ]]; then
    echo "$prompt"
  else
    echo "${prompt:0:max_length}..."
  fi
}

# Convert path to relative to HOME if possible
# Args: $1 = absolute path
relative_path() {
  local path="$1"

  # Return "-" if empty
  [[ -z $path ]] && echo "-" && return

  # If path starts with HOME, make it relative
  if [[ $path == "$HOME"* ]]; then
    echo "~${path#"$HOME"}"
  else
    echo "$path"
  fi
}

# Format agent list as human-readable table
# Reads JSON array from stdin
format_agent_list() {
  local json_array
  json_array=$(cat)

  # Check if empty
  local count
  count=$(echo "$json_array" | jq 'length')
  if [[ $count -eq 0 ]]; then
    echo "No active agents"
    return
  fi

  # Print header
  printf "%-10s %-10s %-15s %-7s %-20s %s\n" "TOOL" "SESSION" "STARTED" "AGE" "DIRECTORY" "PROMPT"

  # Print each agent
  echo "$json_array" | jq -r '.[] | @json' | while IFS= read -r agent_json; do
    # Extract fields with safe defaults
    local tool session_id start_time working_directory prompt
    tool=$(echo "$agent_json" | jq -r '.tool // "unknown"')
    session_id=$(echo "$agent_json" | jq -r '.session_id // .name // "unknown"')
    start_time=$(echo "$agent_json" | jq -r '.start_time // ""')
    working_directory=$(echo "$agent_json" | jq -r '.working_directory // "-"')
    prompt=$(echo "$agent_json" | jq -r '.prompt // ""')

    # Calculate age
    local age
    age=$(calculate_age "$start_time")

    # Format start time (extract HH:MM:SS)
    local start_display
    if [[ -n $start_time ]]; then
      start_display="${start_time/*T/}"
      start_display="${start_display/Z/ UTC}"
    else
      start_display="-"
    fi

    # Truncate session to 8 chars
    local session_short="${session_id:0:8}"

    # Make directory relative
    local dir_display
    dir_display=$(relative_path "$working_directory")

    # Truncate prompt
    local prompt_display
    prompt_display=$(truncate_prompt "$prompt" 30)

    # Print formatted line
    printf "%-10s %-10s %-15s %-7s %-20s %s\n" \
      "$tool" "$session_short" "$start_display" "$age" "$dir_display" "$prompt_display"
  done
}

# Diff two session ID sets (newline separated)
# Args: $1 = before IDs, $2 = after IDs
# Returns: count of removed sessions per tool
diff_session_ids() {
  local before="$1"
  local after="$2"

  local removed
  removed=$(comm -23 <(echo "$before" | sort) <(echo "$after" | sort))

  local claude_count=0

  while IFS= read -r id; do
    [[ -z $id ]] && continue
    if [[ $id == claude:* ]]; then
      ((claude_count++))
    fi
  done <<<"$removed"

  echo "0 $claude_count"
}
