# Test helper functions for pg2-agent tests
# Provides mocking for AI agent scripts (plugin architecture)

# Path to pg2-agent scripts directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
fi

# Test isolation - Override HOME to prevent touching real system
setup_test_home() {
  export TEST_HOME="$TEST_DIR/home"
  export HOME="$TEST_HOME"
  mkdir -p "$TEST_HOME"
}

# Create mock agent script
# Usage: create_mock_agent <name> <priority> <behavior> [exit_code]
#   behavior: success, fail, auth_error, license_error
#   exit_code: 0 (success), 1 (fail), 11 (auth), 12 (license)
create_mock_agent() {
  local name="$1"
  local priority="$2"
  local behavior="$3"
  local exit_code="${4:-1}"

  case "$behavior" in
  success)
    exit_code=0
    ;;
  fail)
    exit_code=1
    ;;
  auth_error)
    exit_code=11
    ;;
  license_error)
    exit_code=12
    ;;
  esac

  cat >"$TEST_DIR/${name}-agent" <<EOF
#!/usr/bin/env bash
# Mock $name agent script
# Accepts: <model> <plan> <thinking> <prompt>

model="\$1"
plan="\$2"
thinking="\$3"
prompt="\$4"

# Read from stdin if prompt is "-"
if [[ "\$prompt" == "-" ]]; then
  prompt=\$(cat)
fi

echo "Mock $name agent called: model=\$model plan=\$plan thinking=\$thinking" >&2

case "$behavior" in
  success)
    echo "$name AI response: This is a mock response from $name."
    exit 0
    ;;
  fail)
    echo "Error: $name failed" >&2
    exit 1
    ;;
  auth_error)
    echo "Error: $name authentication failed" >&2
    exit 11
    ;;
  license_error)
    echo "Error: $name license/usage limit" >&2
    exit 12
    ;;
esac

exit $exit_code
EOF
  chmod +x "$TEST_DIR/${name}-agent"
}

# Create AGENTS_CONFIG JSON file from mock agents
# Usage: create_agents_config [agent_entries...]
#   Each entry: "name:priority" (script path auto-derived from TEST_DIR)
#   If no args, creates default config with claude (10)
# shellcheck disable=SC2120
create_agents_config() {
  local entries=("$@")
  if [[ ${#entries[@]} -eq 0 ]]; then
    entries=("claude:10")
  fi

  local json="["
  local first=true
  for entry in "${entries[@]}"; do
    IFS=':' read -r name priority <<<"$entry"
    if [[ $first != true ]]; then
      json+=","
    fi
    json+="{\"id\":\"$name\",\"priority\":$priority,\"script\":\"$TEST_DIR/${name}-agent\"}"
    first=false
  done
  json+="]"

  echo "$json" >"$TEST_DIR/agents.json"
  export AGENTS_CONFIG="$TEST_DIR/agents.json"
}

# Create a wrapper that sources the .sh file with AGENTS_CONFIG set
create_zr_agent_wrapper() {
  create_agents_config

  cat >"$TEST_DIR/pg2-agent" <<EOF
#!/usr/bin/env bash
export AGENTS_CONFIG="$TEST_DIR/agents.json"
source "$SCRIPTS_DIR/pg2-agent.sh"
EOF
  chmod +x "$TEST_DIR/pg2-agent"
  export PATH="$TEST_DIR:$PATH"
}

# Assertions

assert_output_contains() {
  local output="$1"
  local substring="$2"
  echo "$output" | grep -q "$substring"
}

assert_output_not_contains() {
  local output="$1"
  local substring="$2"
  [[ ! $output =~ $substring ]]
}

assert_exit_success() {
  local status="$1"
  [ "$status" -eq 0 ]
}

assert_exit_failure() {
  local status="$1"
  [ "$status" -ne 0 ]
}
