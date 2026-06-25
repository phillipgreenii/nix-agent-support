#!/usr/bin/env bats

# Tests for agent-rules-session-start: the SessionStart hook that injects the
# always-on agent rules into every Claude Code session as additionalContext.
#
# Contract (Claude Code hooks, SessionStart): the script MUST exit 0 and print a
# JSON object on stdout shaped
#   {"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"…"}}
# where additionalContext is the verbatim rules content. (code.claude.com/docs/en/hooks.md)
#
# The rules content is the single source of truth (pgii-agent-rules.md), passed
# to the script via AGENT_RULES_FILE. Tests run the SOURCE .sh directly (mirrors
# packages/git-tools/git-branch-status), so they set AGENT_RULES_FILE explicitly;
# at build time mkBashScript injects the real markdown store path as the default.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi

  # Isolated scenario: generate a fixture rules file in a temp dir so the test
  # never touches real files (project rule: tests that read files use temp dirs).
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  RULES_FIXTURE="$TEST_DIR/pgii-agent-rules.md"
  printf '%s\n' \
    '# Rules' \
    '' \
    '## Always-Apply Rules' \
    '' \
    '- MUST use "double quotes" & a backslash \ and a tab	here' \
    '- MUST preserve newlines' >"$RULES_FIXTURE"
  export RULES_FIXTURE
}

teardown() {
  rm -rf "$TEST_DIR"
}

run_hook() {
  # The hook ignores arguments and stdin (rules are always-on); no args needed.
  run env AGENT_RULES_FILE="$RULES_FIXTURE" \
    bash -euo pipefail "$SCRIPTS_DIR/agent-rules-session-start.sh"
}

@test "agent-rules-session-start exits 0" {
  run_hook
  [ "$status" -eq 0 ]
}

@test "agent-rules-session-start emits valid JSON" {
  run_hook
  echo "$output" | jq -e . >/dev/null
}

@test "agent-rules-session-start declares the SessionStart hook event" {
  run_hook
  local event
  event="$(echo "$output" | jq -r '.hookSpecificOutput.hookEventName')"
  [ "$event" = "SessionStart" ]
}

@test "agent-rules-session-start injects the rules content as additionalContext" {
  run_hook
  local injected expected
  injected="$(echo "$output" | jq -r '.hookSpecificOutput.additionalContext')"
  expected="$(cat "$RULES_FIXTURE")"
  [ "$injected" = "$expected" ]
}

@test "agent-rules-session-start preserves special characters verbatim" {
  run_hook
  local injected
  injected="$(echo "$output" | jq -r '.hookSpecificOutput.additionalContext')"
  [[ "$injected" == *'MUST use "double quotes" & a backslash \ and a tab'* ]]
}

@test "agent-rules-session-start ignores stdin payload (always-on)" {
  run env AGENT_RULES_FILE="$RULES_FIXTURE" bash -c \
    'echo "{\"session_id\":\"abc\",\"source\":\"resume\"}" | bash -euo pipefail "'"$SCRIPTS_DIR"'/agent-rules-session-start.sh"'
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' >/dev/null
}
