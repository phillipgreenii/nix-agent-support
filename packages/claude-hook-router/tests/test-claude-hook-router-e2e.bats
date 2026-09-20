#!/usr/bin/env bats
# bats file_tags=type:e2e
#
# End-to-end test for claude-hook-router (ADR 0071, packet B3): runs small
# chains of stub shell delegates through the REAL built router binary as a
# subprocess (not an in-process Go call), asserting the final merged
# hookSpecificOutput on stdout for representative scenarios -- including a
# persisted regression scenario standing in for ADR 0049/0051's
# write-protection carve-out, since nothing else in this plan automates that
# check beyond a one-time manual migration-day verification.
#
# Out of scope (see packet B3's Out of scope): the router implementation
# itself (packet B1), the Go-level unit test suite (packet B2), the
# real-Claude-Code Tier 3 validation (packet B4), and wiring this suite into
# checks.* (packet B5).

bats_require_minimum_version 1.5.0

PACKAGE_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
DELEGATES_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/fixtures/delegates" && pwd)"

setup_file() {
  # ROUTER_BIN may already be set by a caller that built the binary itself
  # (e.g. a future nix check, once packet B5 wires this suite into
  # checks.* -- it would inject a package's own built exe path rather than
  # have the check sandbox invoke `go build`). For a local `bats tests/`
  # run, build the real binary once for the whole file and remember its
  # path in BATS_FILE_TMPDIR, which -- unlike a plain shell variable --
  # survives across the separate process each individual @test runs in.
  if [[ -n "${ROUTER_BIN:-}" ]]; then
    return 0
  fi
  ( cd "$PACKAGE_DIR" && go build -o "${BATS_FILE_TMPDIR}/claude-hook-router" ./cmd/claude-hook-router )
  echo "${BATS_FILE_TMPDIR}/claude-hook-router" >"${BATS_FILE_TMPDIR}/router-bin-path"
}

setup() {
  if [[ -n "${ROUTER_BIN:-}" ]]; then
    BIN="$ROUTER_BIN"
  else
    BIN="$(cat "${BATS_FILE_TMPDIR}/router-bin-path")"
  fi

  PLUGIN_ROOT="${BATS_TEST_TMPDIR}/plugin-root"
  PLUGIN_DATA="${BATS_TEST_TMPDIR}/plugin-data"
  PROJECT_DIR="${BATS_TEST_TMPDIR}/project"
  mkdir -p "$PLUGIN_ROOT" "$PLUGIN_DATA" "$PROJECT_DIR"
}

# write_config <json>: writes <json> as this test's router-config.json,
# under this test's own CLAUDE_PLUGIN_ROOT -- exactly where main.go joins
# CLAUDE_PLUGIN_ROOT with the fixed "router-config.json" filename.
write_config() {
  printf '%s' "$1" >"${PLUGIN_ROOT}/router-config.json"
}

# delegate <script> <event> <matcher> <contract> <priority>: renders one
# router-config.json delegate entry (compact JSON) wired to a stub script
# under fixtures/delegates/, exactly as packet A1's nix generator would.
delegate() {
  jq -nc \
    --arg name "$1" \
    --arg event "$2" \
    --arg matcher "$3" \
    --arg command "${DELEGATES_DIR}/$1" \
    --arg contract "$4" \
    --argjson priority "$5" \
    '{name: $name, event: $event, matcher: $matcher, command: $command, contract: $contract, priority: $priority}'
}

# run_router <event-json>: invokes the REAL built router binary as a
# subprocess, piping <event-json> on stdin exactly as Claude Code would,
# capturing stdout ($output) and stderr ($stderr) separately so a
# total-failure fallback's stderr diagnostic can never corrupt the stdout
# JSON assertion below it.
run_router() {
  local input_file="${BATS_TEST_TMPDIR}/event-input.json"
  printf '%s' "$1" >"$input_file"
  run --separate-stderr env \
    CLAUDE_PLUGIN_ROOT="$PLUGIN_ROOT" \
    CLAUDE_PLUGIN_DATA="$PLUGIN_DATA" \
    CLAUDE_PROJECT_DIR="$PROJECT_DIR" \
    "$BIN" <"$input_file"
}

