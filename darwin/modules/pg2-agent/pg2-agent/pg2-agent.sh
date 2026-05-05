# shellcheck shell=bash
if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  cat <<'HELP'
pg2-agent: AI agent wrapper with plugin architecture

Purpose: Provides a unified interface to AI agents. Agents are registered
via Nix configuration and tried in priority order until one succeeds.

Usage: pg2-agent [OPTIONS] [PROMPT]

Options:
  --model MODEL    Model to use: small, medium (default), large
  --plan           Use plan mode (read-only). Default is full mode
  --thinking       Enable extended thinking/reasoning mode
  -h, --help       Show this help message and exit
  -v, --version    Show version information

Prompt:
  Can be provided as an argument or via stdin (pipe-friendly).
  If both are provided, stdin is used as context and argument as instruction.

Registered Agents:
HELP

  # Show registered agents from JSON config
  if [[ -z ${AGENTS_CONFIG:-} ]] || [[ ! -f ${AGENTS_CONFIG} ]]; then
    echo "  (No agents config found)"
  elif [[ $(jq 'length' "$AGENTS_CONFIG") -eq 0 ]]; then
    echo "  (No agents registered - enable claude-code program)"
  else
    while IFS= read -r line; do
      echo "  - $line"
    done < <(jq -r '.[] | "\(.id) (priority: \(.priority))"' "$AGENTS_CONFIG")
  fi

  cat <<'HELP'

Examples:
  # Default: full mode, medium model
  pg2-agent "Refactor this function"

  # Plan mode (read-only) for analysis
  pg2-agent --plan "Analyze this code"

  # Use large model with thinking
  pg2-agent --model large --thinking "Analyze this complex algorithm"

  # Pipe context via stdin
  cat context.txt | pg2-agent "Fill in this template"

  # Combined: stdin for context, arg for instruction
  echo "$template" | pg2-agent "Fill in this PR template"

Exit Codes:
  0  - Success
  1  - All agents failed or no agents registered
  11 - Authentication error (all agents)
  12 - License/usage error (all agents)
HELP
  exit 0
fi

# Load agents from JSON config
if [[ -z ${AGENTS_CONFIG:-} ]] || [[ ! -f ${AGENTS_CONFIG} ]]; then
  echo "Error: AGENTS_CONFIG not set or file not found." >&2
  exit 1
fi

agent_count=$(jq 'length' "$AGENTS_CONFIG")
if [[ $agent_count -eq 0 ]]; then
  echo "Error: No AI agents registered. Enable claude-code program." >&2
  exit 1
fi

# Parse arguments
model="medium"
plan_mode="false"
thinking="false"
prompt=""

while [[ $# -gt 0 ]]; do
  case "${1}" in
  --model)
    model="${2}"
    shift 2
    ;;
  --plan)
    plan_mode="true"
    shift
    ;;
  --thinking)
    thinking="true"
    shift
    ;;
  *)
    prompt="${1}"
    shift
    ;;
  esac
done

# Validate model
case "${model}" in
small | medium | large) ;;
*)
  echo "Error: Invalid model ${model}. Must be: small, medium, or large" >&2
  exit 1
  ;;
esac

# Read from stdin if available
stdin_content=""
if [[ ! -t 0 ]]; then
  stdin_content=$(cat)
fi

# Combine stdin and prompt
if [[ -n ${stdin_content} && -n ${prompt} ]]; then
  full_prompt="${stdin_content}

${prompt}"
elif [[ -n ${stdin_content} ]]; then
  full_prompt="${stdin_content}"
elif [[ -n ${prompt} ]]; then
  full_prompt="${prompt}"
else
  full_prompt="-"
fi

# Try each agent in priority order (JSON is pre-sorted by Nix)
last_exit_code=1
auth_errors=0
license_errors=0

while IFS= read -r agent_json; do
  agent_id=$(echo "$agent_json" | jq -r '.id')
  agent_priority=$(echo "$agent_json" | jq -r '.priority')
  agent_script=$(echo "$agent_json" | jq -r '.script')

  echo "Trying $agent_id (priority $agent_priority)..." >&2

  if output=$(echo "$full_prompt" | "$agent_script" "$model" "$plan_mode" "$thinking" "-" 2>&1); then
    agent_exit=$?
  else
    agent_exit=$?
  fi

  case $agent_exit in
  0)
    echo "$output"
    exit 0
    ;;
  11)
    echo "$agent_id: Authentication error" >&2
    auth_errors=$((auth_errors + 1))
    last_exit_code=11
    ;;
  12)
    echo "$agent_id: License/usage limit" >&2
    license_errors=$((license_errors + 1))
    last_exit_code=12
    ;;
  *)
    echo "$agent_id: Failed with exit code $agent_exit" >&2
    last_exit_code=1
    ;;
  esac
done < <(jq -c '.[]' "$AGENTS_CONFIG")

# All agents failed
echo "Error: All agents failed" >&2

if [[ $auth_errors -eq $agent_count ]]; then
  echo "All agents reported authentication errors" >&2
  exit 11
elif [[ $license_errors -eq $agent_count ]]; then
  echo "All agents reported license/usage errors" >&2
  exit 12
else
  exit $last_exit_code
fi