@test "a chain of 3 stub delegates (decide, rewrite, annotate) merges into one hookSpecificOutput" {
  config=$(jq -nc \
    --argjson d1 "$(delegate decide-ask.sh PreToolUse '*' decide 1)" \
    --argjson d2 "$(delegate rewrite-input.sh PreToolUse '*' rewrite 2)" \
    --argjson d3 "$(delegate annotate-note.sh PreToolUse '*' annotate 3)" \
    '{PreToolUse: [$d1, $d2, $d3]}')
  write_config "$config"

  run_router '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo original"}}'

  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  echo "$output" | jq -e '.permissionDecision == "ask"'
  echo "$output" | jq -e '.updatedInput.command == "echo rewritten"'
  echo "$output" | jq -e '.additionalContext == "annotated-by-annotate-note"'
}

@test "an early abstain does not drop a later delegate's contribution" {
  config=$(jq -nc \
    --argjson d1 "$(delegate abstain.sh PreToolUse '*' decide 1)" \
    --argjson d2 "$(delegate annotate-after-abstain.sh PreToolUse '*' annotate 2)" \
    '{PreToolUse: [$d1, $d2]}')
  write_config "$config"

  run_router '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo hi"}}'

  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  # No permissionDecision at all: the abstaining delegate contributed
  # nothing, as expected -- only the later delegate's context must survive.
  echo "$output" | jq -e 'has("permissionDecision") | not'
  echo "$output" | jq -e '.additionalContext == "contributed-after-abstain"'
}

@test "two different events route to their own delegate only" {
  config=$(jq -nc \
    --argjson pre "$(delegate pretooluse-marker.sh PreToolUse '*' annotate 1)" \
    --argjson post "$(delegate posttooluse-marker.sh PostToolUse '*' annotate 1)" \
    '{PreToolUse: [$pre], PostToolUse: [$post]}')
  write_config "$config"

  run_router '{"hook_event_name":"PreToolUse","tool_name":"Bash"}'
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  echo "$output" | jq -e '.additionalContext == "pretooluse-marker"'

  run_router '{"hook_event_name":"PostToolUse","tool_name":"Bash"}'
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  echo "$output" | jq -e '.additionalContext == "posttooluse-marker"'
}

@test "persisted regression: an unmatched PreToolUse delegate denies a Write call (ceta write-protection carve-out)" {
  config=$(jq -nc \
    --argjson d "$(delegate deny-write-edit.sh PreToolUse '*' decide 1)" \
    '{PreToolUse: [$d]}')
  write_config "$config"

  run_router '{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/tmp/x"}}'
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  echo "$output" | jq -e '.permissionDecision == "deny"'
}

@test "persisted regression: the same unmatched delegate does NOT deny a non-Write/Edit call" {
  config=$(jq -nc \
    --argjson d "$(delegate deny-write-edit.sh PreToolUse '*' decide 1)" \
    '{PreToolUse: [$d]}')
  write_config "$config"

  run_router '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}'
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  [ "$output" = "{}" ]
}

@test "a delegate emitting the real nested hookSpecificOutput contract has its decision honored, not abstained (tc-6sfia)" {
  config=$(jq -nc \
    --argjson d "$(delegate decide-nested-allow.sh PreToolUse '*' decide 1)" \
    '{PreToolUse: [$d]}')
  write_config "$config"

  run_router '{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/tmp/x"}}'
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  # Before the fix this was "{}" -- the nested envelope's key was silently
  # dropped by json.Unmarshal and the delegate's real "allow" decision was
  # discarded as abstain.
  echo "$output" | jq -e '.permissionDecision == "allow"'
}
